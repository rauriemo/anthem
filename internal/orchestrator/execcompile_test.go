package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/execute"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/types"
)

// --- parseExecPlanGate / execPlanGateRe ---

func TestParseExecPlanGate_ExtractsFields(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		action  execute.GateAction
		planID  string
		gen     int
		feedMsg string
	}{
		{
			name:   "approve minimal",
			text:   "[gate:approve:execplan:abc-123:2]",
			action: execute.GateApprove,
			planID: "abc-123",
			gen:    2,
		},
		{
			name:    "revise with feedback",
			text:    "[gate:revise:execplan:pid:5] please add a step for QA",
			action:  execute.GateRevise,
			planID:  "pid",
			gen:     5,
			feedMsg: "please add a step for QA",
		},
		{
			name:   "abort",
			text:   "[gate:abort:execplan:xyz:0]",
			action: execute.GateAbort,
			planID: "xyz",
			gen:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isExecPlanGateAction(tc.text) {
				t.Fatalf("isExecPlanGateAction false for %q", tc.text)
			}
			got, ok := parseExecPlanGate(tc.text)
			if !ok {
				t.Fatalf("parseExecPlanGate false for %q", tc.text)
			}
			if got.Action != tc.action {
				t.Errorf("action = %q, want %q", got.Action, tc.action)
			}
			if got.PlanID != tc.planID {
				t.Errorf("planID = %q, want %q", got.PlanID, tc.planID)
			}
			if got.CompileGeneration != tc.gen {
				t.Errorf("gen = %d, want %d", got.CompileGeneration, tc.gen)
			}
			if got.Feedback != tc.feedMsg {
				t.Errorf("feedback = %q, want %q", got.Feedback, tc.feedMsg)
			}
		})
	}
}

func TestParseExecPlanGate_IgnoresStepGate(t *testing.T) {
	if isExecPlanGateAction("[gate:approve]") {
		t.Error("bare step-level gate should not match execplan regex")
	}
	if _, ok := parseExecPlanGate("[gate:approve]"); ok {
		t.Error("parseExecPlanGate accepted step-level gate")
	}
}

// TestParseExecPlanGate_SkipList pins the :skip= suffix that Prism emits
// when the user has toggled approval gates OFF in the preview. Order is
// preserved and duplicates / empty tokens are dropped so audit logs stay
// readable. Fixtures mirror the events_test.go style (input string -> want
// struct) so the wire format can't drift silently.
func TestParseExecPlanGate_SkipList(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		action  execute.GateAction
		planID  string
		gen     int
		skipped []string
	}{
		{
			name:    "approve with one skip",
			text:    "[gate:approve:execplan:abc:2:skip=g1]",
			action:  execute.GateApprove,
			planID:  "abc",
			gen:     2,
			skipped: []string{"g1"},
		},
		{
			name:    "approve with multiple skips",
			text:    "[gate:approve:execplan:abc:2:skip=g1,g2,g5]",
			action:  execute.GateApprove,
			planID:  "abc",
			gen:     2,
			skipped: []string{"g1", "g2", "g5"},
		},
		{
			name:    "approve with trailing comma and duplicates",
			text:    "[gate:approve:execplan:abc:2:skip=g1,,g2,g1,]",
			action:  execute.GateApprove,
			planID:  "abc",
			gen:     2,
			skipped: []string{"g1", "g2"},
		},
		{
			name:    "approve with empty skip list stays legal",
			text:    "[gate:approve:execplan:abc:2:skip=]",
			action:  execute.GateApprove,
			planID:  "abc",
			gen:     2,
			skipped: nil,
		},
		{
			name:    "no skip suffix preserves backward compat",
			text:    "[gate:approve:execplan:abc:2]",
			action:  execute.GateApprove,
			planID:  "abc",
			gen:     2,
			skipped: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isExecPlanGateAction(tc.text) {
				t.Fatalf("isExecPlanGateAction false for %q", tc.text)
			}
			got, ok := parseExecPlanGate(tc.text)
			if !ok {
				t.Fatalf("parseExecPlanGate false for %q", tc.text)
			}
			if got.Action != tc.action || got.PlanID != tc.planID || got.CompileGeneration != tc.gen {
				t.Errorf("head mismatch: got action=%q plan=%q gen=%d, want %q/%q/%d",
					got.Action, got.PlanID, got.CompileGeneration, tc.action, tc.planID, tc.gen)
			}
			if len(got.SkippedGateIDs) != len(tc.skipped) {
				t.Fatalf("skipped len = %d (%v), want %d (%v)",
					len(got.SkippedGateIDs), got.SkippedGateIDs, len(tc.skipped), tc.skipped)
			}
			for i, id := range tc.skipped {
				if got.SkippedGateIDs[i] != id {
					t.Errorf("skipped[%d] = %q, want %q", i, got.SkippedGateIDs[i], id)
				}
			}
		})
	}
}

