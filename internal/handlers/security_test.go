package handlers

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression tests from the 2026-11-11 security audit (JIT/SecurityAudit-2026-11-11.md
// in the main checkout). Each one was red against f85ea24 and is green after the fix.

// M3: any name matching the key pattern was accepted, including names that change how
// a process starts (LD_PRELOAD) or that the platform will set itself (PORT, FORGEOPS_*).
func TestReservedEnvVarNamesAreRefused(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	pid := a.newProject(key, "web")

	for _, name := range []string{
		"PATH", "path", "HOME", "HOSTNAME", "PORT", "LD_PRELOAD", "LD_LIBRARY_PATH", "ld_audit",
		"DYLD_INSERT_LIBRARIES", "FORGEOPS_TOKEN", "forgeops_x",
	} {
		r := a.do("PUT", "/v1/projects/"+pid+"/env/"+name, key, `{"value":"x"}`)
		if r.status != 400 {
			t.Errorf("PUT env %s = %d, want 400: %s", name, r.status, r.body)
		}
	}
	for _, name := range []string{"NODE_ENV", "DATABASE_URL", "PATHNAME", "MY_PORT", "LDAP_HOST"} {
		a.want(200, "PUT", "/v1/projects/"+pid+"/env/"+name, key, `{"value":"x"}`)
	}
}

// L2: a NUL byte can't be passed to execve and would cut the value short or break an
// env-file. Newlines stay allowed: certificates and keys are multi-line.
func TestEnvVarValueRefusesNUL(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	pid := a.newProject(key, "web")

	a.want(400, "PUT", "/v1/projects/"+pid+"/env/TOKEN", key, `{"value":"a\u0000b"}`)
	a.want(200, "PUT", "/v1/projects/"+pid+"/env/CERT", key, `{"value":"-----BEGIN-----\nabc\n-----END-----"}`)
}

// M4: there was no cap on the number of variables or their total size. The audit
// stored 300 x 60 KB (18 MB) in one project: far past what execve or Docker accept.
func TestEnvVarCountIsCapped(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	pid := a.newProject(key, "web")

	for i := range 200 {
		a.want(200, "PUT", fmt.Sprintf("/v1/projects/%s/env/V%03d", pid, i), key, `{"value":"x"}`)
	}
	r := a.want(400, "PUT", "/v1/projects/"+pid+"/env/ONE_MORE", key, `{"value":"x"}`)
	if !strings.Contains(r.body, "200") {
		t.Errorf("the refusal should name the limit: %s", r.body)
	}
	// Replacing an existing variable at the cap is still fine.
	a.want(200, "PUT", "/v1/projects/"+pid+"/env/V000", key, `{"value":"y"}`)
}

func TestEnvVarTotalSizeIsCapped(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	pid := a.newProject(key, "web")
	body := fmt.Sprintf(`{"value":%q}`, strings.Repeat("v", 60_000))

	for i := range 8 { // 8 x 60 KB = 480 KB, under the 512 KiB total
		a.want(200, "PUT", fmt.Sprintf("/v1/projects/%s/env/BIG%d", pid, i), key, body)
	}
	a.want(400, "PUT", "/v1/projects/"+pid+"/env/BIG8", key, body)
	a.want(200, "PUT", "/v1/projects/"+pid+"/env/BIG0", key, body) // same size, replaced
	a.want(200, "PUT", "/v1/projects/"+pid+"/env/SMALL", key, `{"value":"x"}`)
}

// L4: uuid.Parse accepts uppercase, braces, urn:uuid: and no hyphens, so one project
// had five URLs. Anything keyed on the path (a rate limit, a cache) could be dodged.
func TestOnlyCanonicalIDsResolve(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")
	pid := a.newProject(key, "web")

	a.want(200, "GET", "/v1/projects/"+pid, key, "")
	for _, alias := range []string{
		strings.ToUpper(pid), "{" + pid + "}", "urn:uuid:" + pid, strings.ReplaceAll(pid, "-", ""),
	} {
		a.want(404, "GET", "/v1/projects/"+alias, key, "")
	}
}

// L5: the unknown-field error quoted the field name back in full, so a 900 KB field
// name produced a 900 KB error.
func TestUnknownFieldErrorIsShort(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")

	field := strings.Repeat("f", 100_000)
	r := a.want(400, "POST", "/v1/projects", key, `{"`+field+`":1}`)
	if len(r.body) > 1024 {
		t.Fatalf("error body is %d bytes for an unknown field", len(r.body))
	}
	r = a.want(400, "POST", "/v1/projects", key, `{"tenant_id":"x"}`)
	if !strings.Contains(r.body, "tenant_id") {
		t.Fatalf("a short unknown field should still be named: %s", r.body)
	}
}

// L6: only the first Authorization header was read. If a proxy in front picks a
// different one, the two layers would disagree about who is calling.
func TestMoreThanOneAuthorizationHeaderIsRefused(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")

	for _, pair := range [][2]string{{key, key}, {key, "fo_bad"}, {"fo_bad", key}} {
		req := httptest.NewRequest("GET", "/v1/tenant", nil)
		req.Header.Add("Authorization", "Bearer "+pair[0])
		req.Header.Add("Authorization", "Bearer "+pair[1])
		rec := httptest.NewRecorder()
		a.router.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("two Authorization headers = %d, want 401", rec.Code)
		}
	}
}

