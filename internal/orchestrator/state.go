package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rauriemo/anthem/internal/cost"
)

// OrchestratorState is the JSON-serializable snapshot persisted to state.json.
type OrchestratorState struct {
	Version      int                    `json:"version"`
	SavedAt      time.Time              `json:"saved_at"`
	RetryState   map[string]*RetryState `json:"retry_state,omitempty"`
	CostSessions []cost.SessionCost     `json:"cost_sessions,omitempty"`
}

// RetryState is the JSON-serializable form of RetryInfo.
type RetryState struct {
	TaskID      string    `json:"task_id"`
	Attempts    int       `json:"attempts"`
	NextRetryAt time.Time `json:"next_retry_at"`
	LastError   string    `json:"last_error"`
}

const stateVersion = 1

// DefaultStatePath returns ~/.anthem/state.json.
func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".anthem", "state.json"), nil
}

// SaveState writes the orchestrator's current state to the given path.
// Uses atomic write (write to temp file, then rename) to avoid corruption.
func SaveState(path string, retryState map[string]*RetryInfo, costTracker *cost.Tracker) error {
	state := OrchestratorState{
		Version:      stateVersion,
		SavedAt:      time.Now(),
		RetryState:   make(map[string]*RetryState, len(retryState)),
		CostSessions: costTracker.Sessions(),
	}

	for id, ri := range retryState {
		state.RetryState[id] = &RetryState{
			TaskID:      ri.TaskID,
			Attempts:    ri.Attempts,
			NextRetryAt: ri.NextRetryAt,
			LastError:   ri.LastError,
		}
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("writing temp state file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming state file: %w", err)
	}

	return nil
}

// LoadState reads the orchestrator state from the given path.
// Returns a zero-value state (not an error) if the file does not exist.
func LoadState(path string) (*OrchestratorState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &OrchestratorState{Version: stateVersion}, nil
		}
		return nil, fmt.Errorf("reading state file: %w", err)
	}

	var state OrchestratorState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing state file: %w", err)
	}

	if state.Version > stateVersion {
		return nil, fmt.Errorf("state file version %d is newer than supported version %d", state.Version, stateVersion)
	}

	return &state, nil
}
