package logging

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

var (
	logger *slog.Logger
	once   sync.Once
)

// L returns the configured slog logger.
func L() *slog.Logger {
	once.Do(initLogger)
	return logger
}

// WithSessionID attaches the session_id field to all log entries.
func WithSessionID(sessionID string) *slog.Logger {
	return L().With("session_id", sessionID)
}

func initLogger() {
	handlerOptions := &slog.HandlerOptions{
		Level: parseLevel(os.Getenv("LOG_LEVEL")),
	}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT"))) {
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
