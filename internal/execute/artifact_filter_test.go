package execute

import (
	"reflect"
	"testing"
)

// TestFilterArtifactsByGlobs_EmptyGlobsReturnsUnchanged pins the
// backward-compatibility contract: a review spec without artifact_globs
// must not drop any artifacts. Every gate the runner already opens today
// runs with empty globs, so regressing this would silently empty every
// existing approval drawer.
func TestFilterArtifactsByGlobs_EmptyGlobsReturnsUnchanged(t *testing.T) {
	arts := []StepArtifact{
		{Path: "a.png"},
		{Path: "b.md"},
	}
	got := FilterArtifactsByGlobs(arts, nil)
	if !reflect.DeepEqual(got, arts) {
		t.Errorf("nil globs: got %v, want unchanged %v", got, arts)
	}
	got = FilterArtifactsByGlobs(arts, []string{})
	if !reflect.DeepEqual(got, arts) {
		t.Errorf("empty globs: got %v, want unchanged %v", got, arts)
	}
}

// TestFilterArtifactsByGlobs_DropsNonImageInImageGallery is the real-world
// motivating case: Miyazaki's review.kind:image-gallery declares
// artifact_globs of "Assets/_Project/Art/Generated/**/*.png" and the
// equivalent *.jpg pattern. When a step also produces a .md design doc
// (character reference), the doc must not reach ImageGalleryView or it
// renders as a broken <img> tile.
func TestFilterArtifactsByGlobs_DropsNonImageInImageGallery(t *testing.T) {
	globs := []string{
		"Assets/_Project/Art/Generated/**/*.png",
		"Assets/_Project/Art/Generated/**/*.jpg",
	}
	arts := []StepArtifact{
		{Path: "Assets/_Project/Art/Generated/Characters/hero-child-idle.png"},
		{Path: "Assets/_Project/Art/Generated/Characters/hero-child-idle.jpg"},
		{Path: ".context/features/tower-defense-level-1/young-adventurer-character-reference.md"},
	}
	got := FilterArtifactsByGlobs(arts, globs)
	if len(got) != 2 {
		t.Fatalf("got %d artifacts, want 2 (both images)", len(got))
	}
	for _, a := range got {
		if a.Path == ".context/features/tower-defense-level-1/young-adventurer-character-reference.md" {
			t.Errorf(".md artifact %q leaked past glob filter", a.Path)
		}
	}
}

// TestFilterArtifactsByGlobs_DoubleStarMatchesAnyDepth guards the
// doublestar semantics: agent globs routinely use **/ to match arbitrary
// sub-directory nesting under an output root. A naive single-* matcher
// would reject "Assets/.../Characters/hero.png" against the pattern
// "Assets/**/*.png" and strand every image in a gate.
func TestFilterArtifactsByGlobs_DoubleStarMatchesAnyDepth(t *testing.T) {
	globs := []string{"Assets/**/*.png"}
	cases := []struct {
		path string
		want bool
	}{
		{"Assets/a.png", true},                            // zero intermediate segments
		{"Assets/Characters/hero.png", true},              // one intermediate
		{"Assets/_Project/Art/Characters/hero.png", true}, // three intermediate
		{"Other/hero.png", false},                         // prefix mismatch
		{"Assets/Characters/hero.jpg", false},             // extension mismatch
	}
	for _, tc := range cases {
		arts := []StepArtifact{{Path: tc.path}}
		got := FilterArtifactsByGlobs(arts, globs)
		matched := len(got) == 1
		if matched != tc.want {
			t.Errorf("matchGlob(%q, %q) matched=%v, want %v", globs[0], tc.path, matched, tc.want)
		}
	}
}

// TestFilterArtifactsByGlobs_MultipleGlobsAreOr verifies that supplying
// multiple patterns keeps an artifact that matches any one of them. This
// is how agents mix file types (e.g. "*.png" + "*.jpg" in an
// image-gallery, or "*.ogg" + "*.mp3" in an audio-player).
func TestFilterArtifactsByGlobs_MultipleGlobsAreOr(t *testing.T) {
	globs := []string{"**/*.png", "**/*.jpg"}
	arts := []StepArtifact{
		{Path: "a/b.png"},
		{Path: "a/c.jpg"},
		{Path: "a/d.gif"}, // matches neither
	}
	got := FilterArtifactsByGlobs(arts, globs)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (png+jpg)", len(got))
	}
}

// TestFilterArtifactsByGlobs_WindowsBackslashPathsNormalize protects
// Windows users: if an agent echoes an OS-native path
// ("Assets\\_Project\\...") into artifacts.yaml, the forward-slash globs
// declared in agent frontmatter must still match. Callers already
// normalize in most code paths, but this is a safety net.
func TestFilterArtifactsByGlobs_WindowsBackslashPathsNormalize(t *testing.T) {
	globs := []string{"Assets/**/*.png"}
	arts := []StepArtifact{
		{Path: `Assets\_Project\Art\Generated\Characters\hero.png`},
	}
	got := FilterArtifactsByGlobs(arts, globs)
	if len(got) != 1 {
		t.Errorf("Windows path %q did not match %q; got %d artifacts", arts[0].Path, globs[0], len(got))
	}
}

// TestFilterArtifactsByGlobs_InvalidGlobSkippedNotFatal ensures a single
// malformed pattern doesn't nuke the whole filter. path.Match returns an
// error on e.g. "[unclosed"; we skip that pattern but still honor the
// others. This matches the user's mental model: one typo shouldn't
// silently empty the drawer.
func TestFilterArtifactsByGlobs_InvalidGlobSkippedNotFatal(t *testing.T) {
	globs := []string{"[unclosed", "**/*.png"}
	arts := []StepArtifact{
		{Path: "a.png"},
		{Path: "b.md"},
	}
	got := FilterArtifactsByGlobs(arts, globs)
	if len(got) != 1 || got[0].Path != "a.png" {
		t.Errorf("got %v, want [{Path: a.png}] (invalid glob skipped, valid glob honored)", got)
	}
}

// TestFilterArtifactsByGlobs_NoMatchReturnsEmpty documents behavior when
// the step produced artifacts but none match the review's globs: the
// gate still opens (caller decides), but carries an empty slice. The
// user can still Approve/Revise/Abort based on the prompt text alone.
// Returning nil here would be ambiguous with "filter skipped" -- we
// return an allocated empty slice to keep the intent explicit.
func TestFilterArtifactsByGlobs_NoMatchReturnsEmpty(t *testing.T) {
	globs := []string{"**/*.png"}
	arts := []StepArtifact{
		{Path: "a.md"},
		{Path: "b.txt"},
	}
	got := FilterArtifactsByGlobs(arts, globs)
	if got == nil {
		t.Error("got nil slice, want empty non-nil")
	}
	if len(got) != 0 {
		t.Errorf("got %d artifacts, want 0", len(got))
	}
}
