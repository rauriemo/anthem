package voice

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/rauriemo/anthem/internal/channel"
)

// CommandCallback is invoked when the FastLLM returns a tool_use that maps
// to an Anthem action (invite, delegate, etc.).
type CommandCallback func(cmd VoiceCommand)

// VoiceCommand represents a structured command extracted from a tool_use.
type VoiceCommand struct {
	Type string // "invite_agent", "delegate_task", "check_status"

	AgentName       string
	TaskDescription string
	Instruction     string
	Scope           string
	StatusText      string // populated by check_status handler
}

// CommandBridge translates FastLLM tool calls into VoiceCommand values and
// dispatches them through a callback. It also handles the check_status tool
// by returning text to be spoken via TTS.
type CommandBridge struct {
	callback CommandCallback
	sentBuf  *SentenceBuffer
	incoming chan<- channel.IncomingMessage
	logger   *slog.Logger
}

// NewCommandBridge creates a command bridge. The sentBuf is used to pipe
// check_status results to TTS. The incoming channel is used for delegate_task
// messages that route through the orchestrator.
func NewCommandBridge(cb CommandCallback, sentBuf *SentenceBuffer, incoming chan<- channel.IncomingMessage, logger *slog.Logger) *CommandBridge {
	return &CommandBridge{
		callback: cb,
		sentBuf:  sentBuf,
		incoming: incoming,
		logger:   logger,
	}
}

// HandleToolCall processes a tool_use event from FastLLM.
func (b *CommandBridge) HandleToolCall(tc ToolCall) {
	switch tc.Name {
	case "invite_agent":
		b.handleInviteAgent(tc.Input)
	case "delegate_task":
		b.handleDelegateTask(tc.Input)
	case "check_status":
		b.handleCheckStatus(tc.Input)
	default:
		b.logger.Warn("unknown voice tool call", "name", tc.Name)
	}
}

func (b *CommandBridge) handleInviteAgent(input json.RawMessage) {
	var args struct {
		AgentName       string `json:"agent_name"`
		TaskDescription string `json:"task_description"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		b.logger.Warn("failed to parse invite_agent input", "error", err)
		return
	}

	cmd := VoiceCommand{
		Type:            "invite_agent",
		AgentName:       args.AgentName,
		TaskDescription: args.TaskDescription,
	}
	if b.callback != nil {
		b.callback(cmd)
	}
}

func (b *CommandBridge) handleDelegateTask(input json.RawMessage) {
	var args struct {
		AgentName   string `json:"agent_name"`
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		b.logger.Warn("failed to parse delegate_task input", "error", err)
		return
	}

	cmd := VoiceCommand{
		Type:        "delegate_task",
		AgentName:   args.AgentName,
		Instruction: args.Instruction,
	}
	if b.callback != nil {
		b.callback(cmd)
	}

	msg := channel.IncomingMessage{
		ChannelKind: "voice",
		SenderID:    "user",
		Text:        fmt.Sprintf("@%s %s", args.AgentName, args.Instruction),
		Mention:     args.AgentName,
	}
	select {
	case b.incoming <- msg:
	default:
		b.logger.Warn("dropping delegate_task message, buffer full")
	}
}

func (b *CommandBridge) handleCheckStatus(input json.RawMessage) {
	var args struct {
		Scope string `json:"scope"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		b.logger.Warn("failed to parse check_status input", "error", err)
		return
	}

	cmd := VoiceCommand{
		Type:  "check_status",
		Scope: args.Scope,
	}
	if b.callback != nil {
		b.callback(cmd)
	}
}
