package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/health"
	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/AnkitSinha0/forgeops/internal/repositories"
	"github.com/AnkitSinha0/forgeops/internal/secrets"
	"github.com/AnkitSinha0/forgeops/internal/services"
	"github.com/AnkitSinha0/forgeops/internal/testdb"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

// Integration tests: real router, real services, real Postgres (make up && make test-db).

const testKeyring = "1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

type testAPI struct {
	t       *testing.T
	db      *gorm.DB
	router  chi.Router
	envVars *services.EnvVarService
	tenants *services.TenantService
	logs    *observer.ObservedLogs
}

func newAPI(t *testing.T) *testAPI {
	t.Helper()
	db := testdb.New(t)
	keys, err := secrets.ParseKeyring(testKeyring)
	if err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zapcore.InfoLevel)
	projects := repositories.NewProjectRepository(db)
	a := &testAPI{
		t: t, db: db, logs: logs,
		envVars: services.NewEnvVarService(projects, repositories.NewEnvVarRepository(db), keys),
		tenants: services.NewTenantService(repositories.NewTenantRepository(db)),
	}
	a.router = NewRouter(Deps{
		Log:         zap.New(core),
		Health:      health.NewHandler("test", zap.NewNop()),
		Tenants:     a.tenants,
		APIKeys:     services.NewAPIKeyService(repositories.NewAPIKeyRepository(db)),
		Projects:    services.NewProjectService(projects),
		EnvVars:     a.envVars,
		Deployments: services.NewDeploymentService(projects, repositories.NewDeploymentRepository(db)),
	})
	return a
}

func (a *testAPI) newTenant(slug string) (key string, id uuid.UUID) {
	a.t.Helper()
	tn, key, err := a.tenants.Create(context.Background(), strings.ToUpper(slug), slug)
	if err != nil {
		a.t.Fatal(err)
	}
	return key, tn.ID
}

type result struct {
	status int
	body   string
	header http.Header
}

func (a *testAPI) do(method, path, key, body string) result {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return result{rec.Code, rec.Body.String(), rec.Header()}
}

func (a *testAPI) want(status int, method, path, key, body string) result {
	a.t.Helper()
	r := a.do(method, path, key, body)
	if r.status != status {
		a.t.Fatalf("%s %s = %d, want %d: %s", method, path, r.status, status, r.body)
	}
	return r
}

func (a *testAPI) newProject(key, name string) string {
	a.t.Helper()
	r := a.want(201, "POST", "/v1/projects", key,
		fmt.Sprintf(`{"name":%q,"repo_url":"https://github.com/acme/%s"}`, name, name))
	var p struct {
		ID string `json:"id"`
	}
	mustJSON(a.t, r.body, &p)
	return p.ID
}

func mustJSON(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("bad JSON %q: %v", body, err)
	}
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	var e errorBody
	mustJSON(t, body, &e)
	return e.Error.Code
}

// requestIDField removes the part of an error body that differs on every request.
var requestIDField = regexp.MustCompile(`"request_id":"[^"]*"`)

// ── authentication ──────────────────────────────────────────────────────────

