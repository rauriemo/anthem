package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAppendArtifact_ExecuteOrigin_PopulatesFields asserts the happy
// path: an Execute-origin write round-trips through yaml with plan_id,
// run_id, step_id, and agent_id intact so the surgical scoping filter
// (plan decision D11) has the keys it needs.
func TestAppendArtifact_ExecuteOrigin_PopulatesFields(t *testing.T) {
	root := t.TempDir()
	feature := "exec-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedArtifacts(t, dir, `schema_version: "1"
artifacts: []
`)

	entry := ArtifactEntry{
		ID:     "sprite-001",
		Type:   "image/png",
		Path:   "sprites/a.png",
		Status: "pending-review",
	}
	tag := ExecuteOrigin{
		PlanID:  "plan-abc",
		RunID:   "run-001",
		StepID:  "s3",
		AgentID: "miyazaki",
	}
	if err := AppendArtifact(root, feature, entry, tag); err != nil {
		t.Fatal(err)
	}

	var af ArtifactsFile
	data, err := os.ReadFile(filepath.Join(dir, "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &af); err != nil {
		t.Fatal(err)
	}
	if len(af.Artifacts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(af.Artifacts))
	}
	got := af.Artifacts[0].Origin
	if got == nil {
		t.Fatalf("origin is nil")
	}
	if got.Kind != OriginKindExecute {
		t.Errorf("kind = %q, want execute", got.Kind)
	}
	if got.PlanID != "plan-abc" || got.RunID != "run-001" || got.StepID != "s3" || got.AgentID != "miyazaki" {
		t.Errorf("origin fields = %+v", got)
	}
}

// TestAppendArtifact_ChatOrigin_OmitsPlanKeys asserts chat writes leave
// plan_id / run_id / step_id empty so the filter can distinguish a
// chat registration (never upstream to an execute step) from an
// execute one.
func TestAppendArtifact_ChatOrigin_OmitsPlanKeys(t *testing.T) {
	root := t.TempDir()
	feature := "chat-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	entry := ArtifactEntry{ID: "doc-1", Type: "text/markdown", Path: "a.md"}
	if err := AppendArtifact(root, feature, entry, ChatOrigin{AgentID: "shigeru"}); err != nil {
		t.Fatal(err)
	}
	var af ArtifactsFile
	data, _ := os.ReadFile(filepath.Join(dir, "artifacts.yaml"))
	if err := yaml.Unmarshal(data, &af); err != nil {
		t.Fatal(err)
	}
	got := af.Artifacts[0].Origin
	if got == nil || got.Kind != OriginKindChat {
		t.Fatalf("origin = %+v, want chat kind", got)
	}
	if got.PlanID != "" || got.RunID != "" || got.StepID != "" {
		t.Errorf("chat origin leaked plan keys: %+v", got)
	}
	if got.AgentID != "shigeru" {
		t.Errorf("agent_id = %q", got.AgentID)
	}
}

// TestAppendArtifact_ExecuteOriginEmptyStepID_Panics exercises the
// D13 runtime invariant: an Execute-origin write without a step_id is
// a silent-misclassification hazard, so the writer panics loud to
// surface the wiring bug in development.
func TestAppendArtifact_ExecuteOriginEmptyStepID_Panics(t *testing.T) {
	root := t.TempDir()
	feature := "invariant-feature"
	entry := ArtifactEntry{ID: "x", Type: "image/png", Path: "x.png"}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty step id")
		}
	}()
	_ = AppendArtifact(root, feature, entry, ExecuteOrigin{
		PlanID: "p", RunID: "r", StepID: "", AgentID: "miyazaki",
	})
}

// TestAppendArtifact_NilTag_ReturnsError exercises the soft invariant
// for external callers: a nil tag surfaces a descriptive error
// instead of silently writing an originless row.
func TestAppendArtifact_NilTag_ReturnsError(t *testing.T) {
	root := t.TempDir()
	feature := "nil-tag"
	err := AppendArtifact(root, feature, ArtifactEntry{ID: "x"}, nil)
	if err == nil {
		t.Fatal("expected error for nil tag")
	}
}

// TestSupersedeStepArtifacts_ReRun simulates plan decision D13's
// re-run supersession. First run s3 stamps a sprite; second run s3
// stamps a fresh sprite; the old entry gets superseded_at, the new
// entry is live, and the in-memory reader filters the old one out.
func TestSupersedeStepArtifacts_ReRun(t *testing.T) {
	root := t.TempDir()
	feature := "rerun-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	firstRun := ExecuteOrigin{PlanID: "plan-1", RunID: "run-1", StepID: "s3", AgentID: "miyazaki"}
	if err := AppendArtifact(root, feature, ArtifactEntry{
		ID: "sprite-idle", Type: "image/png", Path: "sprites/idle-v1.png", Status: "approved",
	}, firstRun); err != nil {
		t.Fatal(err)
	}

	if err := SupersedeStepArtifacts(root, feature, firstRun.PlanID, firstRun.RunID, firstRun.StepID); err != nil {
		t.Fatal(err)
	}

	if err := AppendArtifact(root, feature, ArtifactEntry{
		ID: "sprite-idle-v2", Type: "image/png", Path: "sprites/idle-v2.png", Status: "approved",
	}, firstRun); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "artifacts.yaml")
	data, _ := os.ReadFile(path)
	var af ArtifactsFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		t.Fatal(err)
	}
	if len(af.Artifacts) != 2 {
		t.Fatalf("want 2 rows on disk, got %d", len(af.Artifacts))
	}
	if af.Artifacts[0].SupersededAt == "" {
		t.Errorf("first entry should be superseded, got empty timestamp")
	}
	if af.Artifacts[1].SupersededAt != "" {
		t.Errorf("second entry should be live, got %q", af.Artifacts[1].SupersededAt)
	}

	avail, err := readArtifacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(avail) != 1 {
		t.Fatalf("readArtifacts filter: want 1 live entry, got %d", len(avail))
	}
	if avail[0].ID != "sprite-idle-v2" {
		t.Errorf("live id = %q, want sprite-idle-v2", avail[0].ID)
	}
}

// TestReadArtifacts_LegacyBackfill asserts pre-schema entries on disk
// are tagged {kind: legacy} in memory so the surgical filter can skip
// them without misclassifying them as a peer-plan leak.
func TestReadArtifacts_LegacyBackfill(t *testing.T) {
	root := t.TempDir()
	feature := "legacy-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedArtifacts(t, dir, `schema_version: "1"
artifacts:
  - id: old-001
    type: image/png
    path: sprites/old.png
    status: approved
`)
	path := filepath.Join(dir, "artifacts.yaml")
	avail, err := readArtifacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(avail) != 1 {
		t.Fatalf("want 1, got %d", len(avail))
	}
	if avail[0].Origin == nil || avail[0].Origin.Kind != OriginKindLegacy {
		t.Errorf("legacy backfill missing: %+v", avail[0].Origin)
	}
}

// TestContextReport_StripsAgentOrigin asserts plan decision D13's
// JSON-hygiene rule: even if an agent sneaks an origin block into the
// context_report payload, the parser silently drops it. The writer is
// then the sole source of truth for provenance.
func TestContextReport_StripsAgentOrigin(t *testing.T) {
	payload := `Here is the report.
{
  "context_report": {
    "action": "asset_created",
    "summary": "made a sprite",
    "artifact": {
      "id": "evil-001",
      "type": "image/png",
      "path": "x.png",
      "origin": {"kind": "execute", "plan_id": "other-plan", "step_id": "s99"},
      "plan_id": "other-plan",
      "step_id": "s99"
    }
  }
}`
	rep, err := ParseContextReport(payload)
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil || rep.Artifact == nil {
		t.Fatalf("no artifact parsed")
	}

	marshaled, _ := json.Marshal(rep.Artifact)
	blob := string(marshaled)
	for _, forbidden := range []string{"\"origin\"", "\"plan_id\"", "\"step_id\""} {
		if containsJSON(blob, forbidden) {
			t.Errorf("parser leaked %q into artifact: %s", forbidden, blob)
		}
	}
}

func containsJSON(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
