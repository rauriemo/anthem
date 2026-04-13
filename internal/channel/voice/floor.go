package voice

import (
	"fmt"
	"sync"
	"time"
)

// FloorState represents the current state of the voice room floor.
type FloorState int

const (
	// Core states (Phase 1 -- orchestrator mode)
	Idle                 FloorState = iota // No one speaking, room quiet
	UserSpeaking                           // User has the floor, VAD active
	CommitPending                          // User stopped, turn being finalized
	OrchestratorSpeaking                   // Orchestrator's TTS playing
	BargeInAbort                           // User interrupted, canceling orchestrator

	// Extended states (Phase 3+ -- multi-agent mode only)
	OrchestratorAck // Fast ack playing
	AgentSpeaking   // Selected guest agent's TTS playing
	HandoffPending  // Switching to different agent
)

var floorStateNames = map[FloorState]string{
	Idle:                 "idle",
	UserSpeaking:         "user_speaking",
	CommitPending:        "commit_pending",
	OrchestratorSpeaking: "orchestrator_speaking",
	BargeInAbort:         "barge_in_abort",
	OrchestratorAck:      "orchestrator_ack",
	AgentSpeaking:        "agent_speaking",
	HandoffPending:       "handoff_pending",
}

func (s FloorState) String() string {
	if name, ok := floorStateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("FloorState(%d)", int(s))
}

// FloorEvent records a single state transition in the floor controller.
type FloorEvent struct {
	Timestamp  time.Time
	FromState  FloorState
	ToState    FloorState
	AgentID    string
	Transcript string
	Reason     string
}

type transition struct {
	from FloorState
	to   FloorState
}

// orchestratorTransitions defines the valid transition graph for orchestrator mode.
var orchestratorTransitions = map[transition]bool{
	{Idle, UserSpeaking}:                  true, // vad_onset
	{Idle, OrchestratorSpeaking}:          true, // text-chat response (no prior voice turn)
	{UserSpeaking, CommitPending}:         true, // silence_detected
	{UserSpeaking, Idle}:                  true, // vad_false_positive
	{CommitPending, OrchestratorSpeaking}: true, // turn_committed
	{OrchestratorSpeaking, Idle}:          true, // response_done
	{OrchestratorSpeaking, BargeInAbort}:  true, // vad_onset
	{BargeInAbort, UserSpeaking}:          true, // abort_complete
}

// multiAgentTransitions defines the valid transition graph for multi-agent mode (Phase 3+).
var multiAgentTransitions = map[transition]bool{
	{Idle, UserSpeaking}:              true,
	{UserSpeaking, CommitPending}:     true,
	{UserSpeaking, Idle}:              true,
	{CommitPending, OrchestratorAck}:  true,
	{OrchestratorAck, AgentSpeaking}:  true,
	{OrchestratorAck, BargeInAbort}:   true,
	{AgentSpeaking, Idle}:             true,
	{AgentSpeaking, BargeInAbort}:     true,
	{AgentSpeaking, HandoffPending}:   true,
	{HandoffPending, OrchestratorAck}: true,
	{HandoffPending, BargeInAbort}:    true,
	{BargeInAbort, UserSpeaking}:      true,
}

// FloorController manages turn-taking state for the voice room.
type FloorController struct {
	mu            sync.Mutex
	state         FloorState
	activeSpeaker string
	transitions   map[transition]bool
	events        chan FloorEvent
	mode          string
}

// NewFloorController creates a floor controller for the given mode.
// Valid modes: "orchestrator" (Phase 1-2) and "multi-agent" (Phase 3+).
func NewFloorController(mode string) *FloorController {
	t := orchestratorTransitions
	if mode == "multi-agent" {
		t = multiAgentTransitions
	}
	return &FloorController{
		state:       Idle,
		transitions: t,
		events:      make(chan FloorEvent, 64),
		mode:        mode,
	}
}

// State returns the current floor state.
func (fc *FloorController) State() FloorState {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.state
}

// Transition attempts to move from the current state to the target state.
// Returns an error if the transition is not valid in the current mode.
func (fc *FloorController) Transition(to FloorState, reason string) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	from := fc.state
	if !fc.transitions[transition{from, to}] {
		return fmt.Errorf("invalid floor transition %s -> %s (reason: %s, mode: %s)",
			from, to, reason, fc.mode)
	}

	fc.state = to
	ev := FloorEvent{
		Timestamp: time.Now(),
		FromState: from,
		ToState:   to,
		AgentID:   fc.activeSpeaker,
		Reason:    reason,
	}

	select {
	case fc.events <- ev:
	default:
	}

	return nil
}

// Events returns a channel that receives FloorEvent values on every transition.
func (fc *FloorController) Events() <-chan FloorEvent {
	return fc.events
}

// ActiveSpeaker returns the ID of the agent currently holding the floor.
func (fc *FloorController) ActiveSpeaker() string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.activeSpeaker
}

// SetActiveSpeaker sets the agent ID for the current speaker.
func (fc *FloorController) SetActiveSpeaker(agentID string) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.activeSpeaker = agentID
	return nil
}

// Mode returns the floor controller's operating mode.
func (fc *FloorController) Mode() string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.mode
}

// forceState is a test helper that sets state without validation.
func (fc *FloorController) forceState(s FloorState) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.state = s
}
