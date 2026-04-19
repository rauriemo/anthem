package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// --- resolveGuestAlias / rewriteStepAliases ---

// guestRoster is the shared roster used by the alias tests. It exercises
// all three fields that resolveGuestAlias matches against: the orchestrator
// guest has a display Name distinct from its ID ("Tower" vs "orchestrator")
// so a plan-markdown reference to "tower" must resolve to "orchestrator".
// walt covers unique-profile lookup ("animator" -> "walt").
func guestRoster() []GuestProfile {
	return []GuestProfile{
		{ID: "orchestrator", Name: "Tower", Profile: "orchestrator"},
		{ID: "miyazaki", Name: "Miyazaki", Profile: "artist"},
		{ID: "walt", Name: "Walt", Profile: "animator"},
	}
}

func TestResolveGuestAlias_ByName(t *testing.T) {
	// "Tower" is the display name of the orchestrator guest — plan
	// markdown that says "Tower scaffolds the outline" must resolve to
	// id "orchestrator" even though no guest has profile "tower".
	got := resolveGuestAlias("Tower", guestRoster())
	if got != "orchestrator" {
		t.Errorf("resolveGuestAlias(Tower) = %q, want orchestrator", got)
	}
}

func TestResolveGuestAlias_ByProfile(t *testing.T) {
	// "animator" is walt's profile (role) — plan markdown that says
	// "Profile: animator" must resolve to id "walt".
	got := resolveGuestAlias("animator", guestRoster())
	if got != "walt" {
		t.Errorf("resolveGuestAlias(animator) = %q, want walt", got)
	}
}

func TestResolveGuestAlias_CaseInsensitive(t *testing.T) {
	cases := []string{"TOWER", "tower", "Tower", "ToWeR", "  tower  "}
	for _, in := range cases {
		got := resolveGuestAlias(in, guestRoster())
		if got != "orchestrator" {
			t.Errorf("resolveGuestAlias(%q) = %q, want orchestrator", in, got)
		}
	}
}

func TestResolveGuestAlias_AmbiguousReturnsEmpty(t *testing.T) {
	// Two guests share the same profile; a reference to that profile is
	// ambiguous and must NOT silently pick one. Callers fall back to
	// the existing "no active guest can fulfill" error so the user
	// disambiguates explicitly.
	roster := []GuestProfile{
		{ID: "walt-a", Name: "Walt A", Profile: "animator"},
		{ID: "walt-b", Name: "Walt B", Profile: "animator"},
	}
	if got := resolveGuestAlias("animator", roster); got != "" {
		t.Errorf("resolveGuestAlias(animator) on ambiguous roster = %q, want empty", got)
	}
}

func TestResolveGuestAlias_NoMatchReturnsEmpty(t *testing.T) {
	if got := resolveGuestAlias("phoenix", guestRoster()); got != "" {
		t.Errorf("resolveGuestAlias(phoenix) = %q, want empty", got)
	}
	if got := resolveGuestAlias("", guestRoster()); got != "" {
		t.Errorf("resolveGuestAlias(\"\") = %q, want empty", got)
	}
	if got := resolveGuestAlias("   ", guestRoster()); got != "" {
		t.Errorf("resolveGuestAlias(whitespace) = %q, want empty", got)
	}
}

func TestResolveGuestAlias_IDTakesPrecedenceImplicitly(t *testing.T) {
	// When the token is already a valid ID, the resolver returns that
	// ID. (It matches via the ID field; no rewrite is observable, but
	// callers can still use the return value as the canonical handle.)
	got := resolveGuestAlias("walt", guestRoster())
	if got != "walt" {
		t.Errorf("resolveGuestAlias(walt) = %q, want walt", got)
	}
}

func TestRewriteStepAliases_RewritesNameToID(t *testing.T) {
	// LLM emits agent_id "Tower" (the display name). rewriteStepAliases
	// must rewrite it to "orchestrator" so Validate passes and dispatch
	// routes to the correct guest.
	plan := &execute.ExecutionPlan{
		Steps: []execute.PlanStep{
			{ID: "s1", AgentID: "Tower", Description: "x"},
			{ID: "s2", AgentID: "orchestrator", Description: "y"},
			{ID: "s3", AgentID: "animator", Description: "z"},
		},
	}
	rewriteStepAliases(plan, guestRoster(), testLogger())
	want := []string{"orchestrator", "orchestrator", "walt"}
	for i, s := range plan.Steps {
		if s.AgentID != want[i] {
			t.Errorf("step[%d].AgentID = %q, want %q", i, s.AgentID, want[i])
		}
	}
}

