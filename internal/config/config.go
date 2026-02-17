package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"rtp-stream-cleaner/internal/logging"
)

const FileName = "config.json"

type Config struct {
	APIListenAddr           string `json:"api_listen_addr"`
	ServicePassword         string `json:"service_password"`
	PublicIP                string `json:"public_ip"`
	InternalIP              string `json:"internal_ip"`
	RTPPortMin              int    `json:"rtp_port_min"`
	RTPPortMax              int    `json:"rtp_port_max"`
	PeerLearningWindowSec   int    `json:"peer_learning_window_sec"`
	MaxFrameWaitMS          int    `json:"max_frame_wait_ms"`
	IdleTimeoutSec          int    `json:"idle_timeout_sec"`
	VideoInjectCachedSPSPPS bool   `json:"video_inject_cached_sps_pps"`
	StatsLogIntervalSec     int    `json:"stats_log_interval_sec"`
	PacketLog               bool   `json:"packet_log"`
	PacketLogSampleN        int    `json:"packet_log_sample_n"`
	PacketLogOnAnomaly      bool   `json:"packet_log_on_anomaly"`
}

func Load() (Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("resolve current working directory: %w", err)
	}

	path := filepath.Join(cwd, FileName)
	if _, err := os.Stat(path); err == nil {
		cfg, err := loadFromFile(path)
		if err != nil {
			return Config{}, err
		}
		logging.L().Info("loaded config", "source", "file", "path", path)
		return cfg, nil
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("stat config file %s: %w", path, err)
	}

	logging.L().Info("loaded config", "source", "env")
	return loadFromEnv(), nil
}

func loadFromFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config file %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	return cfg, nil
}

func loadFromEnv() Config {
	packetLog := getEnvBool("PACKET_LOG", false)
	return Config{
		APIListenAddr:           getEnv("API_LISTEN_ADDR", "0.0.0.0:8080"),
		ServicePassword:         os.Getenv("SERVICE_PASSWORD"),
		PublicIP:                os.Getenv("PUBLIC_IP"),
		InternalIP:              os.Getenv("INTERNAL_IP"),
		RTPPortMin:              getEnvInt("RTP_PORT_MIN", 30000),
		RTPPortMax:              getEnvInt("RTP_PORT_MAX", 40000),
		PeerLearningWindowSec:   getEnvInt("PEER_LEARNING_WINDOW_SEC", 10),
		MaxFrameWaitMS:          getEnvInt("MAX_FRAME_WAIT_MS", 120),
		IdleTimeoutSec:          getEnvInt("IDLE_TIMEOUT_SEC", 60),
		VideoInjectCachedSPSPPS: getEnvBool("VIDEO_INJECT_CACHED_SPS_PPS", false),
		StatsLogIntervalSec:     getEnvInt("STATS_LOG_INTERVAL_SEC", 5),
		PacketLog:               packetLog,
		PacketLogSampleN:        getEnvInt("PACKET_LOG_SAMPLE_N", 0),
		PacketLogOnAnomaly:      getEnvBool("PACKET_LOG_ON_ANOMALY", packetLog),
	}
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
