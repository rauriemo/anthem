package channel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCredentials(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantNil  bool
		wantErr  bool
		checkBot string
		checkApp string
	}{
		{
			name: "valid slack credentials",
			content: `slack:
  bot_token: "xoxb-test-token"
  app_token: "xapp-test-token"
`,
			checkBot: "xoxb-test-token",
			checkApp: "xapp-test-token",
		},
		{
			name:    "empty file",
			content: "",
			wantNil: false,
		},
		{
			name:    "invalid yaml",
			content: "{{not yaml at all",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "channels.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			cfg, err := LoadCredentials(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil && cfg != nil {
				t.Fatal("expected nil config")
			}

			if tt.checkBot != "" {
				if cfg == nil || cfg.Slack == nil {
					t.Fatal("expected slack credentials")
				}
				if cfg.Slack.BotToken != tt.checkBot {
					t.Errorf("BotToken = %q, want %q", cfg.Slack.BotToken, tt.checkBot)
				}
				if cfg.Slack.AppToken != tt.checkApp {
					t.Errorf("AppToken = %q, want %q", cfg.Slack.AppToken, tt.checkApp)
				}
			}
		})
	}
}

func TestLoadCredentials_NonExistent(t *testing.T) {
	cfg, err := LoadCredentials(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config for missing file")
	}
}

func TestDefaultCredentialsPath(t *testing.T) {
	p, err := DefaultCredentialsPath()
	if err != nil {
		t.Fatalf("DefaultCredentialsPath() error: %v", err)
	}
	if filepath.Base(p) != "channels.yaml" {
		t.Errorf("expected filename channels.yaml, got %q", filepath.Base(p))
	}
	if filepath.Base(filepath.Dir(p)) != ".anthem" {
		t.Errorf("expected parent .anthem, got %q", filepath.Base(filepath.Dir(p)))
	}
}
