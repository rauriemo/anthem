package orchestrator

import (
	"testing"

	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/conduit/pkg/mcpconfig"
)

func TestGuestInvocationMaxTurns(t *testing.T) {
	t.Parallel()
	if n := guestInvocationMaxTurns(false, 0); n != 1 {
		t.Fatalf("no MCP: got %d want 1", n)
	}
	if n := guestInvocationMaxTurns(true, 0); n != defaultGuestMCPMaxTurns {
		t.Fatalf("MCP default: got %d want %d", n, defaultGuestMCPMaxTurns)
	}
	if n := guestInvocationMaxTurns(true, 24); n != 24 {
		t.Fatalf("MCP configured: got %d want 24", n)
	}
}

func TestMergeMCPServersForSelectedGuests(t *testing.T) {
	t.Parallel()
	idx := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"a": {MCPServers: map[string]mcpconfig.MCPServerRef{"u": {Command: "unity"}}},
			"b": {MCPServers: map[string]mcpconfig.MCPServerRef{"ctx": {URL: "http://x"}}},
		},
	}
	global := map[string]mcpconfig.MCPServerRef{"g": {Command: "global"}}

	m := mergeMCPServersForSelectedGuests(global, idx, []string{"a"})
	if len(m) != 2 || m["g"].Command != "global" || m["u"].Command != "unity" {
		t.Fatalf("merge single guest: %#v", m)
	}

	m2 := mergeMCPServersForSelectedGuests(global, idx, []string{"a", "b"})
	if len(m2) != 3 {
		t.Fatalf("merge two guests: want 3 keys, got %d (%#v)", len(m2), m2)
	}

	// Guest overrides global on same name
	idx2 := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"a": {MCPServers: map[string]mcpconfig.MCPServerRef{"g": {URL: "http://override"}}},
		},
	}
	m3 := mergeMCPServersForSelectedGuests(global, idx2, []string{"a"})
	if m3["g"].URL != "http://override" {
		t.Fatalf("guest should override global: %#v", m3["g"])
	}
}
