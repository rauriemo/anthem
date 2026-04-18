package guests

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReviewSpec declares how a guest agent's step output should be rendered for
// user approval at a gate. It is parsed from the `review:` block in an agent's
// frontmatter. See docs/architecture/review-kinds.md for the full contract.
type ReviewSpec struct {
	Kind                 string      `yaml:"kind" json:"kind"`
	Title                string      `yaml:"title,omitempty" json:"title,omitempty"`
	ArtifactGlobs        []string    `yaml:"artifact_globs,omitempty" json:"artifact_globs,omitempty"`
	ContextFiles         []string    `yaml:"context_files,omitempty" json:"context_files,omitempty"`
	ShowPrompt           *bool       `yaml:"show_prompt,omitempty" json:"show_prompt,omitempty"`
	Panels               []PanelSpec `yaml:"panels,omitempty" json:"panels,omitempty"`
	NotificationTemplate string      `yaml:"notification_template,omitempty" json:"notification_template,omitempty"`
	PartialRevise        *bool       `yaml:"partial_revise,omitempty" json:"partial_revise,omitempty"`
	CoherenceHint        string      `yaml:"coherence_hint,omitempty" json:"coherence_hint,omitempty"`
	SchemaVersion        string      `yaml:"schema_version,omitempty" json:"schema_version,omitempty"`
}

// PanelSpec is a composable widget inside a ReviewSpec. Panels reference a
// built-in panel `type` and optionally a `source` glob plus per-type config.
type PanelSpec struct {
	Type       string   `yaml:"type" json:"type"`
	Title      string   `yaml:"title,omitempty" json:"title,omitempty"`
	Source     string   `yaml:"source,omitempty" json:"source,omitempty"`
	ItemFields []string `yaml:"item_fields,omitempty" json:"item_fields,omitempty"`
}

// CoreReviewKinds is the pinned set of media-shape review kinds shipped with
// Prism. Any kind ID outside this set must either be declared in the project's
// .prism/review-extensions.yaml manifest (with an `extends:` fallback) or it
// will be flagged as an error by ValidateReviewSpec.
//
// This list is the authoritative source for the Anthem side; the Prism
// frontend pins a parity test against it (see Phase 7.6).
var CoreReviewKinds = []string{
	"image-gallery",
	"video-preview",
	"audio-player",
	"document",
	"web-preview",
	"model-3d-preview",
	"structured-data",
	"artifact-list",
}

// CorePanelTypes is the pinned set of panel types shipped in Prism.
var CorePanelTypes = []string{
	"viewer",
	"metadata-table",
	"tree",
	"list",
	"diff",
	"markdown",
}

// kindsSupportingPartialRevise enumerates which core kinds handle per-artifact
// selective revise. See Phase 5a.4 in the plan.
var kindsSupportingPartialRevise = map[string]bool{
	"image-gallery":    true,
	"video-preview":    true,
	"audio-player":     true,
	"document":         true,
	"web-preview":      true,
	"model-3d-preview": true,
	"artifact-list":    true,
	"structured-data":  false,
}

// personaShapedTokens are words that indicate a kind ID has drifted toward a
// persona ("quest-list", "sprite-gallery") instead of a media shape
// ("document", "image-gallery"). Catching these at validation time keeps core
// kind IDs portable across projects.
var personaShapedTokens = []string{
	"sprite", "quest", "story", "design",
	"scene", "animation", "balance", "rig",
}

// ReviewDiagnosticSeverity labels a diagnostic as an Error (guest loads with
// Review=nil), Warning (guest loads with the spec, author should fix), or
// Info (no action required, advisory only).
type ReviewDiagnosticSeverity string

const (
	SeverityError   ReviewDiagnosticSeverity = "error"
	SeverityWarning ReviewDiagnosticSeverity = "warning"
	SeverityInfo    ReviewDiagnosticSeverity = "info"
)

// ReviewDiagnostic is one problem (or advisory) produced by
// ValidateReviewSpec. Diagnostics are logged at guest load time and surfaced
// to Prism via the guest-capability channel so the frontend can render a
// dev-mode badge.
type ReviewDiagnostic struct {
	Severity ReviewDiagnosticSeverity `json:"severity"`
	Code     string                   `json:"code"`
	Message  string                   `json:"message"`
}

