package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer spins up a Server backed by a fresh temp SQLite DB in password
// mode with a known password and a fixed session secret.
func newTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := &Config{
		Port:          "0",
		DatabasePath:  dbPath,
		AuthMode:      "password",
		AppPassword:   "secret",
		SessionSecret: []byte("test-secret-do-not-use-in-prod"),
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	auth := NewAuth(cfg)
	srv := NewServer(cfg, store, auth)
	return srv, srv.Routes()
}

// authedCookie performs a real password login and returns the session cookie.
func authedCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	form := url.Values{"password": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("login did not set session cookie (status %d)", rec.Code)
	return nil
}

func do(h http.Handler, method, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	_, h := newTestServer(t)
	rec := do(h, http.MethodGet, "/healthz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rec.Code)
	}
}

func TestProtectedRoutesRejectUnauthenticated(t *testing.T) {
	_, h := newTestServer(t)
	// /admin should bounce to login (302 -> /auth/login...).
	rec := do(h, http.MethodGet, "/admin", nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("/admin unauth = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/auth/login") {
		t.Fatalf("/admin unauth Location = %q, want /auth/login...", loc)
	}
	// An unknown slug while unauthenticated must also be protected (claim page).
	rec = do(h, http.MethodGet, "/newslug", nil, nil)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
		t.Fatalf("unknown slug unauth = %d loc=%q, want 302 -> /auth/login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestUnknownSlugShowsClaimPageWhenAuthed(t *testing.T) {
	_, h := newTestServer(t)
	c := authedCookie(t, h)
	rec := do(h, http.MethodGet, "/fresh", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed unknown slug = %d, want 200 (claim page)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Claim") {
		t.Fatalf("claim page body missing 'Claim'")
	}
}

func TestClaimRedirectUpdateDeleteLifecycle(t *testing.T) {
	_, h := newTestServer(t)
	c := authedCookie(t, h)

	// (b) claim a slug.
	rec := do(h, http.MethodPost, "/go", url.Values{"target_url": {"https://example.com/first"}}, c)
	if rec.Code != http.StatusFound {
		t.Fatalf("claim POST = %d, want 302", rec.Code)
	}

	// (c) redirect: GET /go -> 302 to target. Public (no cookie needed).
	rec = do(h, http.MethodGet, "/go", nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/first" {
		t.Fatalf("redirect Location = %q, want https://example.com/first", loc)
	}

	// (e) admin lists it.
	rec = do(h, http.MethodGet, "/admin", nil, c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/go") {
		t.Fatalf("admin missing the link (status %d)", rec.Code)
	}

	// (f) update changes the target.
	rec = do(h, http.MethodPost, "/admin/update", url.Values{"slug": {"go"}, "target_url": {"https://example.com/second"}}, c)
	if rec.Code != http.StatusFound {
		t.Fatalf("update = %d, want 302", rec.Code)
	}
	rec = do(h, http.MethodGet, "/go", nil, nil)
	if loc := rec.Header().Get("Location"); loc != "https://example.com/second" {
		t.Fatalf("post-update Location = %q, want .../second", loc)
	}

	// (g) delete removes it; subsequent GET shows claim page again (when authed).
	rec = do(h, http.MethodPost, "/admin/delete", url.Values{"slug": {"go"}}, c)
	if rec.Code != http.StatusFound {
		t.Fatalf("delete = %d, want 302", rec.Code)
	}
	rec = do(h, http.MethodGet, "/go", nil, c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Claim") {
		t.Fatalf("after delete, GET /go = %d, want claim page 200", rec.Code)
	}
}

func TestRejectInvalidTargetURL(t *testing.T) {
	_, h := newTestServer(t)
	c := authedCookie(t, h)
	rec := do(h, http.MethodPost, "/bad", url.Values{"target_url": {"not a url"}}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid url claim = %d, want 400", rec.Code)
	}
}

// apiReq issues a JSON request with an optional bearer token.
func apiReq(h http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// tok is the bearer token for the API: the shared app password (same secret the
// web UI uses), set to "secret" in newTestServer.
const tok = "secret"

func TestAPILinkCRUDLifecycle(t *testing.T) {
	_, h := newTestServer(t)

	// CREATE -> 201
	rec := apiReq(h, http.MethodPost, "/api/links",
		`{"slug":"go","target":"https://example.com/first"}`, tok)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created apiLink
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create body not JSON: %v", err)
	}
	if created.Slug != "go" || created.Target != "https://example.com/first" {
		t.Fatalf("create returned %+v", created)
	}

	// CREATE duplicate -> 409
	rec = apiReq(h, http.MethodPost, "/api/links",
		`{"slug":"go","target":"https://example.com/dup"}`, tok)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409", rec.Code)
	}

	// LIST -> 200, contains it
	rec = apiReq(h, http.MethodGet, "/api/links", "", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	var list []apiLink
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list body not JSON array: %v", err)
	}
	if len(list) != 1 || list[0].Slug != "go" {
		t.Fatalf("list = %+v, want one slug 'go'", list)
	}

	// GET -> 200
	rec = apiReq(h, http.MethodGet, "/api/links/go", "", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d, want 200", rec.Code)
	}

	// UPDATE -> 200, new target
	rec = apiReq(h, http.MethodPut, "/api/links/go",
		`{"target":"https://example.com/second"}`, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200", rec.Code)
	}
	var updated apiLink
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Target != "https://example.com/second" {
		t.Fatalf("update target = %q, want .../second", updated.Target)
	}

	// DELETE -> 200
	rec = apiReq(h, http.MethodDelete, "/api/links/go", "", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", rec.Code)
	}

	// GET after delete -> 404
	rec = apiReq(h, http.MethodGet, "/api/links/go", "", tok)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rec.Code)
	}
}

