package main

import (
	"fmt"
	"os"
)

// Config holds all runtime configuration, sourced entirely from the environment
// with sane defaults so the service is easy to bootstrap.
type Config struct {
	Port         string
	DatabasePath string
	BaseURL      string

	AuthMode string // currently only "password"

	// password mode: shared password + HMAC-signed session cookie. The same
	// AppPassword also authenticates the JSON API, sent as a Bearer token
	// (Authorization: Bearer <APP_PASSWORD>). One secret gates both surfaces.
	AppPassword   string
	SessionSecret []byte
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
		Port:         getenv("PORT", "8080"),
		DatabasePath: getenv("DATABASE_PATH", "./shortener.db"),
		BaseURL:      getenv("BASE_URL", ""),
		AuthMode:    getenv("AUTH_MODE", "password"),
		AppPassword: os.Getenv("APP_PASSWORD"),
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
		if c.AppPassword == "" {
			return nil, fmt.Errorf("AUTH_MODE=password but APP_PASSWORD is unset: refusing to start wide open (set APP_PASSWORD)")
		}
	default:
		return nil, fmt.Errorf("unknown AUTH_MODE %q (only \"password\" is supported in this version)", c.AuthMode)
	}

	return c, nil
}
