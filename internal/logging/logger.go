package logging

import (
	"io"
	"log/slog"
)

// NewLogger creates a structured JSON logger writing to the given writer.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
	}))
}
