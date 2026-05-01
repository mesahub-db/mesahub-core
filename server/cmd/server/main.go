package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/0xdps/mesahub-core/auth"
	"github.com/0xdps/mesahub-core/cache"
	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
	"github.com/0xdps/mesahub-core/files"
	"github.com/0xdps/mesahub-core/handler"
	"github.com/0xdps/mesahub-core/middleware"
	"github.com/0xdps/mesahub-core/migrate"
	"github.com/0xdps/mesahub-core/queue"
	"github.com/0xdps/mesahub-core/telemetry"
)

const version = "2.0.0-dev"

func main() {
	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// ── Logging ───────────────────────────────────────────────────────────────
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	if cfg.LogLevel == "debug" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	log.Info().
		Str("version", version).
		Int("port", cfg.Port).
		Msg("sqlite-hub server starting")

	// ── Cache ─────────────────────────────────────────────────────────────────
	cacheClient, err := cache.New(cfg.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("cache init failed")
	}
	{
		mode := cache.ModeOff
		if cfg.RedisURL != "" {
			mode = cache.ModeRedis
		}
		log.Info().Str("mode", mode).Bool("available", cacheClient.Available()).Msg("cache ready")
	}

	// ── DB pool ───────────────────────────────────────────────────────────────
	pool := db.NewPool(cfg.DataPath)
	defer pool.Close()

	// ── Registry ──────────────────────────────────────────────────────────────
	registry, err := db.OpenRegistry(cfg.DataPath)
	if err != nil {
		log.Fatal().Err(err).Msg("registry open failed")
	}
	defer registry.Close()
	auth.SetRegistry(registry)
	registry.SetCache(cacheClient)

	// ── One-time data migrations ───────────────────────────────────────────────
	if err := registry.RunOnce("strip-prefixes", func() error {
		return migrate.StripPrefixes(cfg.DataPath)
	}); err != nil {
		log.Fatal().Err(err).Msg("migration strip-prefixes failed")
	}

	// ── File storage ──────────────────────────────────────────────────────────
	fileStorage, err := files.NewStorage(cfg.DataPath, cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("file storage open failed")
	}
	defer fileStorage.Close()

	// ── Write queue ───────────────────────────────────────────────────────────
	wq := queue.New(cfg.MaxWriteQueueDepth)
	defer wq.Stop()
	// ── Telemetry counters ────────────────────────────────────────────────────────
	tel := telemetry.New()
	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(middleware.StripInternalHeaders)
	r.Use(middleware.Logger)
	r.Use(chimw.Recoverer)
	// Cap request bodies at 10 MB for JSON routes. Upload routes (/import and
	// /files) stream to disk and must not be limited here — they apply their own
	// limits via io.LimitReader inside the handler.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Body != nil && !isUploadPath(req.URL.Path) {
				req.Body = http.MaxBytesReader(w, req.Body, 10<<20)
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Use(auth.CORS(cfg))
	r.Use(auth.AdminStamper(cfg, cacheClient))

	// ── Handlers ──────────────────────────────────────────────────────────────
	dbH := handler.NewDBHandler(cfg, pool, registry)
	queryH := handler.NewQueryHandler(cfg, pool, registry, cacheClient, tel)
	execH := handler.NewExecHandler(cfg, pool, wq, registry, cacheClient, tel)
	restH := handler.NewRestHandler(cfg, pool, wq, registry, cacheClient, tel)
	metricsH := handler.NewMetricsHandler(cfg, registry, fileStorage, wq, tel)
	maintenanceH := handler.NewMaintenanceHandler(cfg, registry, fileStorage)
	systemH := handler.NewSystemHandler(cfg, wq)
	authH := auth.NewHandler(cfg, cacheClient)
	apiKeysH := handler.NewAPIKeysHandler(registry)
	importExportH := handler.NewImportExportHandler(cfg, pool, wq, registry, cacheClient)
	bucketAdminH := handler.NewBucketAdminHandler(cfg, registry, files.NewLocalBucketStorage(fileStorage), cacheClient)

	// Health + version — no auth required
	r.Get("/api/health", handler.Health)
	r.Get("/api/version", handler.NewVersion(version, cacheClient.Available()))

	// Auth routes — public
	r.Post("/api/auth/login", authH.Login)
	r.Post("/api/auth/logout", authH.Logout)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/api/auth/me", authH.Me)
	})

	// ── Admin-only DB management ───────────────────────────────────────────────
	// NOTE: /api/db/deleted must be registered BEFORE /api/db/{name} so chi does
	// not treat the literal "deleted" as a :name capture.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/api/db", dbH.ListDBs)
		r.Post("/api/db", dbH.CreateDB)
		r.Get("/api/db/deleted", dbH.ListDeletedDBs)
		r.Post("/api/db/deleted/{name}", dbH.DeletedDBAction)
		r.Get("/api/db/{name}", dbH.GetDB)
		r.Patch("/api/db/{name}", dbH.PatchDB)
		r.Delete("/api/db/{name}", dbH.DeleteDB)
		r.Get("/api/metrics", metricsH.Metrics)
		r.Post("/api/maintenance/cleanup", maintenanceH.Cleanup)
		// System / internal databases — browse + optional write access.
		r.Get("/api/system/dbs", systemH.ListSystemDBs)
		r.Post("/api/system/db/{name}/query", systemH.QuerySystemDB)
		if cfg.EnableSystemDBWrite {
			r.Post("/api/system/db/{name}/exec", systemH.ExecSystemDB)
		}
		// Admin-managed API keys (shk_ prefix).
		r.Post("/api/apikeys", apiKeysH.CreateAPIKey)
		r.Get("/api/apikeys", apiKeysH.ListAPIKeys)
		r.Delete("/api/apikeys/{id}", apiKeysH.RevokeAPIKey)
		// Admin-managed buckets — management operations (admin only).
		r.Get("/api/buckets", bucketAdminH.ListBuckets)
		r.Post("/api/buckets", bucketAdminH.CreateBucket)
		r.Delete("/api/buckets/{name}", bucketAdminH.DeleteBucket)
	})

	// Bucket file operations — auth handled per-handler (admin OR bucket shk_ key).
	r.Get("/api/buckets/{name}/files", bucketAdminH.ListFiles)
	r.Post("/api/buckets/{name}/files", bucketAdminH.UploadFile)
	// presign-upload must be before {id} routes so chi does not treat it as a file ID.
	r.Post("/api/buckets/{name}/files/presign-upload", bucketAdminH.PresignUploadFile)
	r.Get("/api/buckets/{name}/files/{id}", bucketAdminH.DownloadFile)
	r.Delete("/api/buckets/{name}/files/{id}", bucketAdminH.DeleteFile)
	r.Post("/api/buckets/{name}/files/{id}/presign", bucketAdminH.PresignDownloadFile)
	r.Post("/api/buckets/{name}/tokens/files/revoke", bucketAdminH.RevokeFileToken)

	// ── Per-DB data endpoints (auth handled inside each handler) ──────────────
	r.Post("/api/db/{name}/query", queryH.Query)
	r.Post("/api/db/{name}/exec", execH.Exec)
	// Import / export — registered separately from the JSON routes so the
	// global 10 MB body limit does not apply (isUploadPath returns true for these).
	r.Post("/api/db/{name}/import", importExportH.Import)
	r.Post("/api/db/{name}/export", importExportH.Export)
	// Auto-REST layer — PostgREST-style CRUD for any table.
	r.Get("/api/db/{name}/rest/{table}", restH.Get)
	r.Post("/api/db/{name}/rest/{table}", restH.Post)
	r.Patch("/api/db/{name}/rest/{table}", restH.Patch)
	r.Delete("/api/db/{name}/rest/{table}", restH.Delete)

	// Bucket file shortlinks — no admin session required, only a valid HMAC file token.
	// Registered outside the admin group; auth is enforced inside the handler.
	r.Get("/api/buckets/{name}/files/{id}/download", bucketAdminH.FileShortlink)
	r.Put("/api/buckets/{name}/files/upload", bucketAdminH.UploadShortlink)

	// Legacy /api/v1/* prefix strip — rewrite to /v1/* so old clients still work.
	// /api/v1/query/{ref} → /v1/query/{ref}  (strips the /api prefix, keeps /v1/)
	r.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		if len(p) >= 4 {
			p = p[4:] // strip "/api" → leaves "/v1/..."
		}
		req.URL.Path = p
		if req.URL.RawPath != "" {
			req.URL.RawPath = req.URL.RawPath[4:]
		}
		r.ServeHTTP(w, req)
	}))

	// ── Server ────────────────────────────────────────────────────────────────
	// ReadTimeout and WriteTimeout are intentionally generous to accommodate
	// large SQLite import uploads (up to 100 MB) and export downloads.
	// The ReadHeaderTimeout is kept short to guard against slowloris attacks.
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       300 * time.Second, // large file uploads
		WriteTimeout:      300 * time.Second, // large file exports
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info().Int("port", cfg.Port).Msg("listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	<-quit
	log.Info().Msg("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("shutdown error")
	}
	log.Info().Msg("stopped")
}

// isUploadPath returns true for routes that stream file bodies to disk and
// must not have the global 10 MB MaxBytesReader applied.
func isUploadPath(path string) bool {
	return strings.HasSuffix(path, "/import") ||
		strings.HasSuffix(path, "/files") ||
		strings.Contains(path, "/files/") ||
		strings.HasSuffix(path, "/upload")
}
