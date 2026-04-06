package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/rauriemo/anthem/internal/config"
)

func TestCreateTracker_EmptyKind(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = ""

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	trk, err := createTracker(context.Background(), &cfg, logger)
	if err != nil {
		t.Fatalf("expected nil error for empty tracker kind, got: %v", err)
	}
	if trk != nil {
		t.Fatalf("expected nil tracker for empty kind, got: %v", trk)
	}
}

func TestCreateTracker_UnsupportedKind(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "jira"

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	_, err := createTracker(context.Background(), &cfg, logger)
	if err == nil {
		t.Fatal("expected error for unsupported tracker kind")
	}
}
