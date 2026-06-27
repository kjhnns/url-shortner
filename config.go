package main

import (
	"fmt"
	"os"
	"strings"
)

// Config holds all runtime configuration, sourced entirely from the environment
// with sane defaults so the service is easy to bootstrap.
type Config struct {
	Port         string
	DatabasePath string
	BaseURL      string

	AuthMode string // currently only "password"

	// password mode: the server holds ONLY the lowercase-hex SHA-256 of the app
	// password (APP_PASSWORD_HASH), never the plaintext.
	//   - Web login: the browser POSTs the plaintext (inside TLS); the server
	//     SHA-256s it and constant-time compares to AppPasswordHash.
	//   - JSON API: the client sends Authorization: Bearer <sha256-hex-of-password>
	//     so the raw password never rides the API; the server compares directly.
	// One secret (the password) gates both surfaces, but it is never at rest or on
	// the API wire in plaintext.
	AppPasswordHash string
	SessionSecret   []byte
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// LoadConfig reads configuration from the environment and validates it. It
// refuses to start in an insecure or misconfigured state rather than running
// wide open.
func LoadConfig() (*Config, error) {
	c := &Config{
		Port:            getenv("PORT", "8080"),
		DatabasePath:    getenv("DATABASE_PATH", "./shortener.db"),
		BaseURL:         getenv("BASE_URL", ""),
		AuthMode:        getenv("AUTH_MODE", "password"),
		AppPasswordHash: strings.ToLower(strings.TrimSpace(os.Getenv("APP_PASSWORD_HASH"))),
	}

	// Session secret backs the HMAC-signed cookie. Generate an ephemeral one if
	// unset (sessions won't survive a restart; set SESSION_SECRET to persist).
	if secret := os.Getenv("SESSION_SECRET"); secret != "" {
		c.SessionSecret = []byte(secret)
	} else {
		c.SessionSecret = randomBytes(32)
	}

	// Only password mode ships today. The auth layer is a single middleware so
	// a Google OAuth (or Cloudflare Access) mode can be slotted in later.
	switch c.AuthMode {
	case "password":
		if c.AppPasswordHash == "" {
			return nil, fmt.Errorf("AUTH_MODE=password but APP_PASSWORD_HASH is unset: refusing to start wide open (set APP_PASSWORD_HASH to the lowercase hex sha256 of the app password)")
		}
	default:
		return nil, fmt.Errorf("unknown AUTH_MODE %q (only \"password\" is supported in this version)", c.AuthMode)
	}

	return c, nil
}
