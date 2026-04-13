package voice

// SpeakerSelection determines which agent speaks audibly when multiple
// agents are candidates. Only one agent holds the floor at a time.
type SpeakerSelection struct {
	AudibleSpeaker   string
	SilentCandidates []string
}

// AgentVoiceConfig holds voice-related metadata from an agent's frontmatter.
type AgentVoiceConfig struct {
	ID            string
	VoiceID       string
	VoiceModel    string
	VoicePriority int // Lower = speaks first. Default: 50.
}

// RoutingHint mirrors the relevant fields from routeToGuests() for speaker selection.
type RoutingHint struct {
	Guests              []string
	IncludeOrchestrator bool
	DirectedText        map[string]string
	PreviousSpeaker     string
}

// SelectSpeaker picks exactly one audible speaker from routing results.
// Priority order:
//  1. Explicit @-mention (single directed text key)
//  2. Single routing result
//  3. Specialist match strength (has directed text)
//  4. Last-speaker continuity
//  5. Frontmatter voice_priority
//  6. Alphabetical fallback
func SelectSpeaker(hint RoutingHint, configs map[string]AgentVoiceConfig) SpeakerSelection {
	guests := hint.Guests

	if len(guests) == 0 {
		if hint.IncludeOrchestrator {
			return SpeakerSelection{AudibleSpeaker: "orchestrator"}
		}
		return SpeakerSelection{}
	}

	if len(guests) == 1 {
		return SpeakerSelection{
			AudibleSpeaker:   guests[0],
			SilentCandidates: nil,
		}
	}

	// 1. Explicit @-mention: if directed text has exactly one key
	if len(hint.DirectedText) == 1 {
		for agent := range hint.DirectedText {
			return buildSelection(agent, guests)
		}
	}

	// 3. Specialist match strength: directed text for one but not others
	var withDirected []string
	for _, g := range guests {
		if _, ok := hint.DirectedText[g]; ok {
			withDirected = append(withDirected, g)
		}
	}
	if len(withDirected) == 1 {
		return buildSelection(withDirected[0], guests)
	}

	// 4. Last-speaker continuity
	if hint.PreviousSpeaker != "" {
		for _, g := range guests {
			if g == hint.PreviousSpeaker {
				return buildSelection(g, guests)
			}
		}
	}

	// 5. Frontmatter voice_priority
	if len(configs) > 0 {
		best := guests[0]
		bestPri := getPriority(best, configs)
		for _, g := range guests[1:] {
			p := getPriority(g, configs)
			if p < bestPri || (p == bestPri && g < best) {
				best = g
				bestPri = p
			}
		}
		return buildSelection(best, guests)
	}

	// 6. Alphabetical fallback
	best := guests[0]
	for _, g := range guests[1:] {
		if g < best {
			best = g
		}
	}
	return buildSelection(best, guests)
}

func buildSelection(speaker string, allGuests []string) SpeakerSelection {
	var silent []string
	for _, g := range allGuests {
		if g != speaker {
			silent = append(silent, g)
		}
	}
	return SpeakerSelection{
		AudibleSpeaker:   speaker,
		SilentCandidates: silent,
	}
}

func getPriority(agentID string, configs map[string]AgentVoiceConfig) int {
	if cfg, ok := configs[agentID]; ok && cfg.VoicePriority > 0 {
		return cfg.VoicePriority
	}
	return 50
}
