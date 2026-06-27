// Command url-shortner is a small, vanilla Go + SQLite URL shortener with
// claim-on-visit slugs and a mobile-friendly admin dashboard.
package main

import (
	"log"
	"net/http"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := OpenStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	auth := NewAuth(cfg, store)
	srv := NewServer(cfg, store, auth)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("url-shortner listening on :%s (auth=%s, db=%s)", cfg.Port, cfg.AuthMode, cfg.DatabasePath)
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