func TestAPIRejectsMissingToken(t *testing.T) {
	_, h := newTestServer(t)
	rec := apiReq(h, http.MethodGet, "/api/links", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token = %d, want 401", rec.Code)
	}
	rec = apiReq(h, http.MethodGet, "/api/links", "", "wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token = %d, want 401", rec.Code)
	}
}

func TestAPIGetMissingSlug404(t *testing.T) {
	_, h := newTestServer(t)
	rec := apiReq(h, http.MethodGet, "/api/links/nope", "", tok)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing slug = %d, want 404", rec.Code)
	}
	rec = apiReq(h, http.MethodPut, "/api/links/nope", `{"target":"https://x.com"}`, tok)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update missing = %d, want 404", rec.Code)
	}
	rec = apiReq(h, http.MethodDelete, "/api/links/nope", "", tok)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", rec.Code)
	}
}

func TestAPIValidationRejects(t *testing.T) {
	_, h := newTestServer(t)
	// bad slug
	rec := apiReq(h, http.MethodPost, "/api/links",
		`{"slug":"has space","target":"https://x.com"}`, tok)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad slug = %d, want 400", rec.Code)
	}
	// reserved slug
	rec = apiReq(h, http.MethodPost, "/api/links",
		`{"slug":"admin","target":"https://x.com"}`, tok)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved slug = %d, want 400", rec.Code)
	}
	// bad target
	rec = apiReq(h, http.MethodPost, "/api/links",
		`{"slug":"okslug","target":"not a url"}`, tok)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad target = %d, want 400", rec.Code)
	}
}

func TestRootRedirectConfigurable(t *testing.T) {
	srv, h := newTestServer(t)

	// Before: landing page (200).
	rec := do(h, http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("root before set = %d, want 200 (landing)", rec.Code)
	}

	// Set root_redirect.
	if err := srv.store.SetSetting(settingRootRedirect, "https://example.com/home"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	rec = do(h, http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("root after set = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/home" {
		t.Fatalf("root redirect Location = %q", loc)
	}

	// Clear it: back to landing (200).
	if err := srv.store.SetSetting(settingRootRedirect, ""); err != nil {
		t.Fatalf("clear setting: %v", err)
	}
	rec = do(h, http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("root after clear = %d, want 200 (landing)", rec.Code)
	}
}

func TestAPIRootRedirectEndpoint(t *testing.T) {
	_, h := newTestServer(t)
	// GET default -> "".
	rec := apiReq(h, http.MethodGet, "/api/settings/root_redirect", "", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("get root_redirect = %d, want 200", rec.Code)
	}
	var got map[string]string
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["target"] != "" {
		t.Fatalf("default root_redirect = %q, want empty", got["target"])
	}
	// PUT set.
	rec = apiReq(h, http.MethodPut, "/api/settings/root_redirect",
		`{"target":"https://example.com/h"}`, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("put root_redirect = %d, want 200", rec.Code)
	}
	rec = apiReq(h, http.MethodGet, "/api/settings/root_redirect", "", tok)
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["target"] != "https://example.com/h" {
		t.Fatalf("after put root_redirect = %q", got["target"])
	}
	// PUT invalid -> 400.
	rec = apiReq(h, http.MethodPut, "/api/settings/root_redirect", `{"target":"nope"}`, tok)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("put invalid root_redirect = %d, want 400", rec.Code)
	}
	// No token -> 401.
	rec = apiReq(h, http.MethodGet, "/api/settings/root_redirect", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("root_redirect no token = %d, want 401", rec.Code)
	}
}

func TestReservedAndInvalidSlugs(t *testing.T) {
	if validSlug("admin") || validSlug("auth") || validSlug("static") || validSlug("healthz") || validSlug("") {
		t.Fatal("reserved path accepted as slug")
	}
	if validSlug("has space") || validSlug("a/b") || validSlug(strings.Repeat("x", 65)) {
		t.Fatal("invalid slug accepted")
	}
	if !validSlug("ok_slug-1") {
		t.Fatal("valid slug rejected")
	}
}
