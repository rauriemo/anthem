package orchestrator

import (
	"fmt"
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

// UpdateTaskState sets an agent's status and last_output in task-state.yaml.
// Writes are serialized per feature path via featureLock.
func UpdateTaskState(projectRoot, feature, agentName, status, lastOutput string) error {
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
	ts.Agents[agentName] = TaskAgentState{
		Status:      status,
		CurrentTask: "",
		LastOutput:  lastOutput,
		LastUpdated: now,
	}
	ts.UpdatedAt = now

	return writeYAML(path, &ts)
}

// SetTaskActive marks an agent as active with the given task description.
func SetTaskActive(projectRoot, feature, agentName, taskDescription string) error {
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
