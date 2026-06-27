package main

import (
	"embed"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// reservedPaths are top-level prefixes that must never be interpreted as slugs.
var reservedPaths = map[string]bool{
	"admin": true, "auth": true, "static": true, "healthz": true, "": true,
}

var slugRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// validSlug reports whether s is an acceptable slug and not a reserved path.
func validSlug(s string) bool {
	if reservedPaths[s] {
		return false
	}
	return slugRe.MatchString(s)
}

// validTargetURL reports whether s is a well-formed absolute http(s) URL.
func validTargetURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

// Server bundles dependencies for the HTTP handlers.
type Server struct {
	cfg   *Config
	store *Store
	auth  *Auth
}

func NewServer(cfg *Config, store *Store, auth *Auth) *Server {
	return &Server{cfg: cfg, store: store, auth: auth}
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	renderPage(w, name, data)
}

// Routes builds the http.Handler with all routes wired and auth applied to the
// protected ones. A single middleware (s.auth.Middleware) gates everything that
// is not a public redirect, login, health check, or static asset.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))

	// Auth endpoints (public by nature).
	mux.HandleFunc("/auth/login", s.auth.HandleLogin)
	mux.HandleFunc("/auth/logout", s.auth.HandleLogout)

	// Admin (all protected).
	mux.Handle("/admin", s.auth.Middleware(http.HandlerFunc(s.handleAdmin)))
	mux.Handle("/admin/update", s.auth.Middleware(http.HandlerFunc(s.handleAdminUpdate)))
	mux.Handle("/admin/delete", s.auth.Middleware(http.HandlerFunc(s.handleAdminDelete)))

	// Root + slug catch-all.
	mux.HandleFunc("/", s.handleRoot)

	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleRoot serves the landing page for "/" and dispatches everything else to
// the slug handler.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		s.render(w, "landing.html", map[string]any{"BaseURL": s.cfg.BaseURL})
		return
	}
	s.handleSlug(w, r, path)
}

// handleSlug implements claim-on-visit: a known slug redirects (public); an
// unknown slug shows the protected claim page; POST claims the slug (protected).
func (s *Server) handleSlug(w http.ResponseWriter, r *http.Request, slug string) {
	if !validSlug(slug) {
		http.NotFound(w, r)
		return
	}

	link, err := s.store.GetLink(slug)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	// Known slug: public 302 redirect (any method falls through to GET here).
	if link != nil && r.Method == http.MethodGet {
		go s.store.RecordVisit(slug) // fire-and-forget; redirect must not block
		http.Redirect(w, r, link.TargetURL, http.StatusFound)
		return
	}

	// From here on the slug is unknown (or it's a POST to claim): protected.
	sess, ok := s.auth.currentUser(r)
	if !ok {
		dest := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/auth/login?next="+dest, http.StatusFound)
		return
	}

	if r.Method == http.MethodPost {
		s.claim(w, r, slug, sess)
		return
	}

	// GET on an unknown slug while authed: show the claim page.
	s.render(w, "claim.html", map[string]any{
		"Slug":    slug,
		"BaseURL": s.cfg.BaseURL,
	})
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request, slug string, sess *session) {
	target := strings.TrimSpace(r.FormValue("target_url"))
	if !validTargetURL(target) {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "claim.html", map[string]any{
			"Slug": slug, "BaseURL": s.cfg.BaseURL,
			"Error": "Enter a valid http(s) URL.", "Target": target,
		})
		return
	}
	createdBy := sess.email
	if createdBy == "" {
		createdBy = "password-user"
	}
	if err := s.store.CreateLink(slug, target, createdBy); err != nil {
		w.WriteHeader(http.StatusConflict)
		s.render(w, "claim.html", map[string]any{
			"Slug": slug, "BaseURL": s.cfg.BaseURL,
			"Error": "Could not save (slug may already be taken).", "Target": target,
		})
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.ListLinks()
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	s.render(w, "admin.html", map[string]any{
		"Links":   links,
		"BaseURL": s.cfg.BaseURL,
	})
}

func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := r.FormValue("slug")
	target := strings.TrimSpace(r.FormValue("target_url"))
	if !validSlug(slug) || !validTargetURL(target) {
		http.Error(w, "invalid slug or url", http.StatusBadRequest)
		return
	}
	if _, err := s.store.UpdateTarget(slug, target); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := r.FormValue("slug")
	if !validSlug(slug) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}
	if _, err := s.store.DeleteLink(slug); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}
