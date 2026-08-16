// Package logging builds the application's structured logger and provides
// category helpers so that every log line can be filtered by subsystem.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Category names the subsystem a log line belongs to.
type Category string

// Log categories required by the specification.
const (
	CategoryMarketData Category = "market_data"
	CategoryAnalysis   Category = "analysis"
	CategoryLLM        Category = "llm"
	CategoryRisk       Category = "risk"
	CategoryPosition   Category = "position"
	CategoryBacktest   Category = "backtest"
	CategoryScheduler  Category = "scheduler"
	CategoryAPI        Category = "api"
	CategoryDatabase   Category = "database"
	CategoryNews       Category = "news"
	CategorySystem     Category = "system"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

// New builds a slog logger for the given level and format.
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if strings.EqualFold(format, "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// For returns a logger tagged with a subsystem category.
func For(l *slog.Logger, c Category) *slog.Logger {
	return l.With(slog.String("category", string(c)))
}

// WithRequestID stores a request ID in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts a request ID previously stored in the context.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// FromContext returns a logger enriched with the context's request ID.
func FromContext(ctx context.Context, l *slog.Logger) *slog.Logger {
	if id := RequestID(ctx); id != "" {
		return l.With(slog.String("request_id", id))
	}
	return l
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
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
