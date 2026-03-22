package constraints

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		createFile      bool
		wantLoaded      bool
		wantCount       int
		wantErr         bool
		wantConstraints []string
	}{
		{
			name:       "valid file with multiple constraints",
			createFile: true,
			content: `constraints:
  - "Never force-push to main"
  - "Always create a branch"
  - "Never commit secrets"
`,
			wantLoaded:      true,
			wantCount:       3,
			wantConstraints: []string{"Never force-push to main", "Always create a branch", "Never commit secrets"},
		},
		{
			name:       "missing file returns Loaded=false without error",
			createFile: false,
			wantLoaded: false,
			wantCount:  0,
		},
		{
			name:       "malformed YAML returns error",
			createFile: true,
			content:    "constraints: [invalid yaml\n  - broken",
			wantErr:    true,
		},
		{
			name:       "empty constraints list",
			createFile: true,
			content:    "constraints: []\n",
			wantLoaded: true,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "constraints.yaml")

			if tt.createFile {
				if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			cc, err := LoadFile(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cc.Loaded != tt.wantLoaded {
				t.Errorf("Loaded = %v, want %v", cc.Loaded, tt.wantLoaded)
			}
			if len(cc.Constraints) != tt.wantCount {
				t.Errorf("len(Constraints) = %d, want %d", len(cc.Constraints), tt.wantCount)
			}
			for i, want := range tt.wantConstraints {
				if cc.Constraints[i] != want {
					t.Errorf("Constraints[%d] = %q, want %q", i, cc.Constraints[i], want)
				}
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() returned error: %v", err)
	}
	if path == "" {
		t.Fatal("DefaultPath() returned empty string")
	}
	if filepath.Base(path) != "constraints.yaml" {
		t.Errorf("DefaultPath() base = %q, want constraints.yaml", filepath.Base(path))
	}
}