// TestFilterExecPlanGates covers the dispatch-time filter. Unknown IDs are
// ignored (idempotent between preview and dispatch), and order of the
// surviving gates is preserved so downstream (PlanRunner) observes the
// same sequence it would have without any skips.
func TestFilterExecPlanGates(t *testing.T) {
	gates := []execute.ApprovalGate{
		{ID: "g1", AfterStep: "s1", Prompt: "first"},
		{ID: "g2", AfterStep: "s2", Prompt: "second"},
		{ID: "g3", AfterStep: "s3", Prompt: "third"},
	}
	t.Run("empty skip list returns input unchanged", func(t *testing.T) {
		got := filterExecPlanGates(gates, nil)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
	})
	t.Run("skips one gate and preserves order", func(t *testing.T) {
		got := filterExecPlanGates(gates, []string{"g2"})
		if len(got) != 2 || got[0].ID != "g1" || got[1].ID != "g3" {
			t.Errorf("unexpected filter output: %+v", got)
		}
	})
	t.Run("unknown IDs are silently ignored", func(t *testing.T) {
		got := filterExecPlanGates(gates, []string{"ghost", "g1"})
		if len(got) != 2 || got[0].ID != "g2" || got[1].ID != "g3" {
			t.Errorf("unexpected filter output: %+v", got)
		}
	})
	t.Run("skipping all yields empty slice (not nil)", func(t *testing.T) {
		got := filterExecPlanGates(gates, []string{"g1", "g2", "g3"})
		if got == nil {
			t.Fatal("returned nil, want empty non-nil slice for deterministic JSON")
		}
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})
}

// --- requiredProfilesFromPlan ---

