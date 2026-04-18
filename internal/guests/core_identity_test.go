package guests

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoreKindIdentity_NoPersonaLeak pins the core-kind identity rule: every
// entry in CoreReviewKinds must be a media-shape ID, not a persona-shaped ID.
// Keeps future PRs from sneaking in RebelTower- (or any project-) shaped kinds.
// Paired with the runtime validator's review.kind.persona_shaped diagnostic.
func TestCoreKindIdentity_NoPersonaLeak(t *testing.T) {
	for _, kind := range CoreReviewKinds {
		if strings.TrimSpace(kind) == "" {
			t.Errorf("CoreReviewKinds contains an empty entry")
			continue
		}
		lower := strings.ToLower(kind)
		for _, tok := range personaShapedTokens {
			if strings.Contains(lower, tok) {
				t.Errorf("core kind %q contains persona-shaped token %q — core kinds must be media-shape-based, not persona-based; compose with panels instead", kind, tok)
			}
		}
	}
}

// TestCoreKindIdentity_UniqueIDs guards against accidental duplicates sneaking
// in after a rename.
func TestCoreKindIdentity_UniqueIDs(t *testing.T) {
	seen := make(map[string]bool, len(CoreReviewKinds))
	for _, kind := range CoreReviewKinds {
		if seen[kind] {
			t.Errorf("CoreReviewKinds contains duplicate entry %q", kind)
		}
		seen[kind] = true
	}
}

// TestCorePanelTypes_NoPersonaLeak applies the same rule to panel types. Panels
// are also shape-based (viewer / list / tree / diff / …) and must stay generic.
func TestCorePanelTypes_NoPersonaLeak(t *testing.T) {
	for _, panel := range CorePanelTypes {
		lower := strings.ToLower(panel)
		for _, tok := range personaShapedTokens {
			if strings.Contains(lower, tok) {
				t.Errorf("core panel type %q contains persona-shaped token %q", panel, tok)
			}
		}
	}
}

// TestCoreKindIdentity_MatchesReviewKindsDoc is the authority test: the pinned
// core-kind list in CoreReviewKinds must be exactly the kinds documented in
// docs/architecture/review-kinds.md. Either side going out of sync is a
// failure — the doc is the single source of truth for external consumers, and
// the code is the single source of truth for runtime behavior, so they
// literally have to agree.
func TestCoreKindIdentity_MatchesReviewKindsDoc(t *testing.T) {
	docPath := findReviewKindsDoc(t)
	kindsInDoc := extractKindTableIDs(t, docPath)
	if len(kindsInDoc) == 0 {
		t.Fatalf("found no ``-quoted kind IDs in %s — doc format changed?", docPath)
	}

	want := make(map[string]bool, len(CoreReviewKinds))
	for _, k := range CoreReviewKinds {
		want[k] = true
	}
	got := make(map[string]bool, len(kindsInDoc))
	for _, k := range kindsInDoc {
		got[k] = true
	}
	for _, k := range CoreReviewKinds {
		if !got[k] {
			t.Errorf("core kind %q is in CoreReviewKinds but NOT documented in review-kinds.md", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("kind %q documented in review-kinds.md but NOT in CoreReviewKinds", k)
		}
	}
}

// findReviewKindsDoc walks up from the test file to find the doc. Repo layout
// is anthem/docs/architecture/review-kinds.md.
func findReviewKindsDoc(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "docs", "architecture", "review-kinds.md")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate docs/architecture/review-kinds.md from %s", wd)
	return ""
}

// extractKindTableIDs pulls all backtick-quoted identifiers from the "Core
// kinds" table in review-kinds.md. We look at the table whose rows begin with
// `| \`<id>\` |`. This keeps us from matching stray backticked words elsewhere
// in the document (like panel names or code references).
func extractKindTableIDs(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var ids []string
	inCoreKindsSection := false
	scanner := bufio.NewScanner(f)
	// Some kind table rows are long; bump the buffer.
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Core kinds") {
			inCoreKindsSection = true
			continue
		}
		if inCoreKindsSection && strings.HasPrefix(trimmed, "## ") {
			break
		}
		if !inCoreKindsSection {
			continue
		}
		if !strings.HasPrefix(trimmed, "| `") {
			continue
		}
		// | `kind-id` | ... |
		rest := trimmed[3:] // strip "| `"
		end := strings.Index(rest, "`")
		if end <= 0 {
			continue
		}
		ids = append(ids, rest[:end])
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return ids
}
