package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

type CtxKey string

const RequestIDKey CtxKey = "request_id"

func New(level string, environment string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:     ParseLevel(level),
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// slog hands us a *slog.Source here, never a *runtime.Frame.
			if a.Key == slog.SourceKey {
				src, ok := a.Value.Any().(*slog.Source)
				if !ok || src == nil {
					return a
				}
				return slog.String("source", fmt.Sprintf("%s:%d", filepath.Base(src.File), src.Line))
			}
			return a
		},
	}

	var handler slog.Handler
	if environment == "development" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

func ParseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func FromCtx(ctx context.Context) *slog.Logger {
	log := slog.Default()
	if rid, ok := ctx.Value(RequestIDKey).(string); ok {
		return log.With("request_id", rid)
	}
	return log
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, RequestIDKey, requestID)
}
