package main

import (
	"io"
	"log"
	"net/http"
	"time"

	"rtp-stream-cleaner/internal/config"
)

func main() {
	cfg := config.Load()

	if cfg.PublicIP != "" {
		log.Printf("public_ip=%s", cfg.PublicIP)
	}
	if cfg.InternalIP != "" {
		log.Printf("internal_ip=%s", cfg.InternalIP)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})

	server := &http.Server{
		Addr:              cfg.APIListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("starting http server on %s", cfg.APIListenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}