func TestRewriteStepAliases_LeavesAmbiguousAndUnknownAlone(t *testing.T) {
	// Ambiguous (two guests share profile) and unknown aliases must be
	// left in place so Validate surfaces a clear error pointing at the
	// real offender instead of silently routing to the wrong guest.
	roster := []GuestProfile{
		{ID: "walt-a", Name: "Walt", Profile: "animator"},
		{ID: "walt-b", Name: "Walter", Profile: "animator"},
		{ID: "miyazaki", Name: "Miyazaki", Profile: "artist"},
	}
	plan := &execute.ExecutionPlan{
		Steps: []execute.PlanStep{
			{ID: "s1", AgentID: "animator"}, // ambiguous
			{ID: "s2", AgentID: "phoenix"},  // unknown
			{ID: "s3", AgentID: "artist"},   // unique profile -> miyazaki
		},
	}
	rewriteStepAliases(plan, roster, testLogger())
	if plan.Steps[0].AgentID != "animator" {
		t.Errorf("ambiguous step rewritten: got %q, want animator", plan.Steps[0].AgentID)
	}
	if plan.Steps[1].AgentID != "phoenix" {
		t.Errorf("unknown step rewritten: got %q, want phoenix", plan.Steps[1].AgentID)
	}
	if plan.Steps[2].AgentID != "miyazaki" {
		t.Errorf("unique-profile step = %q, want miyazaki", plan.Steps[2].AgentID)
	}
}

func TestRewriteStepAliases_TolerantNilAndEmpty(t *testing.T) {
	// Contract guardrails: nil plan, empty steps, and empty guests are
	// all no-ops. These paths are exercised indirectly through the
	// compile pipeline but are cheap to assert explicitly.
	rewriteStepAliases(nil, guestRoster(), testLogger())
	empty := &execute.ExecutionPlan{}
	rewriteStepAliases(empty, guestRoster(), testLogger())
	if len(empty.Steps) != 0 {
		t.Errorf("empty plan gained steps: %+v", empty.Steps)
	}
	plan := &execute.ExecutionPlan{Steps: []execute.PlanStep{{ID: "s1", AgentID: "Tower"}}}
	rewriteStepAliases(plan, nil, testLogger())
	if plan.Steps[0].AgentID != "Tower" {
		t.Errorf("empty roster rewrote step: got %q", plan.Steps[0].AgentID)
	}
}

// --- handleExecuteMarkdown alias recovery integration ---

// newExecuteTestOrchWithRoster is like newExecuteTestOrch but lets the test
// inject a custom guest roster and a custom RunFunc so the compile runner
// can respond differently on the first vs. second call (for testing the
// MissingProfile retry path).
func newExecuteTestOrchWithRoster(
	t *testing.T,
	roster map[string]guests.GuestAgent,
	runFunc func(ctx context.Context, opts types.RunOpts) (*types.RunResult, error),
) (*Orchestrator, *testChannel) {
	t.Helper()
	r := agent.NewMockRunner()
	r.RunFunc = runFunc
	orchAg := NewOrchestratorAgent(r, "", "", 100000, 10, 25, 10, 5, testLogger())

	orch, ch := newPlanTestOrch(t, r)
	orch.orchAgent = orchAg
	orch.guestIndex = &guests.GuestIndex{Agents: roster}
	return orch, ch
}

