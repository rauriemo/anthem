package plans

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrCompiledSidecarDrift signals that the compiled JSON sidecar on disk is
// out of sync with the plan's frontmatter — for example, because the
// frontmatter bump succeeded but the tmp->final atomic rename failed
// afterwards, or the markdown was rolled back via git while the sidecar
// stayed behind. Callers should translate this into a "please recompile"
// prompt to the user rather than dispatching a stale plan.
var ErrCompiledSidecarDrift = errors.New("plans: compiled sidecar is out of sync with plan frontmatter")

// compiledSidecarPath returns the on-disk location of the compiled
// ExecutionPlan JSON that sits adjacent to a plan's markdown file. This is the
// sole source of truth for compiled bodies — no parallel store is introduced.
func compiledSidecarPath(planPath string) string {
	return planPath + ".execute.json"
}

// CompiledSidecarPath returns the path where a compiled ExecutionPlan JSON
// would live for the given markdown plan path. Exported so callers outside
// this package can check for sidecar presence without reaching into the
// filesystem convention.
func CompiledSidecarPath(planPath string) string {
	return compiledSidecarPath(planPath)
}

// SaveCompiled persists a compiled ExecutionPlan JSON body adjacent to a
// markdown plan and bumps the plan's frontmatter CompileGeneration/CompiledAt/
// RequiredProfiles in lock-step. The returned newGen is the frontmatter's
// generation after the successful bump.
//
// Write sequence (atomic within the limits of the OS):
//  1. Body is written to <planPath>.execute.json.tmp.
//  2. Frontmatter is updated (CompileGeneration++, CompiledAt=now,
//     RequiredProfiles=snapshot). On failure here the tmp sidecar is removed
//     and the plan's on-disk state is unchanged; newGen is zero and err is
//     returned.
//  3. The tmp file is renamed over <planPath>.execute.json. On rename failure
//     the frontmatter now advertises gen=N+1 while the on-disk sidecar is
//     either absent or still at gen=N; LoadCompiled detects this and returns
//     ErrCompiledSidecarDrift. Callers surface "please recompile" at that
//     point rather than dispatching a stale plan.
//
// The caller is expected to have already embedded PlanGeneration=newGen in
// the JSON body; that is the wire-level drift detector LoadCompiled relies on.
// SaveCompiled does not rewrite the JSON to backfill it — doing so would
// corrupt any digital signatures or canonical encodings the caller relied on.
func (s *Store) SaveCompiled(planPath string, body []byte, requiredProfiles []string) (int, error) {
	plan, err := s.Load(planPath)
	if err != nil {
		return 0, fmt.Errorf("plans: load for compile: %w", err)
	}

	tmpPath := compiledSidecarPath(planPath) + ".tmp"
	if err := os.WriteFile(tmpPath, body, 0o644); err != nil {
		return 0, fmt.Errorf("plans: write tmp sidecar: %w", err)
	}

	fm := plan.Frontmatter
	fm.CompileGeneration++
	fm.CompiledAt = time.Now().UTC()
	fm.RequiredProfiles = append([]string(nil), requiredProfiles...)
	fm.Updated = fm.CompiledAt

	if err := writePlan(planPath, fm, plan.Body); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("plans: bump frontmatter for compile: %w", err)
	}

	finalPath := compiledSidecarPath(planPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Frontmatter has already bumped, so the system is in a drift state.
		// Leave the tmp in place for post-mortem and return a clear error;
		// LoadCompiled will also detect this via the embedded generation.
		return fm.CompileGeneration, fmt.Errorf("plans: rename sidecar after frontmatter bump: %w", err)
	}
	return fm.CompileGeneration, nil
}

// LoadCompiled reads the compiled ExecutionPlan JSON for a plan and returns
// (body, embeddedGeneration, error). Callers compare embeddedGeneration
// against Plan.Frontmatter.CompileGeneration to detect drift introduced by a
// partial SaveCompiled (rename failure after frontmatter bump) or by manual
// rollback (e.g. git checkout of the markdown).
//
// When the sidecar is missing and frontmatter says there has never been a
// compile (CompileGeneration == 0), os.ErrNotExist is returned untouched so
// callers can distinguish "never compiled" from "compiled but drifted."
//
// When the embedded generation differs from the frontmatter generation,
// ErrCompiledSidecarDrift is returned along with the body and generation so
// the caller can surface a specific message without having to re-read.
func (s *Store) LoadCompiled(planPath string) ([]byte, int, error) {
	plan, err := s.Load(planPath)
	if err != nil {
		return nil, 0, fmt.Errorf("plans: load for compiled sidecar: %w", err)
	}

	body, err := os.ReadFile(compiledSidecarPath(planPath))
	if err != nil {
		return nil, 0, err
	}

	var probe struct {
		Metadata struct {
			PlanGeneration int `json:"plan_generation"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return body, 0, fmt.Errorf("plans: parse compiled sidecar generation: %w", err)
	}

	embedded := probe.Metadata.PlanGeneration
	if embedded != plan.Frontmatter.CompileGeneration {
		return body, embedded, ErrCompiledSidecarDrift
	}
	return body, embedded, nil
}
