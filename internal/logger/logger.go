// Package logger provides a slog-backed adapter that satisfies the opamp-go
// client and server Logger interfaces (both require Debugf/Errorf with a ctx).
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Logger adapts *slog.Logger to opamp-go's types.Logger interface.
type Logger struct {
	l *slog.Logger
}

// New builds a JSON structured logger at the given level ("debug","info","warn","error").
func New(level string) *Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return &Logger{l: slog.New(h)}
}

// Slog exposes the underlying structured logger for the app's own logging.
func (g *Logger) Slog() *slog.Logger { return g.l }

// Debugf implements opamp-go types.Logger.
func (g *Logger) Debugf(_ context.Context, format string, v ...interface{}) {
	g.l.Debug(fmt.Sprintf(format, v...))
}

// Errorf implements opamp-go types.Logger.
func (g *Logger) Errorf(_ context.Context, format string, v ...interface{}) {
	g.l.Error(fmt.Sprintf(format, v...))
}
