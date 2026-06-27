package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// apiLink is the JSON representation of a Link returned by the API.
type apiLink struct {
	Slug      string `json:"slug"`
	Target    string `json:"target"`
	Clicks    int64  `json:"clicks"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toAPILink(l *Link) apiLink {
	return apiLink{
		Slug:      l.Slug,
		Target:    l.TargetURL,
		Clicks:    l.Clicks,
		CreatedAt: l.CreatedAt,
		UpdatedAt: l.UpdatedAt,
	}
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError writes a simple {"error":"..."} envelope.
func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// apiAuth gates every /api/* request behind a Bearer token whose value is the
// shared app password (Authorization: Bearer <APP_PASSWORD>), compared in
// constant time. There is no separate API token: one secret gates both the web
// UI (session cookie) and the API. APP_PASSWORD is required for the app to start,
// so the API is always enabled. Returns true if the request may proceed;
// otherwise it has already written the response.
func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) ||
		subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, prefix)), []byte(s.cfg.AppPassword)) != 1 {
		writeAPIError(w, http.StatusUnauthorized, "missing or invalid bearer token")
		return false
	}
	return true
}

// handleAPILinks routes /api/links (collection: list + create).
func (s *Server) handleAPILinks(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.apiList(w, r)
	case http.MethodPost:
		s.apiCreate(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAPILink routes /api/links/{slug} (item: get + update + delete).
func (s *Server) handleAPILink(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	slug := strings.TrimPrefix(r.URL.Path, "/api/links/")
	if slug == "" || strings.Contains(slug, "/") {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.apiGet(w, r, slug)
	case http.MethodPut:
		s.apiUpdate(w, r, slug)
	case http.MethodDelete:
		s.apiDelete(w, r, slug)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) apiList(w http.ResponseWriter, _ *http.Request) {
	links, err := s.store.ListLinks()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	out := make([]apiLink, 0, len(links))
	for i := range links {
		out = append(out, toAPILink(&links[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug   string `json:"slug"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Slug = strings.TrimSpace(body.Slug)
	body.Target = strings.TrimSpace(body.Target)
	if !validSlug(body.Slug) {
		writeAPIError(w, http.StatusBadRequest, "invalid slug (^[A-Za-z0-9_-]{1,64}$, not a reserved path)")
		return
	}
	if !validTargetURL(body.Target) {
		writeAPIError(w, http.StatusBadRequest, "invalid target (must be an absolute http(s) URL)")
		return
	}
	existing, err := s.store.GetLink(body.Slug)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	if existing != nil {
		writeAPIError(w, http.StatusConflict, "slug already exists")
		return
	}
	if err := s.store.CreateLink(body.Slug, body.Target, "api"); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	link, err := s.store.GetLink(body.Slug)
	if err != nil || link == nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusCreated, toAPILink(link))
}

func (s *Server) apiGet(w http.ResponseWriter, _ *http.Request, slug string) {
	link, err := s.store.GetLink(slug)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	if link == nil {
		writeAPIError(w, http.StatusNotFound, "slug not found")
		return
	}
	writeJSON(w, http.StatusOK, toAPILink(link))
}

func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request, slug string) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Target = strings.TrimSpace(body.Target)
	if !validTargetURL(body.Target) {
		writeAPIError(w, http.StatusBadRequest, "invalid target (must be an absolute http(s) URL)")
		return
	}
	ok, err := s.store.UpdateTarget(slug, body.Target)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "slug not found")
		return
	}
	link, err := s.store.GetLink(slug)
	if err != nil || link == nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, toAPILink(link))
}

// handleAPIRootRedirect exposes the configurable root redirect over the API:
// GET returns {"target":"..."} ("" if unset); PUT {"target":"..."} sets it
// (empty target clears it). Bearer auth like the rest of /api.
func (s *Server) handleAPIRootRedirect(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		target, err := s.store.GetSetting(settingRootRedirect)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "db error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"target": target})
	case http.MethodPut:
		var body struct {
			Target string `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		body.Target = strings.TrimSpace(body.Target)
		if body.Target != "" && !validTargetURL(body.Target) {
			writeAPIError(w, http.StatusBadRequest, "invalid target (must be an absolute http(s) URL or empty to clear)")
			return
		}
		if err := s.store.SetSetting(settingRootRedirect, body.Target); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "db error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"target": body.Target})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) apiDelete(w http.ResponseWriter, _ *http.Request, slug string) {
	ok, err := s.store.DeleteLink(slug)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "slug not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "slug": slug})
}
