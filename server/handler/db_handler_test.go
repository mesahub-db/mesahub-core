package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/0xdps/mesahub-core/handler"
)

// dbTestRouter wires DBHandler routes inside an admin-stamped chi router.
func dbTestRouter(e *testEnv) *chi.Mux {
	h := handler.NewDBHandler(e.cfg, e.pool, e.registry)
	return adminRouter(e, func(r chi.Router) {
		// Admin-only routes wrapped in RequireAdmin
		r.Group(func(r chi.Router) {
			r.Use(handler.RequireAdmin)
			r.Get("/api/db", h.ListDBs)
			r.Post("/api/db", h.CreateDB)
			r.Get("/api/db/deleted", h.ListDeletedDBs)
			r.Post("/api/db/deleted/{name}", h.DeletedDBAction)
			r.Patch("/api/db/{name}", h.PatchDB)
			r.Delete("/api/db/{name}", h.DeleteDB)
		})
		// GetDB is not admin-gated in practice; test it without RequireAdmin
		r.Get("/api/db/{name}", h.GetDB)
	})
}

// ── GET /api/db (ListDBs) ─────────────────────────────────────────────────────

func TestDBHandler_ListDBs_Empty(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, raw := envFireRaw(t, router, http.MethodGet, "/api/db", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200", code)
	}
	var arr []any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		t.Fatalf("decode: %v — body: %s", err, raw)
	}
	if len(arr) != 0 {
		t.Errorf("len = %d; want 0 (empty registry)", len(arr))
	}
}

func TestDBHandler_ListDBs_AfterCreate(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "alpha", "owner": "tester",
	})

	code, raw := envFireRaw(t, router, http.MethodGet, "/api/db", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200", code)
	}
	var arr []any
	json.Unmarshal([]byte(raw), &arr)
	if len(arr) != 1 {
		t.Errorf("len = %d; want 1", len(arr))
	}
}

func TestDBHandler_ListDBs_RequiresAdmin(t *testing.T) {
	e := newTestEnv(t)
	h := handler.NewDBHandler(e.cfg, e.pool, e.registry)
	r := noAuthRouter(func(r chi.Router) {
		r.Use(handler.RequireAdmin)
		r.Get("/api/db", h.ListDBs)
	})
	code, _ := envFire(t, r, http.MethodGet, "/api/db", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", code)
	}
}

// ── POST /api/db (CreateDB) ───────────────────────────────────────────────────

func TestDBHandler_CreateDB_OK(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, resp := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "mydb", "owner": "alice",
	})
	if code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 — body: %v", code, resp)
	}
	if resp["name"] != "mydb" {
		t.Errorf("name = %q; want mydb", resp["name"])
	}
	if resp["owner"] != "alice" {
		t.Errorf("owner = %q; want alice", resp["owner"])
	}
}

func TestDBHandler_CreateDB_MissingName(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"owner": "alice",
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing name)", code)
	}
}

func TestDBHandler_CreateDB_MissingOwner(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "mydb",
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing owner)", code)
	}
}

func TestDBHandler_CreateDB_InvalidName_Space(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "my db", "owner": "alice",
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (invalid name with space)", code)
	}
}

func TestDBHandler_CreateDB_InvalidName_Slash(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "my/db", "owner": "alice",
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (invalid name with slash)", code)
	}
}

func TestDBHandler_CreateDB_ValidNameChars(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	// Alphanumeric, hyphens and underscores are all allowed.
	code, resp := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "my_db-2", "owner": "alice",
	})
	if code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 — body: %v", code, resp)
	}
}

func TestDBHandler_CreateDB_Conflict(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "dup", "owner": "alice",
	})
	code, _ := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "dup", "owner": "bob",
	})
	if code != http.StatusConflict {
		t.Errorf("status = %d; want 409 (duplicate)", code)
	}
}

func TestDBHandler_CreateDB_WithDescription(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, resp := envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "descdb", "owner": "alice", "description": "test db",
	})
	if code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 — body: %v", code, resp)
	}
	// description should come back
	if resp["description"] == nil {
		t.Error("expected description field in response")
	}
}

