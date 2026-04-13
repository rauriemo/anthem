package voice

import (
	"testing"
	"time"
)

func TestFloorController_OrchestratorMode_ValidTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		from    FloorState
		to      FloorState
		reason  string
		wantErr bool
	}{
		{"idle to user speaking", Idle, UserSpeaking, "vad_onset", false},
		{"user speaking to commit", UserSpeaking, CommitPending, "silence_detected", false},
		{"user speaking to idle", UserSpeaking, Idle, "vad_false_positive", false},
		{"commit to orchestrator speaking", CommitPending, OrchestratorSpeaking, "turn_committed", false},
		{"orchestrator speaking to idle", OrchestratorSpeaking, Idle, "response_done", false},
		{"orchestrator speaking to barge-in", OrchestratorSpeaking, BargeInAbort, "vad_onset", false},
		{"barge-in to user speaking", BargeInAbort, UserSpeaking, "abort_complete", false},

		{"idle to orchestrator speaking", Idle, OrchestratorSpeaking, "text_chat_response", false},

		// Invalid transitions
		{"idle to commit pending", Idle, CommitPending, "bug", true},
		{"idle to barge-in", Idle, BargeInAbort, "bug", true},
		{"commit to idle", CommitPending, Idle, "bug", true},
		{"commit to user speaking", CommitPending, UserSpeaking, "bug", true},
		{"barge-in to idle", BargeInAbort, Idle, "bug", true},

		// Extended states must fail in orchestrator mode
		{"commit to agent speaking", CommitPending, AgentSpeaking, "bug", true},
		{"commit to orch ack", CommitPending, OrchestratorAck, "bug", true},
		{"idle to handoff pending", Idle, HandoffPending, "bug", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc := NewFloorController("orchestrator")
			fc.forceState(tt.from)
			err := fc.Transition(tt.to, tt.reason)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transition(%v -> %v) error = %v, wantErr %v", tt.from, tt.to, err, tt.wantErr)
			}
		})
	}
}

func TestFloorController_InitialState(t *testing.T) {
	t.Parallel()
	fc := NewFloorController("orchestrator")
	if got := fc.State(); got != Idle {
		t.Errorf("initial state = %v, want Idle", got)
	}
	if got := fc.ActiveSpeaker(); got != "" {
		t.Errorf("initial speaker = %q, want empty", got)
	}
	if got := fc.Mode(); got != "orchestrator" {
		t.Errorf("mode = %q, want orchestrator", got)
	}
}

func TestFloorController_SetActiveSpeaker(t *testing.T) {
	t.Parallel()
	fc := NewFloorController("orchestrator")
	if err := fc.SetActiveSpeaker("orch"); err != nil {
		t.Fatal(err)
	}
	if got := fc.ActiveSpeaker(); got != "orch" {
		t.Errorf("speaker = %q, want orch", got)
	}
}

func TestFloorController_EventsEmitted(t *testing.T) {
	t.Parallel()
	fc := NewFloorController("orchestrator")

	if err := fc.Transition(UserSpeaking, "vad_onset"); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-fc.Events():
		if ev.FromState != Idle {
			t.Errorf("from = %v, want Idle", ev.FromState)
		}
		if ev.ToState != UserSpeaking {
			t.Errorf("to = %v, want UserSpeaking", ev.ToState)
		}
		if ev.Reason != "vad_onset" {
			t.Errorf("reason = %q, want vad_onset", ev.Reason)
		}
		if ev.Timestamp.IsZero() {
			t.Error("timestamp is zero")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for floor event")
	}
}

func TestFloorController_BargeInSequence(t *testing.T) {
	t.Parallel()
	fc := NewFloorController("orchestrator")

	steps := []struct {
		to     FloorState
		reason string
	}{
		{UserSpeaking, "vad_onset"},
		{CommitPending, "silence_detected"},
		{OrchestratorSpeaking, "turn_committed"},
		{BargeInAbort, "vad_onset"},
		{UserSpeaking, "abort_complete"},
	}

	for i, s := range steps {
		if err := fc.Transition(s.to, s.reason); err != nil {
			t.Fatalf("step %d (%s -> %s): %v", i, fc.State(), s.to, err)
		}
	}

	if got := fc.State(); got != UserSpeaking {
		t.Errorf("final state = %v, want UserSpeaking", got)
	}
}

func TestFloorController_FloorStateString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state FloorState
		want  string
	}{
		{Idle, "idle"},
		{UserSpeaking, "user_speaking"},
		{CommitPending, "commit_pending"},
		{OrchestratorSpeaking, "orchestrator_speaking"},
		{BargeInAbort, "barge_in_abort"},
		{OrchestratorAck, "orchestrator_ack"},
		{AgentSpeaking, "agent_speaking"},
		{HandoffPending, "handoff_pending"},
		{FloorState(99), "FloorState(99)"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("FloorState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestFloorController_MultiAgentMode_ValidTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		from    FloorState
		to      FloorState
		reason  string
		wantErr bool
	}{
		{"commit to orch ack", CommitPending, OrchestratorAck, "turn_committed", false},
		{"orch ack to agent speaking", OrchestratorAck, AgentSpeaking, "ack_complete", false},
		{"agent speaking to idle", AgentSpeaking, Idle, "response_done", false},
		{"agent speaking to handoff", AgentSpeaking, HandoffPending, "handoff_requested", false},
		{"handoff to orch ack", HandoffPending, OrchestratorAck, "handoff_committed", false},
		{"handoff to barge-in", HandoffPending, BargeInAbort, "vad_onset", false},

		// OrchestratorSpeaking is NOT in multi-agent transitions
		{"commit to orch speaking (invalid in multi)", CommitPending, OrchestratorSpeaking, "bug", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc := NewFloorController("multi-agent")
			fc.forceState(tt.from)
			err := fc.Transition(tt.to, tt.reason)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transition(%v -> %v) error = %v, wantErr %v", tt.from, tt.to, err, tt.wantErr)
			}
		})
	}
}
