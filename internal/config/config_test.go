package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_FileWinsOverEnv(t *testing.T) {
	tempDir := t.TempDir()
	withExecutableDir(t, tempDir)

	configJSON := `{
		"api_listen_addr": "127.0.0.1:9999",
		"service_password": "from-file-password",
		"public_ip": "198.51.100.10",
		"public_ip_v6": "2001:db8::10",
		"internal_ip": "10.10.0.5",
		"rtp_port_min": 21000,
		"rtp_port_max": 22000,
		"peer_learning_min_packets": 7,
		"peer_relearn_idle_ms": 1500,
		"peer_learning_candidate_ttl_ms": 4500,
		"max_frame_wait_ms": 240,
		"idle_timeout_sec": 70,
		"video_inject_cached_sps_pps": true,
		"video_reorder_enabled": true,
		"video_reorder_max_packets": 12,
		"video_reorder_max_wait_ms": 15,
		"stats_log_interval_sec": 8,
		"packet_log": true,
		"packet_log_sample_n": 13,
		"packet_log_on_anomaly": false,
		"log_level": "debug",
		"log_format": "text"
	}`
	if err := os.WriteFile(filepath.Join(tempDir, FileName), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	setAllEnv(t, map[string]string{
		"API_LISTEN_ADDR":                "0.0.0.0:8081",
		"SERVICE_PASSWORD":               "from-env-password",
		"PUBLIC_IP":                      "203.0.113.50",
		"PUBLIC_IP_V6":                   "2001:db8::50",
		"INTERNAL_IP":                    "10.0.0.1",
		"RTP_PORT_MIN":                   "30000",
		"RTP_PORT_MAX":                   "40000",
		"PEER_LEARNING_MIN_PACKETS":      "5",
		"PEER_RELEARN_IDLE_MS":           "1000",
		"PEER_LEARNING_CANDIDATE_TTL_MS": "4000",
		"MAX_FRAME_WAIT_MS":              "120",
		"IDLE_TIMEOUT_SEC":               "60",
		"VIDEO_INJECT_CACHED_SPS_PPS":    "false",
		"VIDEO_REORDER_ENABLED":          "false",
		"VIDEO_REORDER_MAX_PACKETS":      "8",
		"VIDEO_REORDER_MAX_WAIT_MS":      "10",
		"STATS_LOG_INTERVAL_SEC":         "5",
		"PACKET_LOG":                     "false",
		"PACKET_LOG_SAMPLE_N":            "0",
		"PACKET_LOG_ON_ANOMALY":          "true",
		"LOG_LEVEL":                      "error",
		"LOG_FORMAT":                     "json",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.APIListenAddr != "127.0.0.1:9999" ||
		cfg.ServicePassword != "from-file-password" ||
		cfg.PublicIP != "198.51.100.10" ||
		cfg.PublicIPv6 != "2001:db8::10" ||
		cfg.InternalIP != "10.10.0.5" ||
		cfg.RTPPortMin != 21000 ||
		cfg.RTPPortMax != 22000 ||
		cfg.PeerLearningMinPackets != 7 ||
		cfg.PeerRelearnIdleMS != 1500 ||
		cfg.PeerLearningCandidateTTLMS != 4500 ||
		cfg.MaxFrameWaitMS != 240 ||
		cfg.IdleTimeoutSec != 70 ||
		!cfg.VideoInjectCachedSPSPPS ||
		!cfg.VideoReorderEnabled ||
		cfg.VideoReorderMaxPackets != 12 ||
		cfg.VideoReorderMaxWaitMS != 15 ||
		cfg.StatsLogIntervalSec != 8 ||
		!cfg.PacketLog ||
		cfg.PacketLogSampleN != 13 ||
		cfg.PacketLogOnAnomaly ||
		cfg.LogLevel != "debug" ||
		cfg.LogFormat != "text" {
		t.Fatalf("expected file config values, got %+v", cfg)
	}
}

func TestLoad_EnvFallbackWhenFileAbsent(t *testing.T) {
	tempDir := t.TempDir()
	withExecutableDir(t, tempDir)

	setAllEnv(t, map[string]string{
		"API_LISTEN_ADDR":                "0.0.0.0:7070",
		"SERVICE_PASSWORD":               "env-password",
		"PUBLIC_IP":                      "203.0.113.42",
		"PUBLIC_IP_V6":                   "2001:db8::42",
		"INTERNAL_IP":                    "10.20.30.40",
		"RTP_PORT_MIN":                   "31000",
		"RTP_PORT_MAX":                   "32000",
		"PEER_LEARNING_MIN_PACKETS":      "6",
		"PEER_RELEARN_IDLE_MS":           "1300",
		"PEER_LEARNING_CANDIDATE_TTL_MS": "4200",
		"MAX_FRAME_WAIT_MS":              "180",
		"IDLE_TIMEOUT_SEC":               "65",
		"VIDEO_INJECT_CACHED_SPS_PPS":    "true",
		"VIDEO_REORDER_ENABLED":          "true",
		"VIDEO_REORDER_MAX_PACKETS":      "6",
		"VIDEO_REORDER_MAX_WAIT_MS":      "14",
		"STATS_LOG_INTERVAL_SEC":         "9",
		"PACKET_LOG":                     "true",
		"PACKET_LOG_SAMPLE_N":            "4",
		"PACKET_LOG_ON_ANOMALY":          "false",
		"LOG_LEVEL":                      "warn",
		"LOG_FORMAT":                     "text",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.APIListenAddr != "0.0.0.0:7070" ||
		cfg.ServicePassword != "env-password" ||
		cfg.PublicIP != "203.0.113.42" ||
		cfg.PublicIPv6 != "2001:db8::42" ||
		cfg.InternalIP != "10.20.30.40" ||
		cfg.RTPPortMin != 31000 ||
		cfg.RTPPortMax != 32000 ||
		cfg.PeerLearningMinPackets != 6 ||
		cfg.PeerRelearnIdleMS != 1300 ||
		cfg.PeerLearningCandidateTTLMS != 4200 ||
		cfg.MaxFrameWaitMS != 180 ||
		cfg.IdleTimeoutSec != 65 ||
		!cfg.VideoInjectCachedSPSPPS ||
		!cfg.VideoReorderEnabled ||
		cfg.VideoReorderMaxPackets != 6 ||
		cfg.VideoReorderMaxWaitMS != 14 ||
		cfg.StatsLogIntervalSec != 9 ||
		!cfg.PacketLog ||
		cfg.PacketLogSampleN != 4 ||
		cfg.PacketLogOnAnomaly ||
		cfg.LogLevel != "warn" ||
		cfg.LogFormat != "text" {
		t.Fatalf("expected env config values, got %+v", cfg)
	}
}

func TestLoad_InvalidFileReturnsError(t *testing.T) {
	tempDir := t.TempDir()
	withExecutableDir(t, tempDir)

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

func TestLoad_UsesExecutableDirInsteadOfWorkingDir(t *testing.T) {
	execDir := t.TempDir()
	otherDir := t.TempDir()
	withExecutableDir(t, execDir)
	chdir(t, otherDir)

	execConfig := `{"api_listen_addr":"127.0.0.1:8181","log_level":"error","log_format":"text"}`
	if err := os.WriteFile(filepath.Join(execDir, FileName), []byte(execConfig), 0o644); err != nil {
		t.Fatalf("write config in executable dir: %v", err)
	}

	wdConfig := `{"api_listen_addr":"127.0.0.1:9191"}`
	if err := os.WriteFile(filepath.Join(otherDir, FileName), []byte(wdConfig), 0o644); err != nil {
		t.Fatalf("write config in working dir: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.APIListenAddr != "127.0.0.1:8181" {
		t.Fatalf("expected config from executable dir, got %+v", cfg)
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

func withExecutableDir(t *testing.T, dir string) {
	t.Helper()
	orig := resolveExecutableDir
	resolveExecutableDir = func() (string, error) {
		return dir, nil
	}
	t.Cleanup(func() {
		resolveExecutableDir = orig
	})
}

func setAllEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestLoad_DefaultRTPPortBindAttemptsWhenMissing(t *testing.T) {
	tempDir := t.TempDir()
	withExecutableDir(t, tempDir)
	if err := os.WriteFile(filepath.Join(tempDir, FileName), []byte(`{"rtp_port_min":30000,"rtp_port_max":30010}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RTPPortBindAttempts != DefaultRTPPortBindAttempts {
		t.Fatalf("got %d", cfg.RTPPortBindAttempts)
	}
}

func TestLoad_InvalidRTPPortBindAttempts(t *testing.T) {
	tempDir := t.TempDir()
	withExecutableDir(t, tempDir)
	if err := os.WriteFile(filepath.Join(tempDir, FileName), []byte(`{"rtp_port_bind_attempts":-1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}
