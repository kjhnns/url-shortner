package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "session"
	sessionTTL    = 7 * 24 * time.Hour
)

// randomBytes returns n cryptographically-random bytes (panics only if the OS
// RNG fails, which is unrecoverable).
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return b
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Auth is the access-control layer. Today it implements a single shared
// password with an HMAC-signed session cookie, exposed through one Middleware so
// a richer mode (Google OAuth + allowlist, Cloudflare Access) can be added later
// without restructuring the routes.
type Auth struct {
	cfg *Config
}

func NewAuth(cfg *Config) *Auth { return &Auth{cfg: cfg} }

// session is the data carried in the signed cookie. email is empty in password
// mode (no per-user identity) but is kept so identity-bearing modes can fill it.
type session struct {
	email string
	exp   int64
}

// sign returns the HMAC-SHA256 of msg under the server secret.
func (a *Auth) sign(msg string) string {
	mac := hmac.New(sha256.New, a.cfg.SessionSecret)
	mac.Write([]byte(msg))
	return b64(mac.Sum(nil))
}

// makeSessionValue builds a tamper-proof cookie value: "<payload>.<hmac>" where
// payload is base64("<email>|<exp>").
func (a *Auth) makeSessionValue(email string) string {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := b64([]byte(fmt.Sprintf("%s|%d", email, exp)))
	return payload + "." + a.sign(payload)
}

// parseSession validates the cookie's signature and expiry.
func (a *Auth) parseSession(value string) (*session, bool) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	payload, sig := parts[0], parts[1]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.sign(payload))) != 1 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	fields := strings.SplitN(string(raw), "|", 2)
	if len(fields) != 2 {
		return nil, false
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return nil, false
	}
	return &session{email: fields[0], exp: exp}, true
}

// currentUser returns the requesting user's session, or (nil, false).
func (a *Auth) currentUser(r *http.Request) (*session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	return a.parseSession(c.Value)
}

func (a *Auth) setSession(w http.ResponseWriter, email string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.makeSessionValue(email),
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(a.cfg.BaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (a *Auth) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
}

// Middleware gates a handler, allowing only authenticated requests through and
// bouncing everyone else to the login page (remembering their destination).
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.currentUser(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

// HandleLogin renders the login form (GET) and verifies the shared password
// (POST), setting the session cookie on success.
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	if r.Method == http.MethodPost {
		if subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(a.cfg.AppPassword)) == 1 {
			a.setSession(w, "")
			http.Redirect(w, r, next, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		renderLogin(w, next, "Wrong password.")
		return
	}
	renderLogin(w, next, "")
}

func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	a.clearSession(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// safeNext guards against open-redirects: only same-origin absolute paths are
// honored, everything else falls back to /admin.
func safeNext(next string) string {
	if next == "" {
		return "/admin"
	}
	u, err := url.QueryUnescape(next)
	if err != nil {
		return "/admin"
	}
	if !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") {
		return "/admin"
	}
	return u
}