// Walks the real router, so a route added later can't quietly skip auth.
func TestEveryV1RouteRequiresAuth(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	id := uuid.NewString()
	checked := 0
	err := chi.Walk(a.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/v1") {
			return nil
		}
		path := strings.NewReplacer("{id}", id, "{key}", "SOME_KEY").Replace(route)
		if r := a.do(method, path, "", ""); r.status != 401 {
			t.Errorf("%s %s without a key = %d, want 401", method, route, r.status)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 13 {
		t.Fatalf("walked only %d routes: the walk is probably not seeing them", checked)
	}
}

func TestBadCredentialsAllLookTheSame(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")

	r := a.want(201, "POST", "/v1/api-keys", key, `{"name":"temp"}`)
	var created struct {
		ID, Key string
	}
	mustJSON(t, r.body, &created)
	a.want(204, "DELETE", "/v1/api-keys/"+created.ID, key, "")

	headers := map[string]string{
		"missing":          "",
		"wrong scheme":     "Basic " + key,
		"no token":         "Bearer",
		"empty token":      "Bearer ",
		"malformed":        "Bearer not-a-key",
		"sql in the token": "Bearer ' OR 1=1 --",
		"unknown":          "Bearer fo_" + strings.Repeat("0", 64),
		"revoked":          "Bearer " + created.Key,
	}
	var first string
	for name, header := range headers {
		req := httptest.NewRequest("GET", "/v1/projects", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		a.router.ServeHTTP(rec, req)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: status %d, WWW-Authenticate %q", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
		body := requestIDField.ReplaceAllString(rec.Body.String(), "")
		if first == "" {
			first = body
		} else if body != first {
			t.Errorf("%s: body %q differs from %q, which reveals why the key failed", name, body, first)
		}
	}

	// The real key still works, and the scheme is case-insensitive per the HTTP spec.
	a.want(200, "GET", "/v1/tenant", key, "")
	req := httptest.NewRequest("GET", "/v1/tenant", nil)
	req.Header.Set("Authorization", "bearer "+key)
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("lowercase scheme = %d, want 200", rec.Code)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, id := a.newTenant("acme")

	r := a.want(201, "POST", "/v1/api-keys", key, `{"name":"ci"}`)
	var created struct {
		ID, Key string
	}
	mustJSON(t, r.body, &created)
	if !strings.HasPrefix(created.Key, "fo_") {
		t.Fatalf("no key in the create response: %s", r.body)
	}
	a.want(200, "GET", "/v1/tenant", created.Key, "")

	// The key never appears again, and the database doesn't hold it either.
	r = a.want(200, "GET", "/v1/api-keys", key, "")
	if strings.Contains(r.body, created.Key) || strings.Contains(r.body, "key_hash") {
		t.Fatalf("list exposes key material: %s", r.body)
	}
	var stored int64
	if err := a.db.Raw("SELECT count(*) FROM api_keys k WHERE k::text LIKE ?", "%"+created.Key[3:]+"%").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatal("the plaintext key is in the database")
	}

	// A revoke applies to the very next request.
	a.want(204, "DELETE", "/v1/api-keys/"+created.ID, key, "")
	a.want(401, "GET", "/v1/tenant", created.Key, "")
	a.want(404, "DELETE", "/v1/api-keys/"+created.ID, key, "")

	r = a.want(200, "GET", "/v1/tenant", key, "")
	if !strings.Contains(r.body, id.String()) {
		t.Fatalf("GET /v1/tenant = %s, want tenant %s", r.body, id)
	}
}

// A database failure must not tell a valid client that its key is bad.
func TestDatabaseFailureIsNotReportedAsBadCredential(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	sqlDB, err := a.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.Close()

	r := a.do("GET", "/v1/projects", key, "")
	if r.status != 500 || errorCode(t, r.body) != "internal" {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if strings.Contains(r.body, "closed") || strings.Contains(r.body, "sql") {
		t.Fatalf("a raw database error reached the client: %s", r.body)
	}
}

// ── tenant isolation ────────────────────────────────────────────────────────

func TestAnotherTenantsResourcesLookLikeTheyDoNotExist(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	keyA, tenantA := a.newTenant("acme")
	keyB, _ := a.newTenant("bravo")

	projectA := a.newProject(keyA, "web")
	a.want(200, "PUT", "/v1/projects/"+projectA+"/env/DB_PASSWORD", keyA, `{"value":"a-secret"}`)
	r := a.want(200, "GET", "/v1/api-keys", keyA, "")
	var keysA listBody[apiKeyView]
	mustJSON(t, r.body, &keysA)
	keyIDA := keysA.Items[0].ID

	absent := uuid.NewString()
	update := `{"name":"stolen","repo_url":"https://evil.example/x","branch":"main"}`
	attacks := []struct{ method, path, body string }{
		{"GET", "/v1/projects/" + projectA, ""},
		{"PUT", "/v1/projects/" + projectA, update},
		{"DELETE", "/v1/projects/" + projectA, ""},
		{"GET", "/v1/projects/" + projectA + "/env", ""},
		{"PUT", "/v1/projects/" + projectA + "/env/DB_PASSWORD", `{"value":"overwritten"}`}, // the key exists: the upsert path
		{"PUT", "/v1/projects/" + projectA + "/env/NEW_KEY", `{"value":"planted"}`},         // the key is new: the insert path
		{"DELETE", "/v1/projects/" + projectA + "/env/DB_PASSWORD", ""},
		{"GET", "/v1/projects/" + projectA + "/deployments", ""},
		{"DELETE", "/v1/api-keys/" + keyIDA, ""},
	}
	for _, at := range attacks {
		got := a.do(at.method, at.path, keyB, at.body)
		if got.status != 404 {
			t.Errorf("%s %s as tenant B = %d, want 404: %s", at.method, at.path, got.status, got.body)
			continue
		}
		// Byte-identical to a resource that really doesn't exist, apart from the request ID.
		missing := a.do(at.method, strings.ReplaceAll(strings.ReplaceAll(at.path, projectA, absent), keyIDA, absent), keyB, at.body)
		if requestIDField.ReplaceAllString(got.body, "") != requestIDField.ReplaceAllString(missing.body, "") {
			t.Errorf("%s %s: response differs from a missing resource:\n%s\n%s", at.method, at.path, got.body, missing.body)
		}
	}

	// B's own lists are empty.
	for _, path := range []string{"/v1/projects"} {
		if r := a.want(200, "GET", path, keyB, ""); strings.Contains(r.body, projectA) {
			t.Errorf("%s shows tenant A's data: %s", path, r.body)
		}
	}

	// A's data is untouched.
	r = a.want(200, "GET", "/v1/projects/"+projectA, keyA, "")
	if !strings.Contains(r.body, `"name":"web"`) {
		t.Fatalf("A's project changed: %s", r.body)
	}
	a.want(200, "GET", "/v1/tenant", keyA, "")
	vars, err := a.envVars.Decrypted(context.Background(), tenantA, uuid.MustParse(projectA))
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 1 || vars["DB_PASSWORD"] != "a-secret" {
		t.Fatalf("A's env vars changed: %d vars", len(vars))
	}
}

// ── request handling ────────────────────────────────────────────────────────

func TestProjectCRUD(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")

	r := a.want(201, "POST", "/v1/projects", key, `{"name":"web","repo_url":"https://github.com/acme/web"}`)
	var p projectView
	mustJSON(t, r.body, &p)
	if p.Branch != "main" || r.header.Get("Location") != "/v1/projects/"+p.ID {
		t.Fatalf("branch %q, Location %q", p.Branch, r.header.Get("Location"))
	}

	if r := a.want(409, "POST", "/v1/projects", key, `{"name":"web","repo_url":"https://github.com/acme/web"}`); errorCode(t, r.body) != "conflict" {
		t.Fatalf("duplicate: %s", r.body)
	}

	// PUT is a full replace, so a missing branch is an error rather than "keep the old one".
	a.want(400, "PUT", "/v1/projects/"+p.ID, key, `{"name":"web","repo_url":"https://github.com/acme/web"}`)
	r = a.want(200, "PUT", "/v1/projects/"+p.ID, key, `{"name":"site","repo_url":"https://github.com/acme/site","branch":"dev"}`)
	var updated projectView
	mustJSON(t, r.body, &updated)
	if updated.Name != "site" || updated.Branch != "dev" || !updated.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("after update: %+v, was %+v", updated, p)
	}

	other := a.newProject(key, "other")
	a.want(409, "PUT", "/v1/projects/"+other, key, `{"name":"site","repo_url":"https://github.com/acme/x","branch":"main"}`)

	a.want(200, "PUT", "/v1/projects/"+p.ID+"/env/A", key, `{"value":"1"}`)
	a.want(204, "DELETE", "/v1/projects/"+p.ID, key, "")
	a.want(404, "GET", "/v1/projects/"+p.ID, key, "")
	a.want(404, "GET", "/v1/projects/"+p.ID+"/env", key, "")
	a.want(404, "DELETE", "/v1/projects/"+p.ID, key, "")
}

func TestBadInputIsRefusedNever500(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, tenantID := a.newTenant("acme")
	project := a.newProject(key, "web")
	repo := `"repo_url":"https://github.com/acme/x"`
	long := strings.Repeat("a", 2<<20)

	cases := []struct {
		name         string
		method, path string
		body         string
		status       int
	}{
		{"tenant_id in body", "POST", "/v1/projects", `{"name":"x",` + repo + `,"tenant_id":"` + tenantID.String() + `"}`, 400},
		{"id in body", "POST", "/v1/projects", `{"name":"x",` + repo + `,"id":"` + uuid.NewString() + `"}`, 400},
		{"unknown field", "POST", "/v1/projects", `{"name":"x",` + repo + `,"admin":true}`, 400},
		{"trailing garbage", "POST", "/v1/projects", `{"name":"x",` + repo + `} extra`, 400},
		{"two objects", "POST", "/v1/projects", `{"name":"x",` + repo + `}{"name":"y",` + repo + `}`, 400},
		{"not JSON", "POST", "/v1/projects", `name=x`, 400},
		{"JSON array", "POST", "/v1/projects", `[]`, 400},
		{"JSON null", "POST", "/v1/projects", `null`, 400},
		{"wrong type", "POST", "/v1/projects", `{"name":1}`, 400},
		{"oversized body", "POST", "/v1/projects", `{"name":"` + long + `"}`, 413},
		{"name with capitals", "POST", "/v1/projects", `{"name":"Web",` + repo + `}`, 400},
		{"name with a NUL", "POST", "/v1/projects", `{"name":"a\u0000b",` + repo + `}`, 400},
		{"name too long", "POST", "/v1/projects", `{"name":"` + strings.Repeat("a", 41) + `",` + repo + `}`, 400},
		{"ssh repo", "POST", "/v1/projects", `{"name":"x","repo_url":"ssh://git@github.com/a/b"}`, 400},
		{"file repo", "POST", "/v1/projects", `{"name":"x","repo_url":"file:///etc/passwd"}`, 400},
		{"http repo", "POST", "/v1/projects", `{"name":"x","repo_url":"http://github.com/a/b"}`, 400},
		{"repo with credentials", "POST", "/v1/projects", `{"name":"x","repo_url":"https://user:tok@github.com/a/b"}`, 400},
		{"repo with a query", "POST", "/v1/projects", `{"name":"x","repo_url":"https://github.com/a/b?x=1"}`, 400},
		{"repo with no host", "POST", "/v1/projects", `{"name":"x","repo_url":"https:///a/b"}`, 400},
		{"repo with a NUL", "POST", "/v1/projects", `{"name":"x","repo_url":"https://a.com/\u0000"}`, 400},
		{"branch as an option", "POST", "/v1/projects", `{"name":"x",` + repo + `,"branch":"--upload-pack=x"}`, 400},
		{"branch with ..", "POST", "/v1/projects", `{"name":"x",` + repo + `,"branch":"a..b"}`, 400},
		{"branch .lock", "POST", "/v1/projects", `{"name":"x",` + repo + `,"branch":"a.lock"}`, 400},
		{"key name with a NUL", "POST", "/v1/api-keys", `{"name":"a\u0000b"}`, 400},
		{"key name blank", "POST", "/v1/api-keys", `{"name":"   "}`, 400},
		{"key name too long", "POST", "/v1/api-keys", `{"name":"` + strings.Repeat("é", 101) + `"}`, 400},
		{"env value missing", "PUT", "/v1/projects/" + project + "/env/A", `{}`, 400},
		{"env value too big", "PUT", "/v1/projects/" + project + "/env/A", `{"value":"` + strings.Repeat("a", 64<<10+1) + `"}`, 400},
		{"env key starts with a digit", "PUT", "/v1/projects/" + project + "/env/1A", `{"value":"x"}`, 400},
		{"env key with a NUL", "PUT", "/v1/projects/" + project + "/env/A%00B", `{"value":"x"}`, 400},
		{"env key too long", "PUT", "/v1/projects/" + project + "/env/" + strings.Repeat("A", 257), `{"value":"x"}`, 400},
		{"delete env key with a NUL", "DELETE", "/v1/projects/" + project + "/env/A%00B", "", 404},
		{"limit zero", "GET", "/v1/projects?limit=0", "", 400},
		{"limit negative", "GET", "/v1/projects?limit=-1", "", 400},
		{"limit too big", "GET", "/v1/projects?limit=101", "", 400},
		{"limit not a number", "GET", "/v1/projects?limit=abc", "", 400},
		{"limit overflow", "GET", "/v1/projects?limit=99999999999999999999", "", 400},
		{"limit badly escaped", "GET", "/v1/projects?limit=%zz", "", 400},
		{"limit with a semicolon", "GET", "/v1/projects?limit=2;x=1", "", 400},
		{"limit given twice", "GET", "/v1/projects?limit=2&limit=abc", "", 400},
		{"cursor badly escaped", "GET", "/v1/projects?cursor=%zz&limit=2", "", 400},
		{"cursor given twice", "GET", "/v1/projects?cursor=a&cursor=b", "", 400},
		{"repo port too big", "POST", "/v1/projects", `{"name":"x","repo_url":"https://a.com:99999999/b"}`, 400},
		{"repo port zero", "POST", "/v1/projects", `{"name":"x","repo_url":"https://a.com:0/b"}`, 400},
		{"cursor not base64", "GET", "/v1/projects?cursor=!!!", "", 400},
		{"cursor with a NUL", "GET", "/v1/projects?cursor=YQBi", "", 400},
		{"env cursor with a NUL", "GET", "/v1/projects/" + project + "/env?cursor=YQBi", "", 400},
		{"time cursor not a time", "GET", "/v1/projects/" + project + "/deployments?cursor=YQBi", "", 400},
		{"time cursor out of range", "GET", "/v1/projects/" + project + "/deployments?cursor=" + b64("999999999999999999."+uuid.NewString()), "", 400},
		{"time cursor negative", "GET", "/v1/projects/" + project + "/deployments?cursor=" + b64("-1."+uuid.NewString()), "", 400},
		{"key cursor bad id", "GET", "/v1/api-keys?cursor=" + b64("1.notauuid"), "", 400},
		{"malformed project id", "GET", "/v1/projects/not-a-uuid", "", 404},
		{"unknown route", "GET", "/v1/nope", "", 404},
		{"wrong method", "PATCH", "/v1/projects/" + project, "", 405},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := a.do(c.method, c.path, key, c.body)
			if r.status != c.status {
				t.Fatalf("status %d, want %d: %.200s", r.status, c.status, r.body)
			}
			var e errorBody
			mustJSON(t, r.body, &e)
			if e.Error.Code == "" || e.Error.RequestID == "" {
				t.Fatalf("the error isn't in the standard shape: %.200s", r.body)
			}
		})
	}

	// Nothing above may have created anything.
	r := a.want(200, "GET", "/v1/projects", key, "")
	var list listBody[projectView]
	mustJSON(t, r.body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("bad requests created rows: %s", r.body)
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestContentTypeIsRequired(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		req := httptest.NewRequest("POST", "/v1/api-keys", strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		a.router.ServeHTTP(rec, req)
		if rec.Code != 415 {
			t.Errorf("Content-Type %q = %d, want 415", ct, rec.Code)
		}
	}
}

func TestResponseHeadersAndRequestID(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("X-Request-Id", "forged\nfake log line")
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("healthz needs no key, got %d", rec.Code)
	}
	if _, err := uuid.Parse(rec.Header().Get("X-Request-Id")); err != nil {
		t.Fatalf("the request ID must be generated here, got %q", rec.Header().Get("X-Request-Id"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing headers: %v", rec.Header())
	}
}

// ── env vars ────────────────────────────────────────────────────────────────

func TestEnvVarsAreEncryptedAndWriteOnly(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, tenantID := a.newTenant("acme")
	project := a.newProject(key, "web")
	const value = "super-secret-value-123"

	r := a.want(200, "PUT", "/v1/projects/"+project+"/env/DB_PASSWORD", key, fmt.Sprintf(`{"value":%q}`, value))
	if strings.Contains(r.body, value) || strings.Contains(r.body, `"value"`) {
		t.Fatalf("the set response exposes the value: %s", r.body)
	}
	var set envVarView
	mustJSON(t, r.body, &set)
	if !set.CreatedAt.Equal(set.UpdatedAt) {
		t.Fatalf("a new variable must have created_at = updated_at: %s", r.body)
	}

	// Setting it again keeps created_at and moves updated_at. Both responses must match
	// what a later read returns.
	time.Sleep(2 * time.Millisecond)
	r = a.want(200, "PUT", "/v1/projects/"+project+"/env/DB_PASSWORD", key, fmt.Sprintf(`{"value":%q}`, value))
	var again envVarView
	mustJSON(t, r.body, &again)
	if !again.CreatedAt.Equal(set.CreatedAt) || !again.UpdatedAt.After(set.UpdatedAt) {
		t.Fatalf("replacing: was %+v, now %+v", set, again)
	}

	r = a.want(200, "GET", "/v1/projects/"+project+"/env", key, "")
	if strings.Contains(r.body, value) || strings.Contains(r.body, `"value"`) || !strings.Contains(r.body, "DB_PASSWORD") {
		t.Fatalf("the list exposes the value or misses the key: %s", r.body)
	}
	var listed listBody[envVarView]
	mustJSON(t, r.body, &listed)
	if !listed.Items[0].CreatedAt.Equal(again.CreatedAt) || !listed.Items[0].UpdatedAt.Equal(again.UpdatedAt) {
		t.Fatalf("the set response %+v disagrees with the list %+v", again, listed.Items[0])
	}

	// On disk: not the plaintext, and sealed by key version 1.
	var stored []byte
	if err := a.db.Raw("SELECT value FROM env_vars WHERE key = 'DB_PASSWORD'").Row().Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(value)) || stored[0] != 1 {
		t.Fatalf("stored value isn't sealed (%d bytes)", len(stored))
	}

	got, err := a.envVars.Decrypted(context.Background(), tenantID, uuid.MustParse(project))
	if err != nil || got["DB_PASSWORD"] != value {
		t.Fatalf("Decrypted didn't return the value: %v", err)
	}

	// An empty value is a real value, distinct from a missing one.
	a.want(200, "PUT", "/v1/projects/"+project+"/env/EMPTY", key, `{"value":""}`)
	got, err = a.envVars.Decrypted(context.Background(), tenantID, uuid.MustParse(project))
	if v, ok := got["EMPTY"]; err != nil || !ok || v != "" {
		t.Fatalf("empty value: %q, %v, %v", v, ok, err)
	}

	a.want(204, "DELETE", "/v1/projects/"+project+"/env/EMPTY", key, "")
	a.want(404, "DELETE", "/v1/projects/"+project+"/env/EMPTY", key, "")
}

// An attacker with database write access copies a ciphertext between rows.
func TestCiphertextCopiedToAnotherRowDoesNotDecrypt(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	keyA, tenantA := a.newTenant("acme")
	keyB, tenantB := a.newTenant("bravo")
	projectA, projectB := a.newProject(keyA, "web"), a.newProject(keyB, "web")
	a.want(200, "PUT", "/v1/projects/"+projectA+"/env/SECRET", keyA, `{"value":"a-secret"}`)
	a.want(200, "PUT", "/v1/projects/"+projectB+"/env/SECRET", keyB, `{"value":"b-value"}`)
	a.want(200, "PUT", "/v1/projects/"+projectB+"/env/OTHER", keyB, `{"value":"b-other"}`)

	copyTo := func(destination, key string) {
		t.Helper()
		err := a.db.Exec(`UPDATE env_vars SET value = (SELECT value FROM env_vars WHERE project_id = ? AND key = 'SECRET')
			WHERE project_id = ? AND key = ?`, projectA, destination, key).Error
		if err != nil {
			t.Fatal(err)
		}
	}

	copyTo(projectB, "SECRET") // another tenant, same key name
	if _, err := a.envVars.Decrypted(context.Background(), tenantB, uuid.MustParse(projectB)); err == nil {
		t.Fatal("another tenant's ciphertext decrypted")
	}
	copyTo(projectA, "SECRET") // no-op, proves A's own row is still fine
	if _, err := a.envVars.Decrypted(context.Background(), tenantA, uuid.MustParse(projectA)); err != nil {
		t.Fatalf("A's value should still decrypt: %v", err)
	}
}

// Clients and proxies may escape characters that didn't need it. It is the same key.
func TestEscapedEnvKeyMeansTheSameKey(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, tenantID := a.newTenant("acme")
	project := a.newProject(key, "web")

	a.want(200, "PUT", "/v1/projects/"+project+"/env/A%5FB", key, `{"value":"1"}`)
	got, err := a.envVars.Decrypted(context.Background(), tenantID, uuid.MustParse(project))
	if err != nil || got["A_B"] != "1" {
		t.Fatalf("stored as %v, %v; want A_B", got, err)
	}
	a.want(204, "DELETE", "/v1/projects/"+project+"/env/A%5FB", key, "")
	a.want(404, "DELETE", "/v1/projects/"+project+"/env/A_B", key, "")
}

// Whatever order the database's collation gives mixed-case keys and hyphenated names,
// walking the pages must match reading everything at once.
func TestPagesMatchOnePageUnderMixedCaseAndHyphens(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	for _, n := range []string{"a", "a-b", "a1", "ab", "a-a", "b-1", "b1", "z-z-z", "0", "0-0"} {
		a.newProject(key, n)
	}
	project := a.newProject(key, "web")
	for _, k := range []string{"a", "B", "_x", "A_b", "Ab", "a_B", "_", "__", "Z", "z9"} {
		a.want(200, "PUT", "/v1/projects/"+project+"/env/"+k, key, `{"value":"x"}`)
	}

	check := func(path string, id func(string) []string) {
		t.Helper()
		all := id(path)
		for _, limit := range []int{1, 2, 3} {
			got := walk(t, a, path, key, limit, func(v map[string]any) string {
				if s, ok := v["key"].(string); ok {
					return s
				}
				return v["name"].(string)
			})
			if strings.Join(got, ",") != strings.Join(all, ",") {
				t.Fatalf("%s at limit %d:\n got %v\nwant %v", path, limit, got, all)
			}
		}
	}
	check("/v1/projects", func(path string) []string {
		return walk(t, a, path, key, 100, func(p projectView) string { return p.Name })
	})
	check("/v1/projects/"+project+"/env", func(path string) []string {
		return walk(t, a, path, key, 100, func(e envVarView) string { return e.Key })
	})
}

func TestConcurrentSetsOfOneKeyLeaveOneRow(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, tenantID := a.newTenant("acme")
	project := a.newProject(key, "web")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if r := a.do("PUT", "/v1/projects/"+project+"/env/SHARED", key, fmt.Sprintf(`{"value":"%d"}`, i)); r.status != 200 {
				t.Errorf("status %d: %s", r.status, r.body)
			}
		})
	}
	wg.Wait()

	var rows int64
	if err := a.db.Raw("SELECT count(*) FROM env_vars WHERE key = 'SHARED'").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d rows, want 1", rows)
	}
	if _, err := a.envVars.Decrypted(context.Background(), tenantID, uuid.MustParse(project)); err != nil {
		t.Fatalf("the surviving value must decrypt: %v", err)
	}
}