// ── GET /api/db/:name (GetDB) ─────────────────────────────────────────────────

func TestDBHandler_GetDB_OK(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "getme", "owner": "bob",
	})
	code, resp := envFire(t, router, http.MethodGet, "/api/db/getme", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — body: %v", code, resp)
	}
	if resp["name"] != "getme" {
		t.Errorf("name = %q; want getme", resp["name"])
	}
	if resp["exists"] != true {
		t.Errorf("exists = %v; want true", resp["exists"])
	}
}

func TestDBHandler_GetDB_NotFound(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodGet, "/api/db/nope", nil)
	if code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", code)
	}
}

func TestDBHandler_GetDB_HasSizeBytes(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "sized", "owner": "alice",
	})
	code, resp := envFire(t, router, http.MethodGet, "/api/db/sized", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if resp["size_bytes"] == nil {
		t.Error("expected size_bytes field")
	}
}

// ── PATCH /api/db/:name (PatchDB) ─────────────────────────────────────────────

func TestDBHandler_PatchDB_SetStatus_Inactive(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "patchme", "owner": "alice",
	})
	code, resp := envFire(t, router, http.MethodPatch, "/api/db/patchme", map[string]any{
		"action": "set_status", "status": "inactive",
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — body: %v", code, resp)
	}
	if resp["status"] != "inactive" {
		t.Errorf("status = %q; want inactive", resp["status"])
	}
}

func TestDBHandler_PatchDB_SetStatus_BackToActive(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "toggledb", "owner": "alice",
	})
	envFire(t, router, http.MethodPatch, "/api/db/toggledb", map[string]any{
		"action": "set_status", "status": "inactive",
	})
	code, resp := envFire(t, router, http.MethodPatch, "/api/db/toggledb", map[string]any{
		"action": "set_status", "status": "active",
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d — body: %v", code, resp)
	}
	if resp["status"] != "active" {
		t.Errorf("status = %q; want active", resp["status"])
	}
}

func TestDBHandler_PatchDB_SetStatus_Invalid(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "statusdb", "owner": "alice",
	})
	code, _ := envFire(t, router, http.MethodPatch, "/api/db/statusdb", map[string]any{
		"action": "set_status", "status": "suspended",
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (invalid status value)", code)
	}
}

func TestDBHandler_PatchDB_ResetDB(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "resetdb", "owner": "alice",
	})
	// Create a table so the file exists.
	sqlDB, _ := e.pool.Get("resetdb")
	sqlDB.Exec(`CREATE TABLE t (id INTEGER)`)

	code, resp := envFire(t, router, http.MethodPatch, "/api/db/resetdb", map[string]any{
		"action": "reset_db",
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — body: %v", code, resp)
	}
	if resp["success"] != true {
		t.Errorf("success = %v; want true", resp["success"])
	}
}

func TestDBHandler_PatchDB_UnknownAction(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "actdb", "owner": "alice",
	})
	code, _ := envFire(t, router, http.MethodPatch, "/api/db/actdb", map[string]any{
		"action": "nuke_it",
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (unknown action)", code)
	}
}

func TestDBHandler_PatchDB_NotFound(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodPatch, "/api/db/ghost", map[string]any{
		"action": "set_status", "status": "inactive",
	})
	if code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", code)
	}
}

// ── DELETE /api/db/:name (DeleteDB / soft delete) ─────────────────────────────

func TestDBHandler_DeleteDB_SoftDelete(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "delme", "owner": "alice",
	})
	// Create the file so it can be renamed.
	e.pool.Get("delme")

	code, resp := envFire(t, router, http.MethodDelete, "/api/db/delme", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — body: %v", code, resp)
	}
	if resp["success"] != true {
		t.Errorf("success = %v; want true", resp["success"])
	}
}

func TestDBHandler_DeleteDB_NotFound(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodDelete, "/api/db/noexist", nil)
	if code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", code)
	}
}

