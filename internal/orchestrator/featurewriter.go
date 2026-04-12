package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	featureMus   = make(map[string]*sync.Mutex)
	featureMusMu sync.Mutex
)

func featureLock(featurePath string) *sync.Mutex {
	featureMusMu.Lock()
	defer featureMusMu.Unlock()
	mu, ok := featureMus[featurePath]
	if !ok {
		mu = &sync.Mutex{}
		featureMus[featurePath] = mu
	}
	return mu
}

func featureDir(projectRoot, feature string) string {
	return filepath.Join(projectRoot, ".context", "features", feature)
}

// TaskStateUpdate holds optional rich fields for UpdateTaskState.
type TaskStateUpdate struct {
	Progress  string
	BlockedOn string
	Produces  []string
}

// UpdateTaskState sets an agent's status and last_output in task-state.yaml.
// Writes are serialized per feature path via featureLock.
func UpdateTaskState(projectRoot, feature, agentName, status, lastOutput string, opts ...TaskStateUpdate) error {
	dir := featureDir(projectRoot, feature)
	path := filepath.Join(dir, "task-state.yaml")

	mu := featureLock(dir)
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading task-state: %w", err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		return fmt.Errorf("parsing task-state: %w", err)
	}

	if ts.Agents == nil {
		ts.Agents = make(map[string]TaskAgentState)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	agent := TaskAgentState{
		Status:      status,
		CurrentTask: "",
		LastOutput:  lastOutput,
		LastUpdated: now,
	}
	if len(opts) > 0 {
		agent.Progress = opts[0].Progress
		agent.BlockedOn = opts[0].BlockedOn
		agent.Produces = opts[0].Produces
	}
	ts.Agents[agentName] = agent
	ts.UpdatedAt = now

	return writeYAML(path, &ts)
}

// TaskActiveOpts holds optional fields for SetTaskActive.
type TaskActiveOpts struct {
	Dependencies []string
	Produces     []string
}

// SetTaskActive marks an agent as active with the given task description.
func SetTaskActive(projectRoot, feature, agentName, taskDescription string, opts ...TaskActiveOpts) error {
	dir := featureDir(projectRoot, feature)
	path := filepath.Join(dir, "task-state.yaml")

	mu := featureLock(dir)
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading task-state: %w", err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		return fmt.Errorf("parsing task-state: %w", err)
	}

	if ts.Agents == nil {
		ts.Agents = make(map[string]TaskAgentState)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	agent := ts.Agents[agentName]
	agent.Status = "active"
	agent.CurrentTask = taskDescription
	agent.LastUpdated = now
	if len(opts) > 0 {
		agent.Dependencies = opts[0].Dependencies
		agent.Produces = opts[0].Produces
	}
	ts.Agents[agentName] = agent
	ts.UpdatedAt = now

	return writeYAML(path, &ts)
}

// AppendArtifact adds an entry to artifacts.yaml for the given feature.
// Writes are serialized per feature path via featureLock.
func AppendArtifact(projectRoot, feature string, entry ArtifactEntry) error {
	dir := featureDir(projectRoot, feature)
	path := filepath.Join(dir, "artifacts.yaml")

	mu := featureLock(dir)
	mu.Lock()
	defer mu.Unlock()

	var af ArtifactsFile
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading artifacts: %w", err)
	}
	if err == nil {
		if err := yaml.Unmarshal(data, &af); err != nil {
			return fmt.Errorf("parsing artifacts: %w", err)
		}
	}

	if af.SchemaVersion == "" {
		af.SchemaVersion = "1"
	}

	af.Artifacts = append(af.Artifacts, entry)
	return writeYAML(path, &af)
}

// AppendArtifactValidated adds an entry to artifacts.yaml and logs a warning
// for any metadata keys not in the registry for the entry's artifact type.
func AppendArtifactValidated(projectRoot, feature string, entry ArtifactEntry, knownKeys map[string][]MetadataKeyDef, logger *slog.Logger) error {
	if len(entry.Metadata) > 0 && knownKeys != nil && logger != nil {
		typeName := strings.SplitN(entry.Type, "/", 2)[0]
		defs := knownKeys[typeName]
		known := make(map[string]struct{}, len(defs))
		for _, d := range defs {
			known[d.Key] = struct{}{}
		}
		for k := range entry.Metadata {
			if _, ok := known[k]; !ok {
				logger.Warn("unknown metadata key for artifact type",
					"key", k, "artifact_type", typeName, "artifact_id", entry.ID)
			}
		}
	}
	return AppendArtifact(projectRoot, feature, entry)
}

// AppendChangelog adds a changelog entry to changelog.yaml for the given feature.
// The entry ID is auto-generated as log-{N} where N is the next sequential number.
func AppendChangelog(projectRoot, feature string, entry ChangelogEntry) error {
	dir := featureDir(projectRoot, feature)
	path := filepath.Join(dir, "changelog.yaml")

	mu := featureLock(dir)
	mu.Lock()
	defer mu.Unlock()

	var cf ChangelogFile
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading changelog: %w", err)
	}
	if err == nil {
		if err := yaml.Unmarshal(data, &cf); err != nil {
			return fmt.Errorf("parsing changelog: %w", err)
		}
	}

	if cf.SchemaVersion == "" {
		cf.SchemaVersion = "1"
	}

	if entry.ID == "" {
		entry.ID = fmt.Sprintf("log-%03d", len(cf.Entries)+1)
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	cf.Entries = append(cf.Entries, entry)
	return writeYAML(path, &cf)
}

// UpdateTaskProgress updates an agent's progress and blocked_on without changing status.
func UpdateTaskProgress(projectRoot, feature, agentName, progress, blockedOn string) error {
	dir := featureDir(projectRoot, feature)
	path := filepath.Join(dir, "task-state.yaml")

	mu := featureLock(dir)
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading task-state: %w", err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		return fmt.Errorf("parsing task-state: %w", err)
	}

	if ts.Agents == nil {
		ts.Agents = make(map[string]TaskAgentState)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	agent := ts.Agents[agentName]
	if progress != "" {
		agent.Progress = progress
	}
	agent.BlockedOn = blockedOn
	agent.LastUpdated = now
	ts.Agents[agentName] = agent
	ts.UpdatedAt = now

	return writeYAML(path, &ts)
}

// SanitizeSavePath validates that rawPath resolves within projectRoot.
// Returns the cleaned absolute path or an error if traversal is detected.
func SanitizeSavePath(projectRoot, rawPath string) (string, error) {
	if filepath.IsAbs(rawPath) {
		return "", fmt.Errorf("absolute paths not allowed: %s", rawPath)
	}

	cleaned := filepath.Clean(rawPath)
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("path traversal not allowed: %s", rawPath)
	}

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolving project root: %w", err)
	}

	full := filepath.Join(absRoot, cleaned)
	if !strings.HasPrefix(full, absRoot+string(filepath.Separator)) && full != absRoot {
		return "", fmt.Errorf("path escapes project root: %s", rawPath)
	}

	return full, nil
}

func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func writeYAML(path string, v any) error {
	out, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling YAML: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming to %s: %w", path, err)
	}

	return nil
}