// ── pagination ──────────────────────────────────────────────────────────────

func TestEmptyListsAreEmptyArraysNotNull(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	project := a.newProject(key, "web")
	for _, path := range []string{"/v1/projects/" + project + "/env", "/v1/projects/" + project + "/deployments"} {
		r := a.want(200, "GET", path, key, "")
		if !strings.Contains(r.body, `"items":[]`) || !strings.Contains(r.body, `"next_cursor":null`) {
			t.Errorf("%s = %s", path, r.body)
		}
	}
}

// walk follows next_cursor from the first page to the last and returns every page's IDs.
func walk[V any](t *testing.T, a *testAPI, path, key string, limit int, id func(V) string) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
		url := fmt.Sprintf("%s?limit=%d", path, limit)
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		r := a.want(200, "GET", url, key, "")
		var page listBody[V]
		mustJSON(t, r.body, &page)
		if len(page.Items) > limit {
			t.Fatalf("page has %d items, limit is %d", len(page.Items), limit)
		}
		for _, item := range page.Items {
			ids = append(ids, id(item))
		}
		if page.NextCursor == nil {
			return ids
		}
		cursor = *page.NextCursor
	}
}

func TestPaginationCoversEveryRowOnce(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, tenantID := a.newTenant("acme")
	ctx := context.Background()

	// Projects, including an exact multiple of the page size (no empty last page).
	for i := range 6 {
		a.newProject(key, fmt.Sprintf("p%d", i))
	}
	names := walk(t, a, "/v1/projects", key, 2, func(p projectView) string { return p.Name })
	if strings.Join(names, ",") != "p0,p1,p2,p3,p4,p5" {
		t.Fatalf("projects = %v", names)
	}

	// Env vars.
	project := a.newProject(key, "web")
	for _, k := range []string{"A", "B", "C", "D", "E"} {
		a.want(200, "PUT", "/v1/projects/"+project+"/env/"+k, key, `{"value":"x"}`)
	}
	keys := walk(t, a, "/v1/projects/"+project+"/env", key, 2, func(e envVarView) string { return e.Key })
	if strings.Join(keys, ",") != "A,B,C,D,E" {
		t.Fatalf("env keys = %v", keys)
	}

	// Deployments, all with one timestamp: the case a timestamp-only cursor gets wrong.
	projectID := uuid.MustParse(project)
	build := &models.Build{TenantID: tenantID, ProjectID: projectID, CommitSHA: strings.Repeat("a", 40), Branch: "main"}
	if err := repositories.NewBuildRepository(a.db).Create(ctx, build); err != nil {
		t.Fatal(err)
	}
	same := time.Now().UTC().Truncate(time.Microsecond)
	want := map[string]bool{}
	for range 7 {
		d := &models.Deployment{TenantID: tenantID, ProjectID: projectID, BuildID: build.ID, CreatedAt: same}
		if err := repositories.NewDeploymentRepository(a.db).Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		want[d.ID.String()] = true
	}
	ids := walk(t, a, "/v1/projects/"+project+"/deployments", key, 3, func(d deploymentView) string { return d.ID })
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || !want[id] {
			t.Fatalf("deployment %s repeated or unexpected: %v", id, ids)
		}
		seen[id] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("saw %d of %d deployments", len(seen), len(want))
	}

	// API keys: the initial one plus three more, across pages.
	for i := range 3 {
		a.want(201, "POST", "/v1/api-keys", key, fmt.Sprintf(`{"name":"k%d"}`, i))
	}
	if ks := walk(t, a, "/v1/api-keys", key, 2, func(k apiKeyView) string { return k.ID }); len(ks) != 4 {
		t.Fatalf("api keys = %v", ks)
	}
}

