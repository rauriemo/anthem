package orchestrator

import (
	"testing"

	"github.com/rauriemo/anthem/internal/channel"
	prismchan "github.com/rauriemo/anthem/internal/channel/prism"
	"github.com/rauriemo/anthem/internal/execute"
)

func TestGateResolutionFromMessage_TextFallback(t *testing.T) {
	msg := channel.IncomingMessage{
		ChannelKind: "slack",
		Text:        "[gate:revise] add shadow",
	}
	res, gateID, structured := gateResolutionFromMessage(msg)
	if structured {
		t.Error("non-prism text should not report as structured")
	}
	if gateID != "" {
		t.Errorf("text path should not carry a gate ID, got %q", gateID)
	}
	if res.Action != execute.GateRevise {
		t.Errorf("Action = %v, want revise", res.Action)
	}
	if res.Feedback != "add shadow" {
		t.Errorf("Feedback = %q, want add shadow", res.Feedback)
	}
	if len(res.FlaggedArtifacts) != 0 {
		t.Errorf("text path should not carry flagged artifacts, got %v", res.FlaggedArtifacts)
	}
}

func TestGateResolutionFromMessage_StructuredRawCarriesFlagged(t *testing.T) {
	msg := prismchan.NewGateActionIncomingMessage(
		"g-42",
		"revise",
		"globally darker",
		"thread-1",
		[]prismchan.FlaggedArtifact{
			{ArtifactID: "art-b", Note: "more detail"},
			{ArtifactID: "art-d"},
		},
	)

	res, gateID, structured := gateResolutionFromMessage(msg)
	if !structured {
		t.Fatal("expected structured = true for prism gate_action frame")
	}
	if gateID != "g-42" {
		t.Errorf("gateID = %q, want g-42", gateID)
	}
	if res.Action != execute.GateRevise {
		t.Errorf("Action = %v, want revise", res.Action)
	}
	if res.Feedback != "globally darker" {
		t.Errorf("Feedback = %q", res.Feedback)
	}
	if len(res.FlaggedArtifacts) != 2 {
		t.Fatalf("FlaggedArtifacts len = %d, want 2", len(res.FlaggedArtifacts))
	}
	if res.FlaggedArtifacts[0].ArtifactID != "art-b" ||
		res.FlaggedArtifacts[0].Note != "more detail" {
		t.Errorf("flagged[0] = %+v", res.FlaggedArtifacts[0])
	}
	if res.FlaggedArtifacts[1].ArtifactID != "art-d" {
		t.Errorf("flagged[1] = %+v", res.FlaggedArtifacts[1])
	}
}

func TestGateResolutionFromMessage_StructuredApproveEmpty(t *testing.T) {
	msg := prismchan.NewGateActionIncomingMessage("g-1", "approve", "", "t", nil)
	res, gateID, structured := gateResolutionFromMessage(msg)
	if !structured || gateID != "g-1" {
		t.Errorf("structured=%v gateID=%q", structured, gateID)
	}
	if res.Action != execute.GateApprove {
		t.Errorf("Action = %v, want approve", res.Action)
	}
	if len(res.FlaggedArtifacts) != 0 {
		t.Error("approve must not carry flagged artifacts")
	}
}
