package execute

import (
	"path"
	"path/filepath"
	"strings"
)

// FilterArtifactsByGlobs returns only the artifacts whose Path matches at
// least one of the provided globs.
//
// Semantics:
//
//   - If globs is empty/nil, the input slice is returned unchanged. This is
//     the backward-compatible default: a review spec without
//     artifact_globs surfaces every artifact the step produced.
//   - An artifact matches when any single glob matches its Path. Globs are
//     OR-ed, not AND-ed -- e.g. ["**/*.png", "**/*.jpg"] includes artifacts
//     of either extension.
//   - Invalid (unparseable) globs are silently skipped so a typo in one
//     pattern doesn't swallow every artifact. Validation warnings are
//     emitted separately at guest-load time (see
//     guests.ValidateReviewSpec).
//   - Paths and globs are normalized to forward slashes before matching so
//     artifact paths produced on Windows (which may contain backslashes if
//     an agent echoes an OS-native path) match the forward-slash globs
//     declared in agent frontmatter.
//
// The primary motivation is enforcing agent review-kind contracts at the
// gate boundary: e.g. an image-gallery gate should never surface a .md
// document to ImageGalleryView (which renders everything as <img src>),
// and a document gate should not surface a .png that a markdown viewer
// cannot display. Silently filtering at the runner keeps the contract
// declarative in agent frontmatter rather than forcing per-view logic
// into every Prism review component.
func FilterArtifactsByGlobs(arts []StepArtifact, globs []string) []StepArtifact {
	if len(globs) == 0 {
		return arts
	}
	out := make([]StepArtifact, 0, len(arts))
	for _, a := range arts {
		if matchAnyGlob(a.Path, globs) {
			out = append(out, a)
		}
	}
	return out
}

func matchAnyGlob(p string, globs []string) bool {
	for _, g := range globs {
		if matchGlob(g, p) {
			return true
		}
	}
	return false
}

// matchGlob matches a forward-slash-normalized path against a doublestar
// glob. Supported syntax:
//
//   - **        matches zero or more path segments (a "super segment")
//   - *         matches any single path segment (no cross-segment)
//   - ?, [...]  as in path.Match, applied per-segment
//
// Both pattern and name are normalized with filepath.ToSlash before
// matching so this works identically on Windows and POSIX even when the
// artifact path carries backslashes (e.g. an agent dumped an OS-native
// path into artifacts.yaml). We intentionally do not fall back to
// filepath.Match for the non-doublestar case because that would flip
// separator meaning on Windows (path.Match treats / as the separator;
// filepath.Match treats \).
func matchGlob(pattern, name string) bool {
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchSegments is a plain recursive matcher. Doublestar (** as an entire
// segment) branches into "match zero segments" and "consume one and stay
// on the same pattern position". Every other segment is matched by
// path.Match, which covers *, ?, and character classes without crossing
// segment boundaries.
func matchSegments(pats, segs []string) bool {
	if len(pats) == 0 {
		return len(segs) == 0
	}
	if pats[0] == "**" {
		if matchSegments(pats[1:], segs) {
			return true
		}
		if len(segs) == 0 {
			return false
		}
		return matchSegments(pats, segs[1:])
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pats[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pats[1:], segs[1:])
}
