package orchestrator

import (
	"testing"

	"github.com/rauriemo/anthem/internal/config"
)

// TestProjectSlug_StripsOwnerForURLSafety pins the invariant that
// projectSlug() returns a single-path-segment string (no '/'). This is
// the slug embedded in Prism's /files/{slug}/{path} artifact URL. If
// the slug contains a slash, HTTP path normalization splits it across
// the routing boundary and FastAPI sees
// slug="rauriemo" + path="RebelTower/..." which fails ProjectResolver.
//
// The real-world failure: tracker.repo "rauriemo/RebelTower" produced
// a slug the file server saw as just "rauriemo", so every image tile
// in an approval gate returned 404.
func TestProjectSlug_StripsOwnerForURLSafety(t *testing.T) {
	cases := []struct {
		name string
		repo string
		want string
	}{
		{"owner slash repo", "rauriemo/RebelTower", "RebelTower"},
		{"multiple slashes take last", "github.com/rauriemo/RebelTower", "RebelTower"},
		{"plain repo name", "RebelTower", "RebelTower"},
		{"empty falls through to default", "", "default"},
		{"trailing slash is not the owner separator", "rauriemo/", "rauriemo/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Orchestrator{
				cfg: &config.Config{Tracker: config.TrackerConfig{Repo: tc.repo}},
			}
			got := o.projectSlug()
			if got != tc.want {
				t.Errorf("projectSlug(%q) = %q, want %q", tc.repo, got, tc.want)
			}
		})
	}
}
