// Package auth_test — authorize_test.go tests the core DB-level authorization
// logic, specifically the slug-vs-name scope matching fix.
//
// AuthorizeDBWithKey is the function under test. It uses ValidateTemplateKey
// (shk_ keys) for the name-based route (/api/db/{name}/exec).
//
// Key scenarios:
//   - slug-scoped key (db:<slug>:w) → PASS (regression test for the bug fix)
//   - name-scoped key (db:<name>:w) → PASS (backward compat with legacy keys)
//   - all:w key                     → PASS (wildcard)
//   - all:r key on exec path        → FAIL (403, read-only)
//   - key for wrong DB              → FAIL (403)
//   - no bearer                     → FAIL (401)
//   - key owned by different user   → FAIL (401)
package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	coreauth "github.com/0xdps/mesahub-core/auth"
	"github.com/0xdps/mesahub-core/cache"
	coreconfig "github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
)

// ── constants ─────────────────────────────────────────────────────────────────

const (
	testOwner  = "usr0abc123"
	testDBName = "cockroach-game"        // display name (differs from slug in SaaS mode)
	testDBSlug = "abc123-cockroach-game" // SaaS slug (userId prefix + display name)
	testDBID   = "11111111-1111-1111-1111-111111111111"

	// Template key raw values (shk_ prefix by convention, but any value works).
	keySlugWrite = "shk_slugwrite_testonly" // db:<slug>:w — canonical form per docs
	keyNameWrite = "shk_namewrite_testonly" // db:<name>:w — legacy/backward-compat
	keyAllWrite  = "shk_allwrite_testonly"  // all:w — wildcard
	keyAllRead   = "shk_allread_testonly"   // all:r — read-only
	keyWrongDB   = "shk_wrongdb_testonly"   // db:other-db:w — wrong target
	keySlugRead  = "shk_slugread_testonly"  // db:<slug>:r — read-only on this db
)

// ── helpers ───────────────────────────────────────────────────────────────────

func hashKey(k string) string {
	s := sha256.Sum256([]byte(k))
	return hex.EncodeToString(s[:])
}

func scopeJSON(scopes []string) string {
	b, _ := json.Marshal(scopes)
	return string(b)
}

// seedRegistry creates a temp registry, seeds the test DB record and all test
// API keys, and wires coreauth to it. The registry is closed via t.Cleanup.
func seedRegistry(t *testing.T) *db.Registry {
	t.Helper()
	dir := t.TempDir()

	reg, err := db.OpenRegistry(dir)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	coreauth.SetRegistry(reg)

	// DB record: Name ≠ Slug, simulating SaaS/control mode where slugs carry
	// a userId prefix while Name is the user-chosen display name.
	if _, err := reg.InsertDatabase(testDBID, testDBName, testDBSlug, testOwner, "dashboard", nil, nil); err != nil {
		t.Fatalf("InsertDatabase: %v", err)
	}

	keys := []struct {
		id, name, raw, scopes string
	}{
		{"k1", "slug-write", keySlugWrite, scopeJSON([]string{"db:" + testDBSlug + ":w"})},
		{"k2", "name-write", keyNameWrite, scopeJSON([]string{"db:" + testDBName + ":w"})},
		{"k3", "all-write", keyAllWrite, scopeJSON([]string{"all:w"})},
		{"k4", "all-read", keyAllRead, scopeJSON([]string{"all:r"})},
		{"k5", "wrong-db", keyWrongDB, scopeJSON([]string{"db:other-db:w"})},
		{"k6", "slug-read", keySlugRead, scopeJSON([]string{"db:" + testDBSlug + ":r"})},
	}
	for _, k := range keys {
		if _, err := reg.InsertAPIKey(k.id, k.name, hashKey(k.raw), k.scopes, testOwner, "user", nil); err != nil {
			t.Fatalf("InsertAPIKey %s: %v", k.id, err)
		}
	}
	return reg
}

func fakeCfg(t *testing.T) *coreconfig.Config {
	return &coreconfig.Config{
		AdminToken:    "test-admin-token",
		SessionSecret: "test-session-secret-32bytes!!!!!",
		DataPath:      t.TempDir(),
	}
}

func fakeRecord() *db.DBRecord {
	return &db.DBRecord{
		ID:     testDBID,
		Name:   testDBName,
		Slug:   testDBSlug,
		Owner:  testOwner,
		Status: "active",
	}
}

