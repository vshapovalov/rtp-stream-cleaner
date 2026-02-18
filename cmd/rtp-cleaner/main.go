package main

import (
	"net/http"
	"os"
	"time"

	"rtp-stream-cleaner/internal/api"
	"rtp-stream-cleaner/internal/config"
	"rtp-stream-cleaner/internal/logging"
	"rtp-stream-cleaner/internal/session"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		logging.L().Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logging.Configure(logging.Config{Level: cfg.LogLevel, Format: cfg.LogFormat})
	logger := logging.L()

	if cfg.PublicIP != "" {
		logger.Info("public_ip configured", "public_ip", cfg.PublicIP)
	}
	if cfg.InternalIP != "" {
		logger.Info("internal_ip configured", "internal_ip", cfg.InternalIP)
	}
	if cfg.ServicePassword == "" {
		logger.Warn("service_password is empty; API access is denied until it is configured")
	}

	allocator, err := session.NewPortAllocator(cfg.RTPPortMin, cfg.RTPPortMax)
	if err != nil {
		logger.Error("failed to init port allocator", "error", err)
		os.Exit(1)
	}
	manager := session.NewManager(
		allocator,
		time.Duration(cfg.PeerLearningWindowSec)*time.Second,
		time.Duration(cfg.MaxFrameWaitMS)*time.Millisecond,
		time.Duration(cfg.IdleTimeoutSec)*time.Second,
		cfg.VideoInjectCachedSPSPPS,
		session.ProxyLogConfig{
			StatsInterval:      time.Duration(cfg.StatsLogIntervalSec) * time.Second,
			PacketLog:          cfg.PacketLog,
			PacketLogSampleN:   uint64(cfg.PacketLogSampleN),
			PacketLogOnAnomaly: cfg.PacketLogOnAnomaly,
		},
	)
	handler := api.NewHandler(cfg, manager)

	mux := http.NewServeMux()
	handler.Register(mux)

	server := &http.Server{
		Addr:              cfg.APIListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("starting http server", "addr", cfg.APIListenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
