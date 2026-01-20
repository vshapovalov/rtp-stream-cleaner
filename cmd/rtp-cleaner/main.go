package main

import (
	"log"
	"net/http"
	"time"

	"rtp-stream-cleaner/internal/api"
	"rtp-stream-cleaner/internal/config"
	"rtp-stream-cleaner/internal/session"
)

func main() {
	cfg := config.Load()

	if cfg.PublicIP != "" {
		log.Printf("public_ip=%s", cfg.PublicIP)
	}
	if cfg.InternalIP != "" {
		log.Printf("internal_ip=%s", cfg.InternalIP)
	}

	allocator, err := session.NewPortAllocator(cfg.RTPPortMin, cfg.RTPPortMax)
	if err != nil {
		log.Fatalf("failed to init port allocator: %v", err)
	}
	manager := session.NewManager(allocator, time.Duration(cfg.PeerLearningWindowSec)*time.Second)
	handler := api.NewHandler(cfg, manager)

	mux := http.NewServeMux()
	handler.Register(mux)

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
