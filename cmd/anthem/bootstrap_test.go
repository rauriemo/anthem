package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapDir(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(t *testing.T, dir string)
		wantNoChange bool
	}{
		{
			name:  "creates directory and all files when missing",
			setup: func(t *testing.T, dir string) {},
		},
		{
			name: "skips when directory and files already exist",
			setup: func(t *testing.T, dir string) {
				os.MkdirAll(dir, 0755)
				os.WriteFile(filepath.Join(dir, "VOICE.md"), []byte("existing voice"), 0644)
				os.WriteFile(filepath.Join(dir, "constraints.yaml"), []byte("existing constraints"), 0644)
			},
			wantNoChange: true,
		},
		{
			name: "creates files when directory exists but files missing",
			setup: func(t *testing.T, dir string) {
				os.MkdirAll(dir, 0755)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			anthemDir := filepath.Join(tmpDir, ".anthem")
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

			tt.setup(t, anthemDir)

			var existingVoice, existingConstraints []byte
			if tt.wantNoChange {
				existingVoice, _ = os.ReadFile(filepath.Join(anthemDir, "VOICE.md"))
				existingConstraints, _ = os.ReadFile(filepath.Join(anthemDir, "constraints.yaml"))
			}

			err := bootstrapDir(anthemDir, logger)
			if err != nil {
				t.Fatalf("bootstrapDir failed: %v", err)
			}

			info, err := os.Stat(anthemDir)
			if err != nil {
				t.Fatalf("anthem dir not created: %v", err)
			}
			if !info.IsDir() {
				t.Fatal("anthem path is not a directory")
			}

			voiceContent, err := os.ReadFile(filepath.Join(anthemDir, "VOICE.md"))
			if err != nil {
				t.Fatalf("VOICE.md not created: %v", err)
			}

			constraintsContent, err := os.ReadFile(filepath.Join(anthemDir, "constraints.yaml"))
			if err != nil {
				t.Fatalf("constraints.yaml not created: %v", err)
			}

			if tt.wantNoChange {
				if string(voiceContent) != string(existingVoice) {
					t.Error("existing VOICE.md was overwritten")
				}
				if string(constraintsContent) != string(existingConstraints) {
					t.Error("existing constraints.yaml was overwritten")
				}
			} else {
				if len(voiceContent) == 0 {
					t.Error("VOICE.md is empty")
				}
				if len(constraintsContent) == 0 {
					t.Error("constraints.yaml is empty")
				}
			}
		})
	}
}