// Error implements the error interface so a diagnostic can be returned from
// callers that want to treat errors as errors.
func (d ReviewDiagnostic) Error() string {
	return fmt.Sprintf("[%s/%s] %s", d.Severity, d.Code, d.Message)
}

// ReviewExtensionManifest is the shape of .prism/review-extensions.yaml. It
// lets a project declare custom review kinds that extend one of the core
// kinds as a fallback for builds that don't have the extension renderer.
type ReviewExtensionManifest struct {
	SchemaVersion string                `yaml:"schema_version" json:"schema_version"`
	Kinds         []ReviewExtensionKind `yaml:"kinds" json:"kinds"`
}

// ReviewExtensionKind is one entry in the extension manifest.
type ReviewExtensionKind struct {
	ID          string `yaml:"id" json:"id"`
	Extends     string `yaml:"extends,omitempty" json:"extends,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// LoadExtensionManifest reads .prism/review-extensions.yaml relative to
// projectRoot and returns its contents. An absent file is not an error -- it
// returns an empty manifest. Malformed YAML returns an error.
func LoadExtensionManifest(projectRoot string) (ReviewExtensionManifest, error) {
	var manifest ReviewExtensionManifest
	path := filepath.Join(projectRoot, ".prism", "review-extensions.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return manifest, nil
		}
		return manifest, fmt.Errorf("reading extension manifest: %w", err)
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("parsing extension manifest %s: %w", path, err)
	}
	return manifest, nil
}

// ExtensionKindIDs returns the set of extension kind IDs declared in the
// manifest. Useful for validating that an agent's declared review.kind either
// hits a core kind or an extension kind.
func (m ReviewExtensionManifest) ExtensionKindIDs() map[string]string {
	ids := make(map[string]string, len(m.Kinds))
	for _, k := range m.Kinds {
		if k.ID != "" {
			ids[k.ID] = k.Extends
		}
	}
	return ids
}

// ValidateReviewSpec inspects a ReviewSpec for quality problems and returns a
// slice of severity-tagged diagnostics. The caller decides how to act on each
// severity -- convention:
//
//   - Error   : set GuestAgent.Review to nil, log loudly, don't hard crash.
//   - Warning : keep the spec, log, surface in dev UI.
//   - Info    : log once at startup.
//
// extensionIDs maps project-declared extension kind IDs to their `extends`
// fallback (empty string if no fallback). Nil means "no extensions declared".
func ValidateReviewSpec(spec *ReviewSpec, extensionIDs map[string]string) []ReviewDiagnostic {
	if spec == nil {
		return nil
	}
	var diags []ReviewDiagnostic

	// --- Kind checks -------------------------------------------------------

	if strings.TrimSpace(spec.Kind) == "" {
		diags = append(diags, ReviewDiagnostic{
			Severity: SeverityError,
			Code:     "review.kind.missing",
			Message:  "review.kind is required",
		})
	} else {
		kind := strings.TrimSpace(spec.Kind)
		if !isCoreKind(kind) {
			// Not a core kind. Check extensions.
			if _, ok := extensionIDs[kind]; !ok {
				diags = append(diags, ReviewDiagnostic{
					Severity: SeverityError,
					Code:     "review.kind.unknown",
					Message: fmt.Sprintf(
						"review.kind %q is not a core kind and is not declared in .prism/review-extensions.yaml; valid core kinds: %s",
						kind, strings.Join(CoreReviewKinds, ", "),
					),
				})
			} else {
				diags = append(diags, ReviewDiagnostic{
					Severity: SeverityInfo,
					Code:     "review.kind.extension",
					Message: fmt.Sprintf(
						"review.kind %q is an extension kind; portability requires consumers to have that extension available",
						kind,
					),
				})
			}
		}
		// Persona-shaped lint (non-fatal but warns). Extensions are exempt --
		// projects are allowed to use persona words in their own extension
		// IDs; it's only the core kind list that must stay neutral.
		if isCoreKind(kind) && containsPersonaToken(kind) {
			diags = append(diags, ReviewDiagnostic{
				Severity: SeverityWarning,
				Code:     "review.kind.persona_shaped",
				Message:  fmt.Sprintf("core kind %q contains a persona-shaped token; prefer media-shape names", kind),
			})
		}
	}

	// --- Panels ------------------------------------------------------------

	for i, p := range spec.Panels {
		pt := strings.TrimSpace(p.Type)
		if pt == "" {
			diags = append(diags, ReviewDiagnostic{
				Severity: SeverityError,
				Code:     "review.panels.type.missing",
				Message:  fmt.Sprintf("review.panels[%d]: type is required", i),
			})
			continue
		}
		if !isCorePanelType(pt) {
			diags = append(diags, ReviewDiagnostic{
				Severity: SeverityError,
				Code:     "review.panels.type.unknown",
				Message: fmt.Sprintf(
					"review.panels[%d]: type %q is not a known panel type; valid types: %s",
					i, pt, strings.Join(CorePanelTypes, ", "),
				),
			})
		}
	}

	// --- Globs -------------------------------------------------------------

	for i, g := range spec.ArtifactGlobs {
		if !isValidGlob(g) {
			diags = append(diags, ReviewDiagnostic{
				Severity: SeverityWarning,
				Code:     "review.artifact_globs.invalid",
				Message:  fmt.Sprintf("review.artifact_globs[%d]: %q is not a valid glob", i, g),
			})
		}
	}

	// --- partial_revise -----------------------------------------------------

	if spec.PartialRevise != nil && !*spec.PartialRevise {
		if supports, ok := kindsSupportingPartialRevise[spec.Kind]; ok && !supports {
			diags = append(diags, ReviewDiagnostic{
				Severity: SeverityWarning,
				Code:     "review.partial_revise.redundant",
				Message: fmt.Sprintf(
					"review.partial_revise:false is redundant for kind %q (kind does not support partial revise)",
					spec.Kind,
				),
			})
		}
	}

	// --- notification_template variables -----------------------------------

	if spec.NotificationTemplate != "" {
		for _, v := range extractTemplateVariables(spec.NotificationTemplate) {
			if !isKnownNotificationVar(v) {
				diags = append(diags, ReviewDiagnostic{
					Severity: SeverityWarning,
					Code:     "review.notification_template.unknown_var",
					Message: fmt.Sprintf(
						"review.notification_template references unknown variable {%s}; known variables: {agent_name}, {step_title}, {artifact_count}",
						v,
					),
				})
			}
		}
	}

	// Deterministic ordering so test output is stable.
	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].Severity != diags[j].Severity {
			return severityRank(diags[i].Severity) < severityRank(diags[j].Severity)
		}
		return diags[i].Code < diags[j].Code
	})

	return diags
}

// HasError returns true if any diagnostic in the slice is an Error.
func HasError(diags []ReviewDiagnostic) bool {
	for _, d := range diags {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// KindSupportsPartialRevise reports whether the given core kind handles
// per-artifact selective revise. Unknown kinds default to true (forward-compat
// for extension kinds; they opt out explicitly via review.partial_revise:
// false).
func KindSupportsPartialRevise(kind string) bool {
	if v, ok := kindsSupportingPartialRevise[kind]; ok {
		return v
	}
	return true
}

// --- helpers -----------------------------------------------------------------

func isCoreKind(kind string) bool {
	for _, k := range CoreReviewKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func isCorePanelType(pt string) bool {
	for _, t := range CorePanelTypes {
		if t == pt {
			return true
		}
	}
	return false
}

func containsPersonaToken(s string) bool {
	lower := strings.ToLower(s)
	for _, tok := range personaShapedTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// isValidGlob accepts anything filepath.Match accepts without error. It
// intentionally tolerates ** (doublestar) since filepath.Match doesn't
// validate that but real glob matchers (e.g. doublestar) will.
func isValidGlob(g string) bool {
	if strings.TrimSpace(g) == "" {
		return false
	}
	// filepath.Match rejects unmatched brackets, which is what we want.
	// It accepts ** as literal characters, which is fine for this check --
	// we're only screening for obviously broken patterns.
	_, err := filepath.Match(g, g)
	return err == nil
}

var templateVarPattern = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

func extractTemplateVariables(s string) []string {
	matches := templateVarPattern.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

func isKnownNotificationVar(v string) bool {
	switch v {
	case "agent_name", "step_title", "artifact_count":
		return true
	}
	return false
}

func severityRank(s ReviewDiagnosticSeverity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInfo:
		return 2
	}
	return 3
}
