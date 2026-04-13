package voice

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/channel"
)

func TestCommandBridge_InviteAgent(t *testing.T) {
	var got VoiceCommand
	cb := func(cmd VoiceCommand) { got = cmd }

	incoming := make(chan channel.IncomingMessage, 8)
	bridge := NewCommandBridge(cb, NewSentenceBuffer(func(string) {}), incoming, slog.Default())

	bridge.HandleToolCall(ToolCall{
		Name:  "invite_agent",
		Input: json.RawMessage(`{"agent_name":"noah","task_description":"help with animations"}`),
	})

	if got.Type != "invite_agent" {
		t.Errorf("type = %q, want %q", got.Type, "invite_agent")
	}
	if got.AgentName != "noah" {
		t.Errorf("agent = %q, want %q", got.AgentName, "noah")
	}
	if got.TaskDescription != "help with animations" {
		t.Errorf("task = %q, want %q", got.TaskDescription, "help with animations")
	}
}

func TestCommandBridge_DelegateTask(t *testing.T) {
	var got VoiceCommand
	cb := func(cmd VoiceCommand) { got = cmd }

	incoming := make(chan channel.IncomingMessage, 8)
	bridge := NewCommandBridge(cb, NewSentenceBuffer(func(string) {}), incoming, slog.Default())

	bridge.HandleToolCall(ToolCall{
		Name:  "delegate_task",
		Input: json.RawMessage(`{"agent_name":"forge","instruction":"scaffold a new service"}`),
	})

	if got.Type != "delegate_task" {
		t.Errorf("type = %q, want %q", got.Type, "delegate_task")
	}
	if got.AgentName != "forge" {
		t.Errorf("agent = %q, want %q", got.AgentName, "forge")
	}
	if got.Instruction != "scaffold a new service" {
		t.Errorf("instruction = %q, want %q", got.Instruction, "scaffold a new service")
	}

	select {
	case msg := <-incoming:
		if msg.Mention != "forge" {
			t.Errorf("mention = %q, want %q", msg.Mention, "forge")
		}
		if msg.ChannelKind != "voice" {
			t.Errorf("channel kind = %q, want %q", msg.ChannelKind, "voice")
		}
	case <-time.After(time.Second):
		t.Fatal("no message on incoming channel")
	}
}

func TestCommandBridge_CheckStatus(t *testing.T) {
	var got VoiceCommand
	cb := func(cmd VoiceCommand) { got = cmd }

	incoming := make(chan channel.IncomingMessage, 8)
	bridge := NewCommandBridge(cb, NewSentenceBuffer(func(string) {}), incoming, slog.Default())

	bridge.HandleToolCall(ToolCall{
		Name:  "check_status",
		Input: json.RawMessage(`{"scope":"project"}`),
	})

	if got.Type != "check_status" {
		t.Errorf("type = %q, want %q", got.Type, "check_status")
	}
	if got.Scope != "project" {
		t.Errorf("scope = %q, want %q", got.Scope, "project")
	}
}

func TestCommandBridge_UnknownTool(t *testing.T) {
	var called bool
	cb := func(cmd VoiceCommand) { called = true }

	incoming := make(chan channel.IncomingMessage, 8)
	bridge := NewCommandBridge(cb, NewSentenceBuffer(func(string) {}), incoming, slog.Default())

	bridge.HandleToolCall(ToolCall{
		Name:  "unknown_tool",
		Input: json.RawMessage(`{}`),
	})

	if called {
		t.Error("callback should not be called for unknown tools")
	}
}

func TestCommandBridge_NilCallback(t *testing.T) {
	incoming := make(chan channel.IncomingMessage, 8)
	bridge := NewCommandBridge(nil, NewSentenceBuffer(func(string) {}), incoming, slog.Default())

	bridge.HandleToolCall(ToolCall{
		Name:  "invite_agent",
		Input: json.RawMessage(`{"agent_name":"noah","task_description":"test"}`),
	})
}
