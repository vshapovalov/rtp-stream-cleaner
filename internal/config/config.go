package config

import (
	"os"
	"strconv"
)

type Config struct {
	APIListenAddr           string
	PublicIP                string
	InternalIP              string
	RTPPortMin              int
	RTPPortMax              int
	PeerLearningWindowSec   int
	MaxFrameWaitMS          int
	IdleTimeoutSec          int
	VideoInjectCachedSPSPPS bool
	StatsLogIntervalSec     int
	PacketLog               bool
	PacketLogSampleN        int
	PacketLogOnAnomaly      bool
}

func Load() Config {
	packetLog := getEnvBool("PACKET_LOG", false)
	return Config{
		APIListenAddr:           getEnv("API_LISTEN_ADDR", "0.0.0.0:8080"),
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