// execReq returns POST /api/db/{slug}/exec with the given bearer token.
func execReq(bearer string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/db/"+testDBSlug+"/exec", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

// queryReq returns POST /api/db/{slug}/query with the given bearer token.
func queryReq(bearer string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/db/"+testDBSlug+"/query", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

// ── AuthorizeDB tests ─────────────────────────────────────────────────────────

// TestAuthorizeDB_SlugScopeWrite is the primary regression test for the
// slug-vs-name bug: a key with db:<slug>:w must be accepted on the exec path.
func TestAuthorizeDB_SlugScopeWrite(t *testing.T) {
	seedRegistry(t)
	code, msg := coreauth.AuthorizeDB(execReq(keySlugWrite), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != 0 {
		t.Errorf("slug-scope exec: got %d %q; want 0 (allowed)", code, msg)
	}
}

// TestAuthorizeDB_NameScopeWrite verifies backward compatibility: a legacy key
// with db:<name>:w (display name, not slug) is still accepted.
func TestAuthorizeDB_NameScopeWrite(t *testing.T) {
	seedRegistry(t)
	code, msg := coreauth.AuthorizeDB(execReq(keyNameWrite), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != 0 {
		t.Errorf("name-scope exec: got %d %q; want 0 (allowed)", code, msg)
	}
}

// TestAuthorizeDB_AllWrite verifies that all:w grants exec access.
func TestAuthorizeDB_AllWrite(t *testing.T) {
	seedRegistry(t)
	code, msg := coreauth.AuthorizeDB(execReq(keyAllWrite), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != 0 {
		t.Errorf("all:w exec: got %d %q; want 0 (allowed)", code, msg)
	}
}

// TestAuthorizeDB_ReadOnlyDeniedExec verifies that all:r is rejected for
// exec (write) operations.
func TestAuthorizeDB_ReadOnlyDeniedExec(t *testing.T) {
	seedRegistry(t)
	code, _ := coreauth.AuthorizeDB(execReq(keyAllRead), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != http.StatusForbidden {
		t.Errorf("all:r exec: got %d; want 403", code)
	}
}

// TestAuthorizeDB_SlugReadAllowedQuery verifies db:<slug>:r allows query (read).
func TestAuthorizeDB_SlugReadAllowedQuery(t *testing.T) {
	seedRegistry(t)
	code, msg := coreauth.AuthorizeDB(queryReq(keySlugRead), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != 0 {
		t.Errorf("slug:r query: got %d %q; want 0 (allowed)", code, msg)
	}
}

// TestAuthorizeDB_SlugReadDeniedExec verifies db:<slug>:r is rejected for
// exec (write) operations.
func TestAuthorizeDB_SlugReadDeniedExec(t *testing.T) {
	seedRegistry(t)
	code, _ := coreauth.AuthorizeDB(execReq(keySlugRead), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != http.StatusForbidden {
		t.Errorf("slug:r exec: got %d; want 403", code)
	}
}

// TestAuthorizeDB_WrongDB verifies a key scoped to a different DB is rejected.
func TestAuthorizeDB_WrongDB(t *testing.T) {
	seedRegistry(t)
	code, _ := coreauth.AuthorizeDB(execReq(keyWrongDB), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != http.StatusForbidden {
		t.Errorf("wrong-db exec: got %d; want 403", code)
	}
}

// TestAuthorizeDB_NoBearer verifies missing auth returns 401.
func TestAuthorizeDB_NoBearer(t *testing.T) {
	seedRegistry(t)
	code, _ := coreauth.AuthorizeDB(execReq(""), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != http.StatusUnauthorized {
		t.Errorf("no bearer: got %d; want 401", code)
	}
}

// TestAuthorizeDB_WrongOwner verifies that a key owned by a different user is
// rejected even if the scope matches.
func TestAuthorizeDB_WrongOwner(t *testing.T) {
	reg := seedRegistry(t)

	const otherKey = "shk_otheruser_testonly"
	if _, err := reg.InsertAPIKey("k7", "other-user-key", hashKey(otherKey),
		scopeJSON([]string{"db:" + testDBSlug + ":w"}), "different-user", "user", nil); err != nil {
		t.Fatalf("InsertAPIKey k7: %v", err)
	}
	code, _ := coreauth.AuthorizeDB(execReq(otherKey), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != http.StatusUnauthorized {
		t.Errorf("wrong-owner exec: got %d; want 401", code)
	}
}

// TestAuthorizeDB_AdminKey verifies that a key owned by "admin" has full access
// regardless of scope (standalone mode pattern).
func TestAuthorizeDB_AdminKey(t *testing.T) {
	reg := seedRegistry(t)

	const adminKey = "shk_adminscoped_testonly"
	if _, err := reg.InsertAPIKey("k8", "admin-key", hashKey(adminKey),
		scopeJSON([]string{"all:w"}), "admin", "user", nil); err != nil {
		t.Fatalf("InsertAPIKey k8: %v", err)
	}
	code, msg := coreauth.AuthorizeDB(execReq(adminKey), fakeCfg(t), cache.NewNoop(), fakeRecord())
	if code != 0 {
		t.Errorf("admin-owned key exec: got %d %q; want 0 (allowed)", code, msg)
	}
}
