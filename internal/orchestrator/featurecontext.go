package orchestrator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type DecisionEntry struct {
	ID        string   `yaml:"id"`
	Date      string   `yaml:"date"`
	Decision  string   `yaml:"decision"`
	Rationale string   `yaml:"rationale"`
	DecidedBy string   `yaml:"decided_by"`
	Status    string   `yaml:"status"`
	Affects   []string `yaml:"affects"`
}

type DecisionsFile struct {
	SchemaVersion string          `yaml:"schema_version"`
	Decisions     []DecisionEntry `yaml:"decisions"`
}

type ArtifactEntry struct {
	ID             string            `yaml:"id"`
	Type           string            `yaml:"type"`
	Path           string            `yaml:"path"`
	CreatedBy      string            `yaml:"created_by"`
	CreatedAt      string            `yaml:"created_at"`
	Feature        string            `yaml:"feature"`
	Status         string            `yaml:"status"`
	ApprovedBy     string            `yaml:"approved_by"`
	Description    string            `yaml:"description"`
	SourceArtifact string            `yaml:"source_artifact"`
	Tags           []string          `yaml:"tags"`
	Metadata       map[string]string `yaml:"metadata,omitempty"`
	DependsOn      []string          `yaml:"depends_on,omitempty"`
	Consumers      []string          `yaml:"consumers,omitempty"`
	UpdatedAt      string            `yaml:"updated_at,omitempty"`
	UpdatedBy      string            `yaml:"updated_by,omitempty"`
	// Origin is the structured provenance block stamped by the
	// featurewriter. Pointer semantics distinguish "never stamped"
	// (legacy entries read before backfill has run) from an explicit
	// legacy backfill (`kind: legacy`). Never populated from agent
	// JSON -- the context_report parser strips any agent-supplied
	// origin-shaped keys before the writer sees them.
	Origin *ArtifactOrigin `yaml:"origin,omitempty"`
	// SupersededAt is stamped by the re-run cleanup pass when a
	// step re-executes: the old entry stays on disk for audit
	// purposes but is filtered out of upstream scoping. Empty when
	// the entry is still live.
	SupersededAt string `yaml:"superseded_at,omitempty"`
}

type ArtifactsFile struct {
	SchemaVersion string          `yaml:"schema_version"`
	Artifacts     []ArtifactEntry `yaml:"artifacts"`
}

type TaskAgentState struct {
	Status       string   `yaml:"status"`
	CurrentTask  string   `yaml:"current_task"`
	Progress     string   `yaml:"progress,omitempty"`
	BlockedOn    string   `yaml:"blocked_on,omitempty"`
	Dependencies []string `yaml:"dependencies,omitempty"`
	Produces     []string `yaml:"produces,omitempty"`
	LastOutput   string   `yaml:"last_output"`
	LastUpdated  string   `yaml:"last_updated"`
}

type TaskStateFile struct {
	SchemaVersion string                    `yaml:"schema_version"`
	Feature       string                    `yaml:"feature"`
	Phase         string                    `yaml:"phase"`
	UpdatedAt     string                    `yaml:"updated_at"`
	Agents        map[string]TaskAgentState `yaml:"agents"`
}

type PlanFrontmatter struct {
	SchemaVersion string `yaml:"schema_version"`
	Feature       string `yaml:"feature"`
	Phase         string `yaml:"phase"`
	Owner         string `yaml:"owner"`
}

const changelogDisplayLimit = 15

// HydrateFeatureContext reads .context/features/{feature}/ from projectRoot
// and constructs a context injection string for guest agent prompts.
// Output is split into Recent Activity (timeline) and Current State (snapshot).
func HydrateFeatureContext(projectRoot, feature string) (string, error) {
	return hydrateFeatureContext(projectRoot, feature, nil)
}

