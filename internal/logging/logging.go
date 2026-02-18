package logging

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

var (
	logger *slog.Logger
	mu     sync.RWMutex
)

type Config struct {
	Level  string
	Format string
}

// L returns the configured slog logger.
func L() *slog.Logger {
	mu.RLock()
	current := logger
	mu.RUnlock()
	if current != nil {
		return current
	}

	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		initLoggerLocked(Config{Level: os.Getenv("LOG_LEVEL"), Format: os.Getenv("LOG_FORMAT")})
	}
	return logger
}

func Configure(cfg Config) {
	mu.Lock()
	defer mu.Unlock()
	initLoggerLocked(cfg)
}

// WithSessionID attaches the session_id field to all log entries.
func WithSessionID(sessionID string) *slog.Logger {
	return L().With("session_id", sessionID)
}

func initLoggerLocked(cfg Config) {
	handlerOptions := &slog.HandlerOptions{
		Level: parseLevel(cfg.Level),
	}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, handlerOptions)
	default:
		handler = slog.NewJSONHandler(os.Stdout, handlerOptions)
	}
	logger = slog.New(handler)
	slog.SetDefault(logger)
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
