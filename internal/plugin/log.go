package plugin

import (
	"context"
	"io"
	"log/slog"
)

// loggerKey is the unexported context key under which a *slog.Logger is stored.
type loggerKey struct{}

// discardLogger drops everything; returned when no logger is in the context so
// callers can log unconditionally without nil checks.
var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// WithLogger returns a copy of ctx carrying l. The server attaches a
// tracker-scoped logger before calling a plugin's Run, so plugins (and the
// shared HTTP/registry helpers) can emit debug logs that land in the dashboard
// log stream.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFrom returns the logger stored in ctx, or a discard logger if none.
// Plugins call this to log: plugin.LoggerFrom(ctx).Debug("fetching", "url", u).
func LoggerFrom(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return discardLogger
}
