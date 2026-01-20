package config

import "os"

type Config struct {
	APIListenAddr string
	PublicIP      string
	InternalIP    string
}

func Load() Config {
	return Config{
		APIListenAddr: getEnv("API_LISTEN_ADDR", "0.0.0.0:8080"),
		PublicIP:      os.Getenv("PUBLIC_IP"),
		InternalIP:    os.Getenv("INTERNAL_IP"),
	}
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
