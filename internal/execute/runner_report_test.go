package execute

import (
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/contextreport"
	"github.com/rauriemo/anthem/internal/prompt"
)

// TestBuildPlanContext_IncludesUpstreamReport pins the core forwarding
// behavior: once a successful upstream step's context_report has been
// stashed on the PlanRunner, the plan context rendered for a
// downstream step carries a Final-Report summary so the downstream
// LLM sees the chosen slug, artifact ID, and path the upstream agent
// picked at runtime — not just the (possibly placeholder-laden)
// upstream step description copied from the compiled plan. Motivating
// bug: embercoil-serpent chain, where Walt's prompt contained a
// literal "<slug>" path because Miyazaki's chosen slug never made it
// into the downstream prompt.
func TestBuildPlanContext_IncludesUpstreamReport(t *testing.T) {
	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
	})
	pr.plan = &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "miyazaki", Description: "Draw south-facing sprite", Status: StepCompleted},
			{ID: "s2", AgentID: "walt", Description: "Animate the sprite at Assets/.../<slug>-idle-south.png", DependsOn: "s1", Status: StepPending},
		},
		Metadata: PlanMetadata{Title: "Report forwarding pin", CreatedAt: time.Now()},
	}

	pr.stepReports["s1"] = &contextreport.ContextReport{
		Action:  "asset_created",
		Summary: "Generated embercoil-serpent idle sprite",
		Artifact: &contextreport.ReportArtifact{
			ID:   "embercoil-serpent-idle-south",
			Type: "image/png",
			Path: "Assets/_Project/Art/Generated/Creatures/embercoil-serpent/embercoil-serpent-idle-south.png",
		},
		Produces: []string{"embercoil-serpent-idle-south"},
	}

	pc := pr.buildPlanContext(&pr.plan.Steps[1], nil)
	if pc == nil {
		t.Fatal("buildPlanContext returned nil")
	}
	if len(pc.UpstreamDetail) != 1 {
		t.Fatalf("expected one UpstreamDetail entry, got %d", len(pc.UpstreamDetail))
	}

	detail := pc.UpstreamDetail[0]
	if detail.FinalReport == nil {
		t.Fatal("FinalReport should be populated from stepReports[s1]")
	}
	got := *detail.FinalReport
	want := prompt.UpstreamReportSummary{
		Summary:      "Generated embercoil-serpent idle sprite",
		Action:       "asset_created",
		ArtifactID:   "embercoil-serpent-idle-south",
		ArtifactPath: "Assets/_Project/Art/Generated/Creatures/embercoil-serpent/embercoil-serpent-idle-south.png",
		Produces:     []string{"embercoil-serpent-idle-south"},
	}
	if got.Summary != want.Summary ||
		got.Action != want.Action ||
		got.ArtifactID != want.ArtifactID ||
		got.ArtifactPath != want.ArtifactPath ||
		len(got.Produces) != 1 || got.Produces[0] != want.Produces[0] {
		t.Fatalf("FinalReport mismatch\ngot:  %+v\nwant: %+v", got, want)
	}

	// Also assert the rendered prompt text surfaces the concrete
	// path so the downstream LLM can resolve its <slug>-laden
	// description against the upstream agent's actual output.
	rendered := prompt.BuildGuestPrompt("persona", prompt.LiveContext{}, "go", prompt.GuestPromptOpts{
		PlanContext: pc,
	})
	for _, needle := range []string{
		"Final report from miyazaki",
		"asset_created",
		"embercoil-serpent-idle-south",
		"Assets/_Project/Art/Generated/Creatures/embercoil-serpent/embercoil-serpent-idle-south.png",
	} {
		if !strings.Contains(rendered, needle) {
			t.Errorf("rendered prompt missing %q\n----\n%s\n----", needle, rendered)
		}
	}
}

// TestBuildPlanContext_OmitsReportSectionWhenNil is the backward-compat
// pin: when the runner has not stored a context_report for the
// upstream step (e.g. the upstream agent never emitted one), the
// rendered plan context must not sprout a stray "Final report from"
// header and must otherwise match the pre-fix layout.
func TestBuildPlanContext_OmitsReportSectionWhenNil(t *testing.T) {
	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
	})
	pr.plan = &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "miyazaki", Description: "Draw sprite", Status: StepCompleted},
			{ID: "s2", AgentID: "walt", Description: "Animate sprite", DependsOn: "s1", Status: StepPending},
		},
		Metadata: PlanMetadata{Title: "No-report pin", CreatedAt: time.Now()},
	}

	pc := pr.buildPlanContext(&pr.plan.Steps[1], nil)
	if pc == nil {
		t.Fatal("buildPlanContext returned nil")
	}
	if len(pc.UpstreamDetail) != 1 {
		t.Fatalf("expected one UpstreamDetail entry, got %d", len(pc.UpstreamDetail))
	}
	if pc.UpstreamDetail[0].FinalReport != nil {
		t.Fatalf("FinalReport should be nil when no report was stored; got %+v", *pc.UpstreamDetail[0].FinalReport)
	}

	rendered := prompt.BuildGuestPrompt("persona", prompt.LiveContext{}, "go", prompt.GuestPromptOpts{
		PlanContext: pc,
	})
	if strings.Contains(rendered, "Final report from") {
		t.Errorf("rendered prompt should not contain a Final-Report section when FinalReport is nil:\n----\n%s\n----", rendered)
	}
	// Sanity: the upstream description is still present (confirms
	// we actually rendered the UpstreamDetail block).
	if !strings.Contains(rendered, "Draw sprite") {
		t.Errorf("rendered prompt missing upstream description; render incomplete:\n%s", rendered)
	}
}