func TestDBHandler_DeleteDB_DisappearsFromList(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "gone", "owner": "alice",
	})
	e.pool.Get("gone")
	envFire(t, router, http.MethodDelete, "/api/db/gone", nil)

	code, raw := envFireRaw(t, router, http.MethodGet, "/api/db", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var arr []any
	json.Unmarshal([]byte(raw), &arr)
	if len(arr) != 0 {
		t.Errorf("len = %d; want 0 (soft-deleted db should not appear in list)", len(arr))
	}
}

// ── GET /api/db/deleted (ListDeletedDBs) ──────────────────────────────────────

func TestDBHandler_ListDeletedDBs_Empty(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, raw := envFireRaw(t, router, http.MethodGet, "/api/db/deleted", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var arr []any
	json.Unmarshal([]byte(raw), &arr)
	if len(arr) != 0 {
		t.Errorf("len = %d; want 0", len(arr))
	}
}

func TestDBHandler_ListDeletedDBs_AfterDelete(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "softdel", "owner": "alice",
	})
	e.pool.Get("softdel")
	envFire(t, router, http.MethodDelete, "/api/db/softdel", nil)

	code, raw := envFireRaw(t, router, http.MethodGet, "/api/db/deleted", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var arr []any
	json.Unmarshal([]byte(raw), &arr)
	if len(arr) != 1 {
		t.Errorf("len = %d; want 1 soft-deleted db", len(arr))
	}
}

// ── POST /api/db/deleted/:name (DeletedDBAction) ──────────────────────────────

func TestDBHandler_DeletedDBAction_HardDelete(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "hardme", "owner": "alice",
	})
	e.pool.Get("hardme")
	envFire(t, router, http.MethodDelete, "/api/db/hardme", nil)

	// Find the renamed (soft-deleted) entry.
	rows, _ := e.registry.ListDeletedDatabases()
	if len(rows) == 0 {
		t.Fatal("expected 1 soft-deleted row")
	}
	deletedSlug := rows[0].Slug

	code, resp := envFire(t, router, http.MethodPost, "/api/db/deleted/"+deletedSlug, map[string]any{
		"action": "hard_delete",
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — body: %v", code, resp)
	}
	if resp["success"] != true {
		t.Errorf("success = %v; want true", resp["success"])
	}

	// Should be gone from deleted list too.
	remaining, _ := e.registry.ListDeletedDatabases()
	if len(remaining) != 0 {
		t.Errorf("remaining deleted = %d; want 0", len(remaining))
	}
}

func TestDBHandler_DeletedDBAction_Restore(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "restoredb", "owner": "alice",
	})
	e.pool.Get("restoredb")
	envFire(t, router, http.MethodDelete, "/api/db/restoredb", nil)

	rows, _ := e.registry.ListDeletedDatabases()
	if len(rows) == 0 {
		t.Fatal("expected 1 soft-deleted row")
	}
	deletedSlug := rows[0].Slug

	code, resp := envFire(t, router, http.MethodPost, "/api/db/deleted/"+deletedSlug, map[string]any{
		"action": "restore",
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — body: %v", code, resp)
	}
	if resp["name"] != "restoredb" {
		t.Errorf("name = %q; want restoredb", resp["name"])
	}
}

func TestDBHandler_DeletedDBAction_InvalidAction(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	envFire(t, router, http.MethodPost, "/api/db", map[string]any{
		"name": "badact", "owner": "alice",
	})
	e.pool.Get("badact")
	envFire(t, router, http.MethodDelete, "/api/db/badact", nil)

	rows, _ := e.registry.ListDeletedDatabases()
	if len(rows) == 0 {
		t.Fatal("need 1 deleted row")
	}
	deletedSlug := rows[0].Slug

	code, _ := envFire(t, router, http.MethodPost, "/api/db/deleted/"+deletedSlug, map[string]any{
		"action": "teleport",
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", code)
	}
}

func TestDBHandler_DeletedDBAction_NotFoundInDeletedList(t *testing.T) {
	e := newTestEnv(t)
	router := dbTestRouter(e)

	code, _ := envFire(t, router, http.MethodPost, "/api/db/deleted/ghost-123456", map[string]any{
		"action": "hard_delete",
	})
	if code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", code)
	}
}