// ── failure behaviour ───────────────────────────────────────────────────────

func TestPanicBecomesAGenericJSON500(t *testing.T) {
	t.Parallel()
	core, logs := observer.New(zapcore.InfoLevel)
	h := &handler{log: zap.New(core)}
	chain := requestID(h.accessLog(h.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("internal detail: password=abc")
	}))))
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 500 || errorCode(t, rec.Body.String()) != "internal" || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if logs.FilterMessage("panic in handler").Len() != 1 {
		t.Fatal("the panic wasn't logged")
	}
	access := logs.FilterMessage("request").All()
	if len(access) != 1 || access[0].ContextMap()["status"] != int64(500) {
		t.Fatalf("the access log must record the 500: %+v", access)
	}
}

// If a tenant route is ever wired outside the auth group, it must fail, not run unscoped.
func TestTenantRouteWithoutAuthFailsClosed(t *testing.T) {
	t.Parallel()
	h := &handler{log: zap.NewNop()}
	called := false
	chain := requestID(h.recoverPanics(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_ = tenantFrom(r.Context())
		called = true
	})))
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || called {
		t.Fatalf("status %d, handler continued: %v", rec.Code, called)
	}
}

func TestExpiredContextIsATimeoutNotA500(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	req := httptest.NewRequest("GET", "/v1/projects", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	if rec.Code != 503 || errorCode(t, rec.Body.String()) != "timeout" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// Credentials and values must not appear in any log line, across a full flow that
// includes failures.
func TestLogsNeverContainCredentialsOrValues(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	project := a.newProject(key, "web")
	const value = "log-canary-secret-value"
	a.want(200, "PUT", "/v1/projects/"+project+"/env/SECRET", key, fmt.Sprintf(`{"value":%q}`, value))
	a.want(400, "PUT", "/v1/projects/"+project+"/env/1BAD", key, fmt.Sprintf(`{"value":%q}`, value))
	a.want(400, "PUT", "/v1/projects/"+project+"/env/SECRET", key, fmt.Sprintf(`{"value":%q,"x":1}`, value))
	r := a.want(201, "POST", "/v1/api-keys", key, `{"name":"second"}`)
	var created struct{ Key string }
	mustJSON(t, r.body, &created)
	a.want(401, "GET", "/v1/projects", "fo_"+strings.Repeat("0", 64), "")

	if a.logs.Len() == 0 {
		t.Fatal("no log lines captured, so this test proves nothing")
	}
	for _, entry := range a.logs.All() {
		line := fmt.Sprintf("%s %v", entry.Message, entry.ContextMap())
		for _, secret := range []string{key, created.Key, value} {
			if strings.Contains(line, secret) {
				t.Fatalf("log line contains a secret: %s", line)
			}
		}
	}
}