func TestRequiredProfilesFromPlan_UniqueSorted(t *testing.T) {
	plan := &execute.ExecutionPlan{
		Steps: []execute.PlanStep{
			{ID: "s3", AgentID: "artist"},
			{ID: "s1", AgentID: "animator"},
			{ID: "s2", AgentID: "artist"},
		},
	}
	got := requiredProfilesFromPlan(plan)
	want := []string{"animator", "artist"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- extractCompileJSON ---

func TestExtractCompileJSON_HandlesBareAndWrapped(t *testing.T) {
	cases := map[string]string{
		"bare":   `{"steps":[],"metadata":{}}`,
		"prefix": "Thinking... {\"steps\":[],\"metadata\":{}}",
		"suffix": "{\"steps\":[],\"metadata\":{}}\nTrailing text ignored",
		"nested": `{"a":{"b":1},"c":2}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := extractCompileJSON(in)
			if err != nil {
				t.Fatalf("%v", err)
			}
			var sink any
			if err := json.Unmarshal([]byte(out), &sink); err != nil {
				t.Errorf("extracted JSON did not parse: %v (%q)", err, out)
			}
		})
	}
}

func TestExtractCompileJSON_FailsWithoutObject(t *testing.T) {
	if _, err := extractCompileJSON("no json here"); err == nil {
		t.Error("expected error on output with no JSON object")
	}
}

// --- ConsultCompilePlan: agent-picked gates, missing-profile, ID preservation ---

// compileRunner builds an agent.MockRunner whose single Run call replays a
// canned compiler response. We match by the presence of "Compile Mode" in
// the prompt since ConsultCompilePlan is a one-shot call.
func compileRunner(respBody string) *agent.MockRunner {
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if !strings.Contains(opts.Prompt, "Compile Mode") {
			return nil, fmt.Errorf("unexpected prompt: %s", opts.Prompt)
		}
		return &types.RunResult{SessionID: "compile-s", Output: respBody}, nil
	}
	return r
}

func TestConsultCompilePlan_HappyPath(t *testing.T) {
	out := `{
	  "steps": [
	    {"id":"s1","agent_id":"artist","description":"draw sprites"},
	    {"id":"s2","agent_id":"animator","description":"animate","depends_on":"s1"}
	  ],
	  "gates": [
	    {"id":"g1","after_step":"s1","prompt":"Review sprite batch"}
	  ],
	  "metadata": {"title":"Scene X","description":"test"}
	}`
	ag := NewOrchestratorAgent(compileRunner(out), "", "", 100000, 10, 25, 10, 5, testLogger())

	res, err := ag.ConsultCompilePlan(context.Background(), CompilePlanInput{
		MarkdownPlan: "# Plan\n\nDo stuff.",
		ActiveGuests: []GuestProfile{{ID: "artist"}, {ID: "animator"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.MissingProfile != "" {
		t.Fatalf("unexpected missing profile: %q", res.MissingProfile)
	}
	if res.Plan == nil {
		t.Fatal("expected plan, got nil")
	}
	if len(res.Plan.Steps) != 2 || len(res.Plan.Gates) != 1 {
		t.Errorf("unexpected shape: steps=%d gates=%d", len(res.Plan.Steps), len(res.Plan.Gates))
	}
	for _, s := range res.Plan.Steps {
		if s.Status != execute.StepPending {
			t.Errorf("step %q status = %q, want pending", s.ID, s.Status)
		}
	}
}

func TestConsultCompilePlan_MissingProfile(t *testing.T) {
	ag := NewOrchestratorAgent(
		compileRunner(`{"error":"missing profile: animator"}`),
		"", "", 100000, 10, 25, 10, 5, testLogger(),
	)
	res, err := ag.ConsultCompilePlan(context.Background(), CompilePlanInput{
		MarkdownPlan: "# X",
		ActiveGuests: []GuestProfile{{ID: "artist"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.MissingProfile != "animator" {
		t.Errorf("missing profile = %q, want animator", res.MissingProfile)
	}
	if res.Plan != nil {
		t.Error("expected nil plan on missing-profile bailout")
	}
}

func TestConsultCompilePlan_RejectsEmptyInput(t *testing.T) {
	ag := NewOrchestratorAgent(compileRunner("{}"), "", "", 100000, 10, 25, 10, 5, testLogger())

	if _, err := ag.ConsultCompilePlan(context.Background(), CompilePlanInput{}); err == nil {
		t.Error("expected error on empty markdown plan")
	}
	if _, err := ag.ConsultCompilePlan(context.Background(), CompilePlanInput{
		MarkdownPlan: "# x",
	}); err == nil {
		t.Error("expected error on empty guest roster")
	}
}

func TestConsultCompilePlan_RevisePromptIncludesPrior(t *testing.T) {
	var capturedPrompt string
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedPrompt = opts.Prompt
		return &types.RunResult{
			SessionID: "c",
			Output:    `{"steps":[{"id":"s1","agent_id":"artist","description":"x"}],"metadata":{}}`,
		}, nil
	}
	ag := NewOrchestratorAgent(r, "", "", 100000, 10, 25, 10, 5, testLogger())

	_, err := ag.ConsultCompilePlan(context.Background(), CompilePlanInput{
		MarkdownPlan:     "# X\n",
		ActiveGuests:     []GuestProfile{{ID: "artist"}},
		PriorCompilation: `{"steps":[{"id":"s1","agent_id":"artist","description":"old"}]}`,
		Feedback:         "change step 1 to blue sprites",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedPrompt, "Prior Compilation") {
		t.Error("revise prompt missing Prior Compilation section")
	}
	if !strings.Contains(capturedPrompt, "Revise Feedback") {
		t.Error("revise prompt missing Revise Feedback section")
	}
	if !strings.Contains(capturedPrompt, "change step 1 to blue sprites") {
		t.Error("revise prompt missing the actual feedback text")
	}
	if !strings.Contains(capturedPrompt, "REUSE") {
		t.Error("revise prompt should instruct REUSE of existing ids")
	}
}

// --- handleExecuteMarkdown: no-draft reply, inline-beats-draft, missing-profile, happy path ---

// newExecuteTestOrch produces an Orchestrator with planStore + orchAgent
// configured for Execute-mode compile tests. orchRunner is used for the
// single compile consult; we don't exercise the Plan-mode scout path.
func newExecuteTestOrch(t *testing.T, compileOutput string) (*Orchestrator, *testChannel) {
	t.Helper()
	r := compileRunner(compileOutput)
	orchAg := NewOrchestratorAgent(r, "", "", 100000, 10, 25, 10, 5, testLogger())

	orch, ch := newPlanTestOrch(t, r)
	orch.orchAgent = orchAg
	orch.guestIndex = &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"artist":   {ID: "artist", Role: "artist"},
			"animator": {ID: "animator", Role: "animator"},
		},
	}
	return orch, ch
}

func TestHandleExecuteMarkdown_NoDraftReplyGuidance(t *testing.T) {
	orch, ch := newExecuteTestOrch(t, `{}`)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "u",
		ThreadID:    "t1",
		Text:        "[system:execute] go",
		Timestamp:   time.Now(),
	})

	found := false
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "Execute mode needs a plan to compile") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected no-draft guidance; got: %+v", ch.sentMessages())
	}
}

// seedDraftPlan writes a plan markdown into the store and returns (path, id).
func seedDraftPlan(t *testing.T, orch *Orchestrator, body string) (string, string) {
	t.Helper()
	path, err := orch.planStore.Save(orch.projectSlug(), "Plan", body)
	if err != nil {
		t.Fatalf("seed plan save: %v", err)
	}
	p, err := orch.planStore.Load(path)
	if err != nil {
		t.Fatalf("seed plan load: %v", err)
	}
	return path, p.Frontmatter.ID
}

func TestHandleExecuteMarkdown_CompilesStoredDraftAndBroadcastsExecplan(t *testing.T) {
	out := `{"steps":[{"id":"s1","agent_id":"artist","description":"x"}],"metadata":{"title":"T"}}`
	orch, ch := newExecuteTestOrch(t, out)
	_, planID := seedDraftPlan(t, orch, "# Plan\n\nDo things.")

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text: "[system:execute]", Timestamp: time.Now(),
	})

	var foundExecplan bool
	for _, m := range ch.sentMessages() {
		if m.DisplayID == "execplan:"+planID {
			foundExecplan = true
			disp, ok := m.Display.(map[string]any)
			if !ok {
				t.Fatalf("display not map: %T", m.Display)
			}
			if disp["kind"] != "execplan" {
				t.Errorf("kind = %v, want execplan", disp["kind"])
			}
			gen, _ := disp["compileGeneration"].(int)
			if gen != 1 {
				t.Errorf("compileGeneration = %v, want 1", disp["compileGeneration"])
			}
		}
	}
	if !foundExecplan {
		t.Fatalf("no execplan:%s broadcast; messages=%+v", planID, ch.sentMessages())
	}

	// Frontmatter should have bumped and recorded required profiles.
	metas, _ := orch.planStore.List(orch.projectSlug())
	if len(metas) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(metas))
	}
	p, err := orch.planStore.Load(metas[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Frontmatter.CompileGeneration != 1 {
		t.Errorf("gen = %d, want 1", p.Frontmatter.CompileGeneration)
	}
	if len(p.Frontmatter.RequiredProfiles) != 1 || p.Frontmatter.RequiredProfiles[0] != "artist" {
		t.Errorf("RequiredProfiles = %v, want [artist]", p.Frontmatter.RequiredProfiles)
	}

	// Reissue execute — DisplayID for the revise must be the same (stable).
	ch.reset()
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text: "[system:execute] revise it", Timestamp: time.Now(),
	})
	for _, m := range ch.sentMessages() {
		if m.DisplayID != "" && m.DisplayID != "execplan:"+planID {
			// any non-empty display must be our stable exec plan
			if strings.HasPrefix(m.DisplayID, "execplan:") {
				t.Errorf("revise used a different execplan displayID: %q", m.DisplayID)
			}
		}
	}
}

func TestHandleExecuteMarkdown_MissingProfileReply(t *testing.T) {
	orch, ch := newExecuteTestOrch(t, `{"error":"missing profile: composer"}`)
	_, _ = seedDraftPlan(t, orch, "# Plan\n\nRequires a composer.")

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text: "[system:execute]", Timestamp: time.Now(),
	})

	var matched bool
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "composer") && strings.Contains(m.Text, "Cannot compile") {
			matched = true
		}
	}
	if !matched {
		t.Errorf("expected missing-profile guidance; got %+v", ch.sentMessages())
	}
}

func TestHandleExecuteMarkdown_CompileMutexRejectsParallel(t *testing.T) {
	out := `{"steps":[{"id":"s1","agent_id":"artist","description":"x"}],"metadata":{}}`
	orch, ch := newExecuteTestOrch(t, out)
	_, planID := seedDraftPlan(t, orch, "# Plan\n\nDo stuff.")

	// Pre-occupy the mutex so the real dispatch rejects.
	orch.compileMu.Lock()
	orch.compileInFlight[planID] = true
	orch.compileMu.Unlock()

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text: "[system:execute]", Timestamp: time.Now(),
	})

	var matched bool
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "compile is already running") {
			matched = true
		}
	}
	if !matched {
		t.Errorf("expected mutex rejection; got %+v", ch.sentMessages())
	}
}

// --- handleExecutePlanApproval rejections ---

func seedCompiledSidecar(t *testing.T, orch *Orchestrator, planPath string, gen int, requiredProfiles []string) {
	t.Helper()
	plan := execute.ExecutionPlan{
		Steps: []execute.PlanStep{
			{ID: "s1", AgentID: "artist", Description: "x", Status: execute.StepPending},
		},
		Metadata: execute.PlanMetadata{Title: "T"},
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	// Seed so frontmatter gen matches the embedded body gen.
	newGen, err := orch.planStore.SaveCompiled(planPath, body, requiredProfiles)
	if err != nil {
		t.Fatalf("seed compiled: %v", err)
	}
	if newGen != gen {
		// Re-save until we reach requested gen. Tests that need gen=1 are fine.
		for newGen < gen {
			newGen, err = orch.planStore.SaveCompiled(planPath, body, requiredProfiles)
			if err != nil {
				t.Fatalf("bump compiled: %v", err)
			}
		}
	}
	// Rewrite sidecar with the correct embedded plan_generation to match FM.
	plan.Metadata.PlanGeneration = newGen
	body, _ = json.Marshal(plan)
	if err := os.WriteFile(planPath+".execute.json", body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHandleExecutePlanApproval_RejectsStaleGeneration(t *testing.T) {
	orch, ch := newExecuteTestOrch(t, `{}`)
	path, planID := seedDraftPlan(t, orch, "# Plan\n")
	seedCompiledSidecar(t, orch, path, 1, []string{"artist"})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text:      fmt.Sprintf("[system:execute] [gate:approve:execplan:%s:0]", planID),
		Timestamp: time.Now(),
	})

	matched := false
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "no longer current") {
			matched = true
		}
	}
	if !matched {
		t.Errorf("expected stale-generation rejection; got %+v", ch.sentMessages())
	}
}

func TestHandleExecutePlanApproval_RejectsGuestSetChanged(t *testing.T) {
	orch, ch := newExecuteTestOrch(t, `{}`)
	path, planID := seedDraftPlan(t, orch, "# Plan\n")
	// Compile required an "animator" role even though guest index only has artist/animator right now.
	seedCompiledSidecar(t, orch, path, 1, []string{"artist", "composer"})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text:      fmt.Sprintf("[system:execute] [gate:approve:execplan:%s:1]", planID),
		Timestamp: time.Now(),
	})

	matched := false
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "composer") && strings.Contains(m.Text, "no longer active") {
			matched = true
		}
	}
	if !matched {
		t.Errorf("expected guest-set-changed rejection; got %+v", ch.sentMessages())
	}
}

func TestHandleExecutePlanApproval_AbortReply(t *testing.T) {
	orch, ch := newExecuteTestOrch(t, `{}`)
	_, planID := seedDraftPlan(t, orch, "# Plan\n")

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text:      fmt.Sprintf("[system:execute] [gate:abort:execplan:%s:0]", planID),
		Timestamp: time.Now(),
	})

	matched := false
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "Plan execution aborted") {
			matched = true
		}
	}
	if !matched {
		t.Errorf("expected abort reply; got %+v", ch.sentMessages())
	}
}
