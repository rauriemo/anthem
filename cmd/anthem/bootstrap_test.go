package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapDir(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(t *testing.T, dir string)
		wantVoice     bool
		wantNoChange  bool
	}{
		{
			name:      "creates directory and VOICE.md when missing",
			setup:     func(t *testing.T, dir string) {},
			wantVoice: true,
		},
		{
			name: "skips when directory and VOICE.md already exist",
			setup: func(t *testing.T, dir string) {
				os.MkdirAll(dir, 0755)
				os.WriteFile(filepath.Join(dir, "VOICE.md"), []byte("existing"), 0644)
			},
			wantVoice:    true,
			wantNoChange: true,
		},
		{
			name: "creates VOICE.md when directory exists but file missing",
			setup: func(t *testing.T, dir string) {
				os.MkdirAll(dir, 0755)
			},
			wantVoice: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			anthemDir := filepath.Join(tmpDir, ".anthem")
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

			tt.setup(t, anthemDir)

			var existingContent []byte
			if tt.wantNoChange {
				existingContent, _ = os.ReadFile(filepath.Join(anthemDir, "VOICE.md"))
			}

			err := bootstrapDir(anthemDir, logger)
			if err != nil {
				t.Fatalf("bootstrapDir failed: %v", err)
			}

			// Check directory exists
			info, err := os.Stat(anthemDir)
			if err != nil {
				t.Fatalf("anthem dir not created: %v", err)
			}
			if !info.IsDir() {
				t.Fatal("anthem path is not a directory")
			}

			// Check VOICE.md exists
			voicePath := filepath.Join(anthemDir, "VOICE.md")
			content, err := os.ReadFile(voicePath)
			if err != nil {
				t.Fatalf("VOICE.md not created: %v", err)
			}

			if tt.wantNoChange {
				if string(content) != string(existingContent) {
					t.Error("existing VOICE.md was overwritten")
				}
			} else if len(content) == 0 {
				t.Error("VOICE.md is empty")
			}
		})
	}
}
