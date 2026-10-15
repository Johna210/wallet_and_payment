// Package logging builds the service's structured JSON logger.
package logging

import (
	"io"
	"log/slog"
)

// New returns a JSON logger writing to w at the given minimum level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