func TestHandleExecute_MissingProfileResolvedByName(t *testing.T) {
	// Simulates a real-world Tower-agent plan: first compile call bails
	// with "missing profile: tower"; the server resolves "tower" to the
	// orchestrator guest's id and retries; the second call returns a
	// valid plan keyed to id "orchestrator". The user should NEVER see
	// the "Cannot compile plan" follow-up.
	calls := 0
	runFunc := func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if !strings.Contains(opts.Prompt, "Compile Mode") {
			return nil, fmt.Errorf("unexpected prompt: %s", opts.Prompt)
		}
		calls++
		if calls == 1 {
			return &types.RunResult{SessionID: "c1", Output: `{"error":"missing profile: tower"}`}, nil
		}
		// On the retry, the hint must be present in the Revise
		// Feedback section so the LLM knows how to disambiguate.
		if !strings.Contains(opts.Prompt, "orchestrator") {
			t.Errorf("retry prompt did not carry disambiguation hint: %s", opts.Prompt)
		}
		return &types.RunResult{
			SessionID: "c2",
			Output:    `{"steps":[{"id":"s1","agent_id":"orchestrator","description":"scaffold"}],"metadata":{"title":"T"}}`,
		}, nil
	}
	orch, ch := newExecuteTestOrchWithRoster(t, map[string]guests.GuestAgent{
		"orchestrator": {ID: "orchestrator", Name: "Tower", Role: "orchestrator"},
	}, runFunc)
	_, planID := seedDraftPlan(t, orch, "# Plan\n\nTower scaffolds the outline.")

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text: "[system:execute]", Timestamp: time.Now(),
	})

	if calls != 2 {
		t.Errorf("expected 2 compile calls (initial + retry), got %d", calls)
	}
	var foundExecplan bool
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "Cannot compile plan") {
			t.Errorf("alias-resolved compile should not surface missing-profile error; got %q", m.Text)
		}
		if m.DisplayID == "execplan:"+planID {
			foundExecplan = true
		}
	}
	if !foundExecplan {
		t.Errorf("expected execplan broadcast after retry; got %+v", ch.sentMessages())
	}
}

func TestHandleExecute_MissingProfileUnresolvedStillReplies(t *testing.T) {
	// When the missing profile doesn't match any guest by id/name/role
	// the original user-facing error still fires — no retry, no
	// silent alias substitution.
	calls := 0
	runFunc := func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if !strings.Contains(opts.Prompt, "Compile Mode") {
			return nil, fmt.Errorf("unexpected prompt: %s", opts.Prompt)
		}
		calls++
		return &types.RunResult{SessionID: "c", Output: `{"error":"missing profile: phoenix"}`}, nil
	}
	orch, ch := newExecuteTestOrchWithRoster(t, map[string]guests.GuestAgent{
		"orchestrator": {ID: "orchestrator", Name: "Tower", Role: "orchestrator"},
	}, runFunc)
	_, _ = seedDraftPlan(t, orch, "# Plan\n\nNeeds a phoenix.")

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text: "[system:execute]", Timestamp: time.Now(),
	})

	if calls != 1 {
		t.Errorf("expected 1 compile call (no retry for unresolved alias), got %d", calls)
	}
	var matched bool
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "phoenix") && strings.Contains(m.Text, "Cannot compile") {
			matched = true
		}
	}
	if !matched {
		t.Errorf("expected missing-profile guidance for unresolved alias; got %+v", ch.sentMessages())
	}
}

func TestHandleExecute_ValidateRewritesUnknownAgentID(t *testing.T) {
	// LLM emits a step whose agent_id is the guest's display Name
	// ("Tower") instead of its id ("orchestrator"). rewriteStepAliases
	// must rewrite it before Validate runs, so the plan compiles
	// cleanly and the user sees an execplan broadcast (not a
	// "failed validation" error).
	runFunc := func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if !strings.Contains(opts.Prompt, "Compile Mode") {
			return nil, fmt.Errorf("unexpected prompt: %s", opts.Prompt)
		}
		return &types.RunResult{
			SessionID: "c",
			Output:    `{"steps":[{"id":"s1","agent_id":"Tower","description":"scaffold"}],"metadata":{"title":"T"}}`,
		}, nil
	}
	orch, ch := newExecuteTestOrchWithRoster(t, map[string]guests.GuestAgent{
		"orchestrator": {ID: "orchestrator", Name: "Tower", Role: "orchestrator"},
	}, runFunc)
	_, planID := seedDraftPlan(t, orch, "# Plan\n\nTower scaffolds.")

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism", SenderID: "u", ThreadID: "t1",
		Text: "[system:execute]", Timestamp: time.Now(),
	})

	var foundExecplan bool
	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "failed validation") {
			t.Errorf("alias rewrite should prevent Validate failure; got %q", m.Text)
		}
		if m.DisplayID == "execplan:"+planID {
			foundExecplan = true
		}
	}
	if !foundExecplan {
		t.Errorf("expected execplan broadcast after alias rewrite; got %+v", ch.sentMessages())
	}

	// The saved required-profiles set must reflect the canonical id,
	// not the display name. Otherwise downstream guest-set-change
	// checks would look for a non-existent "Tower" profile.
	metas, _ := orch.planStore.List(orch.projectSlug())
	if len(metas) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(metas))
	}
	p, err := orch.planStore.Load(metas[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Frontmatter.RequiredProfiles) != 1 || p.Frontmatter.RequiredProfiles[0] != "orchestrator" {
		t.Errorf("RequiredProfiles = %v, want [orchestrator]", p.Frontmatter.RequiredProfiles)
	}
}

