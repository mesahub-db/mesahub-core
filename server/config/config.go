package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	// Core
	Port     int
	DataPath string

	// Auth
	AdminToken    string
	SessionSecret string

	// Redis — cache is Redis-backed when REDIS_URL is set, no-op otherwise.
	RedisURL string

	// CORS
	CORSOrigins string

	// Operational limits
	MaxVolumeUsagePct  int
	MaxSQLLength       int
	MaxSQLBindings     int
	MaxWriteQueueDepth int
	FileMaxSizeBytes   int64
	LogLevel           string

	// EnableSystemDBWrite allows admin users to execute write statements
	// against system databases (store.db) via POST /api/system/db/{name}/exec.
	// Disabled by default — set ENABLE_SYSTEM_DB_WRITE=true to enable.
	EnableSystemDBWrite bool
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:                intEnv("PORT", 3000),
		DataPath:            strEnv("DATA_PATH", "/data"),
		AdminToken:          os.Getenv("ADMIN_TOKEN"),
		SessionSecret:       os.Getenv("SESSION_SECRET"),
		RedisURL:            os.Getenv("REDIS_URL"),
		CORSOrigins:         strEnv("CORS_ALLOWED_ORIGINS", ""),
		MaxVolumeUsagePct:   intEnv("MAX_VOLUME_USAGE_PERCENT", 85),
		MaxSQLLength:        intEnv("MAX_SQL_LENGTH", 100_000),
		MaxSQLBindings:      intEnv("MAX_SQL_BINDINGS", 5000),
		MaxWriteQueueDepth:  intEnv("MAX_WRITE_QUEUE_DEPTH", 256),
		FileMaxSizeBytes:    int64(intEnv("FILE_MAX_SIZE_BYTES", 104_857_600)), // 100 MB
		LogLevel:            strEnv("LOG_LEVEL", "info"),
		EnableSystemDBWrite: strEnv("ENABLE_SYSTEM_DB_WRITE", "false") == "true",
	}

	if cfg.AdminToken == "" {
		return nil, fmt.Errorf("ADMIN_TOKEN is required")
	}
	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("SESSION_SECRET is required")
	}
	if os.Getenv("FILE_TOKEN_SIGNING_SECRET") == "" {
		return nil, fmt.Errorf("FILE_TOKEN_SIGNING_SECRET is required")
	}

	return cfg, nil
}

func strEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %s has invalid integer value %q — using default %d\n", key, v, fallback)
		return fallback
	}
	if n < 0 {
		fmt.Fprintf(os.Stderr, "config: %s has negative value %d — using default %d\n", key, n, fallback)
		return fallback
	}
	return n
}
