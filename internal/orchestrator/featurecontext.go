package orchestrator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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
		for _, a := range artifacts {
			fmt.Fprintf(&sb, "- %s (%s) [%s] -- %s\n", a.ID, a.Type, a.Status, a.Description)
			fmt.Fprintf(&sb, "  Path: %s\n", a.Path)
			if a.CreatedBy != "" {
				line := fmt.Sprintf("  By: %s", a.CreatedBy)
				if len(a.Metadata) > 0 {
					var parts []string
					for k, v := range a.Metadata {
						parts = append(parts, k+": "+v)
					}
					line += " | " + strings.Join(parts, ", ")
				}
				sb.WriteString(line)
				sb.WriteString("\n")
			}
			if len(a.Consumers) > 0 {
				fmt.Fprintf(&sb, "  Needed by: %s\n", strings.Join(a.Consumers, ", "))
			}
			if len(a.DependsOn) > 0 {
				fmt.Fprintf(&sb, "  Derived from: %s\n", strings.Join(a.DependsOn, ", "))
			}
		}
	}

	return sb.String(), nil
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
	var available []ArtifactEntry
	for _, a := range af.Artifacts {
		if a.Status != "rejected" {
			available = append(available, a)
		}
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
