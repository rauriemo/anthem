package github

import "testing"

func TestParseRepo(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{name: "valid", input: "user/repo", wantOwner: "user", wantRepo: "repo"},
		{name: "org repo", input: "my-org/my-repo", wantOwner: "my-org", wantRepo: "my-repo"},
		{name: "missing slash", input: "noslash", wantErr: true},
		{name: "empty owner", input: "/repo", wantErr: true},
		{name: "empty repo", input: "owner/", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := ParseRepo(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}