// --- compile prompt shape: Name in roster, corrected example ---

func TestConsultCompilePlan_PromptIncludesGuestName(t *testing.T) {
	// Regression guard for the durable fix: the roster JSON the LLM
	// sees MUST carry the "name" field. Without it, plan-markdown
	// references like "Tower" are opaque to the compiler and
	// compilation falls back to the server-side alias retry on every
	// execute. We assert the wire shape, not just the Go struct, so
	// a future refactor that drops `json:"name,omitempty"` is caught
	// immediately.
	var capturedPrompt string
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedPrompt = opts.Prompt
		return &types.RunResult{
			SessionID: "c",
			Output:    `{"steps":[{"id":"s1","agent_id":"orchestrator","description":"x"}],"metadata":{}}`,
		}, nil
	}
	ag := NewOrchestratorAgent(r, "", "", 100000, 10, 25, 10, 5, testLogger())

	_, err := ag.ConsultCompilePlan(context.Background(), CompilePlanInput{
		MarkdownPlan: "# X\n",
		ActiveGuests: []GuestProfile{{ID: "orchestrator", Name: "Tower", Profile: "orchestrator"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedPrompt, `"name":"Tower"`) {
		t.Errorf("compile prompt did not include guest name in roster JSON; prompt was:\n%s", capturedPrompt)
	}
	// The corrected example in compilePromptSuffix anchors the LLM on
	// real ids. If someone reverts it to profile-shaped values the
	// LLM-authored agent_id drift will reappear.
	if !strings.Contains(capturedPrompt, `"agent_id": "miyazaki"`) {
		t.Errorf("compile prompt example should use a real id like \"miyazaki\"; prompt was:\n%s", capturedPrompt)
	}
	if strings.Contains(capturedPrompt, `"agent_id": "artist"`) {
		t.Errorf("compile prompt example should no longer use profile-shaped \"artist\" id; prompt was:\n%s", capturedPrompt)
	}
}

// --- activeGuestProfiles snapshot now includes Name ---

func TestActiveGuestProfiles_IncludesName(t *testing.T) {
	// Contract: the guest roster shipped to the LLM must carry the
	// display Name so the compiler can resolve plan-markdown
	// references like "Tower" or "Miyazaki" to ids without the
	// server-side alias recovery kicking in every time.
	orch, _ := newExecuteTestOrchWithRoster(t, map[string]guests.GuestAgent{
		"orchestrator": {ID: "orchestrator", Name: "Tower", Role: "orchestrator"},
		"miyazaki":     {ID: "miyazaki", Name: "Miyazaki", Role: "artist"},
	}, func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{SessionID: "x", Output: "{}"}, nil
	})

	profiles := orch.activeGuestProfiles()
	byID := make(map[string]GuestProfile, len(profiles))
	for _, p := range profiles {
		byID[p.ID] = p
	}
	if byID["orchestrator"].Name != "Tower" {
		t.Errorf("orchestrator.Name = %q, want Tower", byID["orchestrator"].Name)
	}
	if byID["miyazaki"].Name != "Miyazaki" {
		t.Errorf("miyazaki.Name = %q, want Miyazaki", byID["miyazaki"].Name)
	}
}

// --- Orchestrator-as-first-class-guest (Option A) ---

// TestActiveGuestProfiles_SurfacesOrchestratorFromFrontmatter asserts the
// compile roster always includes the orchestrator with name/description
// sourced from agents/orchestrator.md. ScanDirectory excludes
// orchestrator.md from GuestIndex, so activeGuestProfiles must layer it in
// itself — otherwise plans that route coordination steps to "Tower" fail
// at MissingProfile / Validate time with no recovery path.
func TestActiveGuestProfiles_SurfacesOrchestratorFromFrontmatter(t *testing.T) {
	orch, _ := newExecuteTestOrch(t, `{}`)
	tmpRoot := t.TempDir()
	orch.cfg.Workspace.Root = tmpRoot
	agentsDir := filepath.Join(tmpRoot, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	body := "---\n" +
		"name: Tower\n" +
		"role: orchestrator\n" +
		"description: Project coordinator for RebelTower.\n" +
		"---\n\n" +
		"Tower persona body.\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "orchestrator.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write orchestrator.md: %v", err)
	}

	profiles := orch.activeGuestProfiles()
	byID := make(map[string]GuestProfile, len(profiles))
	for _, p := range profiles {
		byID[p.ID] = p
	}
	orchP, ok := byID["orchestrator"]
	if !ok {
		t.Fatalf("expected orchestrator entry in profiles; got %+v", profiles)
	}
	if orchP.Name != "Tower" {
		t.Errorf("orchestrator.Name = %q, want Tower", orchP.Name)
	}
	if orchP.Profile != "orchestrator" {
		t.Errorf("orchestrator.Profile = %q, want orchestrator", orchP.Profile)
	}
	if !strings.Contains(orchP.Description, "RebelTower") {
		t.Errorf("orchestrator.Description = %q, want to contain RebelTower", orchP.Description)
	}
}

// TestActiveGuestProfiles_SurfacesSyntheticOrchestratorWhenMissing asserts
// that even on a fresh project with no orchestrator.md on disk, the
// compile roster still carries a synthetic "Tower" entry so first-time
// plan authors can route steps to the orchestrator without editing
// frontmatter first.
func TestActiveGuestProfiles_SurfacesSyntheticOrchestratorWhenMissing(t *testing.T) {
	orch, _ := newExecuteTestOrch(t, `{}`)
	orch.cfg.Workspace.Root = t.TempDir() // no agents/ directory at all

	profiles := orch.activeGuestProfiles()
	var orchP GuestProfile
	var found bool
	for _, p := range profiles {
		if p.ID == "orchestrator" {
			orchP = p
			found = true
		}
	}
	if !found {
		t.Fatalf("expected synthetic orchestrator entry; got %+v", profiles)
	}
	if orchP.Name != "Tower" {
		t.Errorf("orchestrator.Name = %q, want Tower (fallback default)", orchP.Name)
	}
	if orchP.Profile != "orchestrator" {
		t.Errorf("orchestrator.Profile = %q, want orchestrator", orchP.Profile)
	}
	if orchP.Description == "" {
		t.Error("orchestrator.Description should have a non-empty fallback default")
	}
}

// TestHandleExecuteMarkdown_AcceptsOrchestratorAgentID is the end-to-end
// regression guard for Option A: a compiled plan with
// step.agent_id=orchestrator must pass Validate, save with "orchestrator"
// in RequiredProfiles, and broadcast an execplan artifact — no
// MissingProfile follow-up, no Validate failure. Before this change the
// same plan bailed at MissingProfile because the guest scan excludes
// orchestrator.md from GuestIndex.
func TestHandleExecuteMarkdown_AcceptsOrchestratorAgentID(t *testing.T) {
	out := `{"steps":[{"id":"s1","agent_id":"orchestrator","description":"scaffold"}],"metadata":{"title":"T"}}`
	orch, ch := newExecuteTestOrch(t, out)
	planPath, planID := seedDraftPlan(t, orch, "# Plan\n\nTower scaffolds the outline.")

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "u",
		ThreadID:    "t1",
		Text:        "[system:execute]",
		Timestamp:   time.Now(),
	})

	for _, m := range ch.sentMessages() {
		if strings.Contains(m.Text, "Cannot compile plan") {
			t.Errorf("unexpected compile rejection: %q", m.Text)
		}
		if strings.Contains(m.Text, "failed validation") {
			t.Errorf("unexpected validate rejection: %q", m.Text)
		}
	}

	var foundExecplan bool
	for _, m := range ch.sentMessages() {
		if m.DisplayID == "execplan:"+planID {
			foundExecplan = true
		}
	}
	if !foundExecplan {
		t.Errorf("expected execplan broadcast for %q; got %+v", planID, ch.sentMessages())
	}

	p, err := orch.planStore.Load(planPath)
	if err != nil {
		t.Fatalf("load saved plan: %v", err)
	}
	if len(p.Frontmatter.RequiredProfiles) != 1 || p.Frontmatter.RequiredProfiles[0] != "orchestrator" {
		t.Errorf("RequiredProfiles = %v, want [orchestrator]", p.Frontmatter.RequiredProfiles)
	}
}
