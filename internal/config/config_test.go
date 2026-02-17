package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_FileWinsOverEnv(t *testing.T) {
	tempDir := t.TempDir()
	chdir(t, tempDir)

	configJSON := `{
		"api_listen_addr": "127.0.0.1:9999",
		"service_password": "from-file-password",
		"public_ip": "198.51.100.10",
		"internal_ip": "10.10.0.5",
		"rtp_port_min": 21000,
		"rtp_port_max": 22000,
		"peer_learning_window_sec": 17,
		"max_frame_wait_ms": 240,
		"idle_timeout_sec": 70,
		"video_inject_cached_sps_pps": true,
		"stats_log_interval_sec": 8,
		"packet_log": true,
		"packet_log_sample_n": 13,
		"packet_log_on_anomaly": false
	}`
	if err := os.WriteFile(filepath.Join(tempDir, FileName), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	setAllEnv(t, map[string]string{
		"API_LISTEN_ADDR":             "0.0.0.0:8081",
		"SERVICE_PASSWORD":            "from-env-password",
		"PUBLIC_IP":                   "203.0.113.50",
		"INTERNAL_IP":                 "10.0.0.1",
		"RTP_PORT_MIN":                "30000",
		"RTP_PORT_MAX":                "40000",
		"PEER_LEARNING_WINDOW_SEC":    "10",
		"MAX_FRAME_WAIT_MS":           "120",
		"IDLE_TIMEOUT_SEC":            "60",
		"VIDEO_INJECT_CACHED_SPS_PPS": "false",
		"STATS_LOG_INTERVAL_SEC":      "5",
		"PACKET_LOG":                  "false",
		"PACKET_LOG_SAMPLE_N":         "0",
		"PACKET_LOG_ON_ANOMALY":       "true",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.APIListenAddr != "127.0.0.1:9999" ||
		cfg.ServicePassword != "from-file-password" ||
		cfg.PublicIP != "198.51.100.10" ||
		cfg.InternalIP != "10.10.0.5" ||
		cfg.RTPPortMin != 21000 ||
		cfg.RTPPortMax != 22000 ||
		cfg.PeerLearningWindowSec != 17 ||
		cfg.MaxFrameWaitMS != 240 ||
		cfg.IdleTimeoutSec != 70 ||
		!cfg.VideoInjectCachedSPSPPS ||
		cfg.StatsLogIntervalSec != 8 ||
		!cfg.PacketLog ||
		cfg.PacketLogSampleN != 13 ||
		cfg.PacketLogOnAnomaly {
		t.Fatalf("expected file config values, got %+v", cfg)
	}
}

func TestLoad_EnvFallbackWhenFileAbsent(t *testing.T) {
	tempDir := t.TempDir()
	chdir(t, tempDir)

	setAllEnv(t, map[string]string{
		"API_LISTEN_ADDR":             "0.0.0.0:7070",
		"SERVICE_PASSWORD":            "env-password",
		"PUBLIC_IP":                   "203.0.113.42",
		"INTERNAL_IP":                 "10.20.30.40",
		"RTP_PORT_MIN":                "31000",
		"RTP_PORT_MAX":                "32000",
		"PEER_LEARNING_WINDOW_SEC":    "12",
		"MAX_FRAME_WAIT_MS":           "180",
		"IDLE_TIMEOUT_SEC":            "65",
		"VIDEO_INJECT_CACHED_SPS_PPS": "true",
		"STATS_LOG_INTERVAL_SEC":      "9",
		"PACKET_LOG":                  "true",
		"PACKET_LOG_SAMPLE_N":         "4",
		"PACKET_LOG_ON_ANOMALY":       "false",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.APIListenAddr != "0.0.0.0:7070" ||
		cfg.ServicePassword != "env-password" ||
		cfg.PublicIP != "203.0.113.42" ||
		cfg.InternalIP != "10.20.30.40" ||
		cfg.RTPPortMin != 31000 ||
		cfg.RTPPortMax != 32000 ||
		cfg.PeerLearningWindowSec != 12 ||
		cfg.MaxFrameWaitMS != 180 ||
		cfg.IdleTimeoutSec != 65 ||
		!cfg.VideoInjectCachedSPSPPS ||
		cfg.StatsLogIntervalSec != 9 ||
		!cfg.PacketLog ||
		cfg.PacketLogSampleN != 4 ||
		cfg.PacketLogOnAnomaly {
		t.Fatalf("expected env config values, got %+v", cfg)
	}
}

func TestLoad_InvalidFileReturnsError(t *testing.T) {
	tempDir := t.TempDir()
	chdir(t, tempDir)

	if err := os.WriteFile(filepath.Join(tempDir, FileName), []byte("{broken json"), 0o644); err != nil {
		t.Fatalf("write invalid config file: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid config file")
	}
	if !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})
}

func setAllEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		t.Setenv(key, value)
	}
}