// HydrateFeatureContextForStep is the step-scoped variant of
// HydrateFeatureContext. Artifacts whose IDs appear in focusIDs are
// rendered with full metadata (path, creator, metadata, consumers,
// depends_on) while every other artifact collapses to a single-line
// reference (plan decision D11). Used by the execute runner so a
// long-running feature with dozens of artifacts still keeps the
// current step's upstream visually distinct from the rest.
func HydrateFeatureContextForStep(projectRoot, feature string, focusIDs []string) (string, error) {
	set := make(map[string]struct{}, len(focusIDs))
	for _, id := range focusIDs {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	return hydrateFeatureContext(projectRoot, feature, set)
}

func hydrateFeatureContext(projectRoot, feature string, focus map[string]struct{}) (string, error) {
	if feature == "" {
		return "", nil
	}

	dir := filepath.Join(projectRoot, ".context", "features", feature)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", nil
	}

	var sb strings.Builder

	// Plan
	planPhase, planBody, err := readPlan(filepath.Join(dir, "plan.md"))
	if err == nil {
		fmt.Fprintf(&sb, "## Feature Context: %s\n", feature)
		if planPhase != "" {
			fmt.Fprintf(&sb, "Phase: %s\n", planPhase)
		}
		sb.WriteString("\n### Plan Summary\n")
		sb.WriteString(planBody)
		sb.WriteString("\n")
	}

	// Feature Files (plan decision D2). Listing the contents of
	// .context/features/<feature>/ lets a guest agent discover
	// auxiliary files the orchestrator hasn't explicitly rendered
	// (design notes, reference images, agent-authored research) and
	// request them via Read. Deterministic ordering + kilobyte-scale
	// sizing keeps the section stable across step runs so prompt
	// dumps diff cleanly.
	if files, err := listFeatureFiles(dir); err == nil && len(files) > 0 {
		sb.WriteString("\n### Feature Files (ls .context/features/")
		sb.WriteString(feature)
		sb.WriteString("/)\n")
		for _, f := range files {
			if f.IsDir {
				fmt.Fprintf(&sb, "- %s/\n", f.RelPath)
				continue
			}
			fmt.Fprintf(&sb, "- %s (%d bytes)\n", f.RelPath, f.Size)
		}
		sb.WriteString("\nRead any of these with the Read tool if you need more detail than the sections above surface.\n")
	}

	// Decisions
	decisions, err := readDecisions(filepath.Join(dir, "decisions.yaml"))
	if err == nil && len(decisions) > 0 {
		sb.WriteString("\n### Recent Decisions\n")
		for _, d := range decisions {
			fmt.Fprintf(&sb, "- [%s] %s (by %s): %s\n", d.Status, d.Decision, d.DecidedBy, d.Rationale)
		}
	}

	// --- Recent Activity (what changed) ---
	changelog, _ := readChangelog(filepath.Join(dir, "changelog.yaml"))
	if len(changelog) > 0 {
		sb.WriteString("\n### Recent Activity (what changed)\n")
		start := 0
		if len(changelog) > changelogDisplayLimit {
			start = len(changelog) - changelogDisplayLimit
		}
		for i := len(changelog) - 1; i >= start; i-- {
			e := changelog[i]
			line := fmt.Sprintf("- [%s] %s: %s", e.Timestamp, e.Agent, e.Summary)
			if e.ArtifactID != "" {
				line += fmt.Sprintf(" (%s)", e.ArtifactID)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	// --- Current State (what exists now) ---
	taskState, taskErr := readTaskState(filepath.Join(dir, "task-state.yaml"))
	artifacts, artErr := readArtifacts(filepath.Join(dir, "artifacts.yaml"))

	hasState := taskErr == nil && len(taskState.Agents) > 0
	hasArtifacts := artErr == nil && len(artifacts) > 0

	if hasState || hasArtifacts {
		sb.WriteString("\n### Current State (what exists now)\n")
	}

	if hasState {
		sb.WriteString("\n#### Agent Activity\n")
		for name, agent := range taskState.Agents {
			status := strings.ToUpper(agent.Status)
			fmt.Fprintf(&sb, "- %s: %s", name, status)
			if agent.CurrentTask != "" {
				fmt.Fprintf(&sb, " -- %s", agent.CurrentTask)
			}
			if agent.Progress != "" {
				fmt.Fprintf(&sb, " (%s)", agent.Progress)
			}
			sb.WriteString("\n")

			if agent.BlockedOn != "" {
				fmt.Fprintf(&sb, "  Blocked on: %s\n", agent.BlockedOn)
			}
			if len(agent.Produces) > 0 {
				fmt.Fprintf(&sb, "  Produces: %s\n", strings.Join(agent.Produces, ", "))
			}
			if len(agent.Dependencies) > 0 {
				fmt.Fprintf(&sb, "  Depends on: %s\n", strings.Join(agent.Dependencies, ", "))
			}
			if agent.Status == "idle" && agent.LastOutput != "" {
				fmt.Fprintf(&sb, "  Last: %s\n", agent.LastOutput)
			}
		}
	}

	if hasArtifacts {
		sb.WriteString("\n#### Available Artifacts\n")
		renderArtifactEntries(&sb, artifacts, focus)
	}

	return sb.String(), nil
}

// renderArtifactEntries writes the Available Artifacts block. When
// focus is empty or nil, every artifact renders in full detail
// (legacy behavior). When focus is non-empty, artifacts whose IDs are
// in the set render with the full metadata block and every other
// artifact collapses to a one-line reference so the prompt stays
// compact for long-running features (plan decision D11).
func renderArtifactEntries(sb *strings.Builder, artifacts []ArtifactEntry, focus map[string]struct{}) {
	scoped := len(focus) > 0
	for _, a := range artifacts {
		if scoped {
			if _, ok := focus[a.ID]; !ok {
				fmt.Fprintf(sb, "- %s (%s) [%s] -- %s\n", a.ID, a.Type, a.Status, trimToLen(a.Description, 80))
				continue
			}
		}
		fmt.Fprintf(sb, "- %s (%s) [%s] -- %s\n", a.ID, a.Type, a.Status, a.Description)
		fmt.Fprintf(sb, "  Path: %s\n", a.Path)
		if a.CreatedBy != "" {
			line := fmt.Sprintf("  By: %s", a.CreatedBy)
			if len(a.Metadata) > 0 {
				var parts []string
				for k, v := range a.Metadata {
					parts = append(parts, k+": "+v)
				}
				sort.Strings(parts)
				line += " | " + strings.Join(parts, ", ")
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		if len(a.Consumers) > 0 {
			fmt.Fprintf(sb, "  Needed by: %s\n", strings.Join(a.Consumers, ", "))
		}
		if len(a.DependsOn) > 0 {
			fmt.Fprintf(sb, "  Derived from: %s\n", strings.Join(a.DependsOn, ", "))
		}
	}
}

func trimToLen(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func readPlan(path string) (phase string, body string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	yamlBytes, bodyStr, err := splitPlanFrontmatter(data)
	if err != nil {
		return "", strings.TrimSpace(string(data)), nil
	}

	var fm PlanFrontmatter
	if err := yaml.Unmarshal(yamlBytes, &fm); err != nil {
		return "", bodyStr, nil
	}

	return fm.Phase, bodyStr, nil
}

func splitPlanFrontmatter(data []byte) (yamlBytes []byte, body string, err error) {
	content := bytes.TrimSpace(data)
	if !bytes.HasPrefix(content, []byte("---")) {
		return nil, "", fmt.Errorf("no frontmatter")
	}

	content = content[3:]
	if len(content) > 0 && content[0] == '\n' {
		content = content[1:]
	} else if len(content) > 1 && content[0] == '\r' && content[1] == '\n' {
		content = content[2:]
	}

	idx := bytes.Index(content, []byte("\n---"))
	if idx < 0 {
		return nil, "", fmt.Errorf("no closing delimiter")
	}

	yamlPart := content[:idx]
	rest := content[idx+4:]
	if len(rest) > 0 && rest[0] == '\r' {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	return bytes.TrimSpace(yamlPart), strings.TrimSpace(string(rest)), nil
}

func readDecisions(path string) ([]DecisionEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var df DecisionsFile
	if err := yaml.Unmarshal(data, &df); err != nil {
		return nil, fmt.Errorf("parsing decisions: %w", err)
	}
	var final []DecisionEntry
	for _, d := range df.Decisions {
		if d.Status == "final" {
			final = append(final, d)
		}
	}
	return final, nil
}

func readArtifacts(path string) ([]ArtifactEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var af ArtifactsFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		return nil, fmt.Errorf("parsing artifacts: %w", err)
	}
	// In-memory legacy backfill: readers downstream of this helper
	// (HydrateFeatureContext, Available Artifacts rendering, and the
	// upstream filter introduced in plan decision D11) treat
	// Origin==nil as an unknown-provenance hazard and skip such entries
	// from plan-scoped views. Stamping legacy origin here lets them
	// render unconditionally without needing to disambiguate "pre-PR
	// file we haven't touched yet" from "active write with missing
	// tag".
	backfillLegacyOrigins(af.Artifacts)
	var available []ArtifactEntry
	for _, a := range af.Artifacts {
		if a.Status == "rejected" {
			continue
		}
		if a.SupersededAt != "" {
			continue
		}
		available = append(available, a)
	}
	return available, nil
}

func readTaskState(path string) (TaskStateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TaskStateFile{}, err
	}
	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		return TaskStateFile{}, fmt.Errorf("parsing task-state: %w", err)
	}
	return ts, nil
}

// --- Changelog ---

type ChangelogEntry struct {
	ID         string   `yaml:"id"`
	Timestamp  string   `yaml:"timestamp"`
	Agent      string   `yaml:"agent"`
	Action     string   `yaml:"action"`
	Summary    string   `yaml:"summary"`
	ArtifactID string   `yaml:"artifact_id,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
}

type ChangelogFile struct {
	SchemaVersion string           `yaml:"schema_version"`
	Entries       []ChangelogEntry `yaml:"entries"`
}

// featureFileEntry describes a single item inside
// .context/features/<feature>/ as rendered by HydrateFeatureContext.
// Kept deliberately flat so the Feature Files section serializes
// deterministically (filepath.Walk traversal with path-sorted output).
type featureFileEntry struct {
	RelPath string
	Size    int64
	IsDir   bool
}

// listFeatureFiles walks the feature directory one level deep (plus
// shallow subdirectory listings) and returns a sorted slice ready for
// rendering. Large files are still listed by size so agents know what
// to expect before calling Read; the section body in
// HydrateFeatureContext does not embed their contents.
//
// The traversal caps at featureFilesMax entries so a feature with
// thousands of generated sprites doesn't blow up the prompt. Remaining
// items are surfaced via an "... (N more)" footer in the caller.
func listFeatureFiles(dir string) ([]featureFileEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]featureFileEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		if e.IsDir() {
			out = append(out, featureFileEntry{RelPath: name, IsDir: true})
			sub, err := os.ReadDir(full)
			if err != nil {
				continue
			}
			for _, se := range sub {
				if strings.HasPrefix(se.Name(), ".") {
					continue
				}
				subInfo, err := se.Info()
				if err != nil {
					continue
				}
				rel := filepath.ToSlash(filepath.Join(name, se.Name()))
				if se.IsDir() {
					out = append(out, featureFileEntry{RelPath: rel + "/", IsDir: true})
					continue
				}
				out = append(out, featureFileEntry{RelPath: rel, Size: subInfo.Size()})
			}
			continue
		}
		out = append(out, featureFileEntry{RelPath: name, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, nil
}

func readChangelog(path string) ([]ChangelogEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf ChangelogFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing changelog: %w", err)
	}
	return cf.Entries, nil
}

// --- Metadata Key Registry ---

type MetadataKeyDef struct {
	Key         string `yaml:"key"`
	Description string `yaml:"description"`
}

type MetadataKeysFile struct {
	SchemaVersion string                      `yaml:"schema_version"`
	Types         map[string][]MetadataKeyDef `yaml:"types"`
}

// ReadMetadataKeys reads .context/features/{feature}/metadata-keys.yaml and
// returns the canonical key definitions per artifact type. Returns an empty
// map (no error) if the file does not exist.
func ReadMetadataKeys(projectRoot, feature string) (map[string][]MetadataKeyDef, error) {
	if feature == "" {
		return nil, nil
	}
	path := filepath.Join(projectRoot, ".context", "features", feature, "metadata-keys.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var mk MetadataKeysFile
	if err := yaml.Unmarshal(data, &mk); err != nil {
		return nil, fmt.Errorf("parsing metadata-keys: %w", err)
	}
	return mk.Types, nil
}