// L7: names shown to people accepted zero-width and bidi-override characters, so one
// key could be made to look like another in a list.
func TestLabelsRefuseInvisibleCharacters(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")

	zwsp, rlo, bom, lri := string(rune(0x200B)), string(rune(0x202E)), string(rune(0xFEFF)), string(rune(0x2066))
	for _, name := range []string{"ci" + zwsp + "key", rlo + "deploy", "a" + bom + "b", "x" + lri + "y"} {
		r := a.do("POST", "/v1/api-keys", key, fmt.Sprintf(`{"name":%q}`, name))
		if r.status != 400 {
			t.Errorf("key name %q = %d, want 400", name, r.status)
		}
	}
	a.want(201, "POST", "/v1/api-keys", key, `{"name":"déploiement CI"}`)
}

// L3: repo_url accepted loopback, link-local and IP-literal hosts and bidi characters;
// branch accepted names git refuses. The DNS check stays Phase 3's job (a name can
// resolve anywhere), but a URL that is visibly internal is refused up front.
func TestRepoURLAndBranchAreStrict(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	key, _ := a.newTenant("acme")

	n := 0
	create := func(url, branch string) result { // a fresh name each time, so 409 can't mask 201
		n++
		return a.do("POST", "/v1/projects", key, fmt.Sprintf(`{"name":"p%d","repo_url":%q,"branch":%q}`, n, url, branch))
	}
	for _, u := range []string{
		"https://localhost/x", "https://api.localhost/x", "https://127.0.0.1/x", "https://[::1]/x",
		"https://169.254.169.254/latest", "https://0x7f.1/x", "https://10.0.0.1/x", "https://gitlab/x",
		"https://github.com./a/b", "https://a.com/x" + string(rune(0x202E)) + "y", "https://a.com/x" + string(rune(0x85)) + "y", "https://a.com/%00",
		"https://a.com/x#",
	} {
		if r := create(u, "main"); r.status != 400 {
			t.Errorf("repo_url %q = %d, want 400", u, r.status)
		}
	}
	for _, b := range []string{"HEAD", "feature/.x", "a/./b", "a.lock/b", "a@{1}", "a\\b", "x/HEAD/"} {
		if r := create("https://github.com/acme/web", b); r.status != 400 {
			t.Errorf("branch %q = %d, want 400", b, r.status)
		}
	}
	for i, c := range [][2]string{
		{"https://github.com/acme/web", "feature/new-ui"},
		{"https://gitlab.example.com:8443/a/b.git", "release-1.2"},
		{"https://GitHub.com/acme/web", "v1.0"},
	} {
		r := a.do("POST", "/v1/projects", key, fmt.Sprintf(`{"name":"ok%d","repo_url":%q,"branch":%q}`, i, c[0], c[1]))
		if r.status != 201 {
			t.Errorf("%v = %d, want 201: %s", c, r.status, r.body)
		}
	}
}
