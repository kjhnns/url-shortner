package main

import (
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
