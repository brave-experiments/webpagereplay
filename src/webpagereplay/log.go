package webpagereplay

import (
	"context"
	"log/slog"
	"os"
)

// Logger is a simple logging interface that can be easily swapped.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
	WithContext(ctx context.Context) Logger
}

type slogLogger struct {
	logger *slog.Logger
}

func (l *slogLogger) Debug(msg string, args ...any) {
	l.logger.Debug(msg, args...)
}

func (l *slogLogger) Info(msg string, args ...any) {
	l.logger.Info(msg, args...)
}

func (l *slogLogger) Warn(msg string, args ...any) {
	l.logger.Warn(msg, args...)
}

func (l *slogLogger) Error(msg string, args ...any) {
	l.logger.Error(msg, args...)
}

func (l *slogLogger) With(args ...any) Logger {
	return &slogLogger{logger: l.logger.With(args...)}
}

func (l *slogLogger) WithContext(ctx context.Context) Logger {
	panic("Not implemented.")
}

var (
	levelVar      = &slog.LevelVar{}
	defaultLogger = &slogLogger{
		logger: slog.New(makeHandler(levelVar)),
	}
)

func makeHandler(level slog.Leveler) slog.Handler {
	return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})
}

// SetLogLevel sets the logging level. Supported levels are:
// DEBUG, INFO, WARN, ERROR.
func SetLogLevel(level string) error {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return err
	}
	levelVar.Set(l)
	return nil
}

// Log is a helper function that returns the default logger.
func Log() Logger {
	return defaultLogger
}

type nullLogger struct{}

func (l *nullLogger) Debug(msg string, args ...any) {}
func (l *nullLogger) Info(msg string, args ...any)  {}
func (l *nullLogger) Warn(msg string, args ...any)  {}
func (l *nullLogger) Error(msg string, args ...any) {}
func (l *nullLogger) With(args ...any) Logger       { return l }
func (l *nullLogger) WithContext(ctx context.Context) Logger {
	panic("Not implemented.")
}

// NullLogger returns a logger that discards all log entries.
func NullLogger() Logger {
	return &nullLogger{}
}
