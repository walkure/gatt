package logger

import (
	"fmt"
	"log/slog"
)

// logger
var logger = slog.Default()

// SetLogger updates logger
func SetLogger(newLogger *slog.Logger) {
	logger = newLogger
}

// Debugf outputs DEBUG level log with format
func Debugf(format string, a ...any) {
	logger.Debug(fmt.Sprintf(format, a...))
}

// Infof outputs INFO level log with format
func Infof(format string, a ...any) {
	logger.Info(fmt.Sprintf(format, a...))
}

// Warnf outputs WARN level log with format
func Warnf(format string, a ...any) {
	logger.Warn(fmt.Sprintf(format, a...))
}

// Errorf outputs ERROR level log with format
func Errorf(format string, a ...any) {
	logger.Error(fmt.Sprintf(format, a...))
}
