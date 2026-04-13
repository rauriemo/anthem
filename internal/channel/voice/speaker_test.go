package voice

import "testing"

func TestSelectSpeaker_SingleGuest(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		Guests: []string{"eiji"},
	}, nil)

	if sel.AudibleSpeaker != "eiji" {
		t.Errorf("speaker = %q, want eiji", sel.AudibleSpeaker)
	}
	if len(sel.SilentCandidates) != 0 {
		t.Errorf("silent = %v, want empty", sel.SilentCandidates)
	}
}

func TestSelectSpeaker_EmptyGuests_OrchestratorFallback(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		Guests:              []string{},
		IncludeOrchestrator: true,
	}, nil)

	if sel.AudibleSpeaker != "orchestrator" {
		t.Errorf("speaker = %q, want orchestrator", sel.AudibleSpeaker)
	}
}

func TestSelectSpeaker_ExplicitMention(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		Guests:       []string{"eiji", "miyazaki"},
		DirectedText: map[string]string{"miyazaki": "handle the sprites"},
	}, nil)

	if sel.AudibleSpeaker != "miyazaki" {
		t.Errorf("speaker = %q, want miyazaki", sel.AudibleSpeaker)
	}
	if len(sel.SilentCandidates) != 1 || sel.SilentCandidates[0] != "eiji" {
		t.Errorf("silent = %v, want [eiji]", sel.SilentCandidates)
	}
}

func TestSelectSpeaker_LastSpeakerContinuity(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		Guests:          []string{"eiji", "miyazaki"},
		PreviousSpeaker: "miyazaki",
	}, nil)

	if sel.AudibleSpeaker != "miyazaki" {
		t.Errorf("speaker = %q, want miyazaki (continuity)", sel.AudibleSpeaker)
	}
}

func TestSelectSpeaker_VoicePriority(t *testing.T) {
	t.Parallel()
	configs := map[string]AgentVoiceConfig{
		"eiji":    {ID: "eiji", VoicePriority: 30},
		"tolkien": {ID: "tolkien", VoicePriority: 10},
	}

	sel := SelectSpeaker(RoutingHint{
		Guests: []string{"eiji", "tolkien"},
	}, configs)

	if sel.AudibleSpeaker != "tolkien" {
		t.Errorf("speaker = %q, want tolkien (priority 10 < 30)", sel.AudibleSpeaker)
	}
}

func TestSelectSpeaker_AlphabeticalFallback(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		Guests: []string{"tolkien", "eiji", "miyazaki"},
	}, nil)

	if sel.AudibleSpeaker != "eiji" {
		t.Errorf("speaker = %q, want eiji (alphabetical)", sel.AudibleSpeaker)
	}
}

func TestSelectSpeaker_AllTiedPriority(t *testing.T) {
	t.Parallel()
	configs := map[string]AgentVoiceConfig{
		"eiji":    {ID: "eiji", VoicePriority: 50},
		"tolkien": {ID: "tolkien", VoicePriority: 50},
	}

	sel := SelectSpeaker(RoutingHint{
		Guests: []string{"tolkien", "eiji"},
	}, configs)

	if sel.AudibleSpeaker != "eiji" {
		t.Errorf("speaker = %q, want eiji (alphabetical tiebreak)", sel.AudibleSpeaker)
	}
}

func TestSelectSpeaker_DirectedTextOneOfMany(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		Guests:       []string{"eiji", "miyazaki", "tolkien"},
		DirectedText: map[string]string{"eiji": "handle scene", "miyazaki": "handle sprites"},
	}, nil)

	// Two directed texts: falls through to next criteria (no previous speaker, no configs -> alphabetical)
	if sel.AudibleSpeaker != "eiji" {
		t.Errorf("speaker = %q, want eiji (alphabetical after multi-directed)", sel.AudibleSpeaker)
	}
}

func TestSelectSpeaker_OrchestratorOnlyMode(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		IncludeOrchestrator: true,
	}, nil)

	if sel.AudibleSpeaker != "orchestrator" {
		t.Errorf("speaker = %q, want orchestrator", sel.AudibleSpeaker)
	}
}

func TestSelectSpeaker_EmptyGuests_NoOrchestrator(t *testing.T) {
	t.Parallel()
	sel := SelectSpeaker(RoutingHint{
		Guests:              []string{},
		IncludeOrchestrator: false,
	}, nil)

	if sel.AudibleSpeaker != "" {
		t.Errorf("speaker = %q, want empty when no guests and orchestrator excluded", sel.AudibleSpeaker)
	}
}
