package orchestrator

import (
	"context"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/types"
)

// DefaultInteractiveDeniedTools is the baseline deny list injected
// into every Run/Continue call that is not in ModeLoop. It blocks
// the remote-mutation surfaces we know of: GitHub CLI, raw HTTP
// (curl/wget, PowerShell's Invoke-*), Claude's built-in WebFetch
// tool, and the official GitHub MCP server.
//
// Ordered most-common to least so Claude Code's deny-log reasons
// stay readable when an agent tries to reach for a blocked tool.
//
// Adding an entry here tightens the Chat/Plan/Execute envelope
// with zero risk to Loop mode: the wrapper returns early in Loop
// and forwards opts unchanged.
//
// NOTE: Claude Code honors --disallowedTools even under
// --dangerously-skip-permissions. This is the whole reason Layer 2
// works -- we keep bypassPermissions for local-file tool smoothness
// while denying remote-mutation tools by name.
var DefaultInteractiveDeniedTools = []string{
	"Bash(gh *)",
	"Bash(curl *)",
	"Bash(wget *)",
	"Bash(Invoke-RestMethod*)",
	"Bash(Invoke-WebRequest*)",
	"WebFetch",
	"mcp__github__*",
}

// modeGatedRunner wraps an agent.AgentRunner so every Run/Continue
// call made while the orchestrator is in Chat/Plan/Execute mode
// gets DefaultInteractiveDeniedTools appended to the existing
// DeniedTools slice. In ModeLoop the wrapper is a pass-through.
//
// This is Layer 2 of the three-layer route-isolation boundary
// (see tracker_gate.go for the full layer map). It intentionally
// does not touch PermissionMode -- bypassPermissions remains in
// effect so Claude can edit files and run local builds; only
// remote-mutation tools get denied.
//
// The wrapper never mutates the caller's slice. It copies
// opts.DeniedTools into a fresh slice and appends the defaults
// there; the caller's underlying array is untouched so a caller
// who reuses a RunOpts across calls won't accumulate denies.
type modeGatedRunner struct {
	inner agent.AgentRunner
	mode  func() types.Mode
}

// newModeGatedRunner wraps inner with a mode gate. The mode fn is
// evaluated on every call so mode transitions during the life of
// the orchestrator take effect immediately with no re-wrap.
func newModeGatedRunner(inner agent.AgentRunner, mode func() types.Mode) *modeGatedRunner {
	return &modeGatedRunner{inner: inner, mode: mode}
}

// Run forwards to the inner runner. When CurrentMode != ModeLoop
// it appends DefaultInteractiveDeniedTools to a fresh copy of
// opts.DeniedTools before forwarding; in ModeLoop it forwards
// opts unchanged.
func (r *modeGatedRunner) Run(ctx context.Context, opts types.RunOpts) (*types.RunResult, error) {
	if r.mode() != types.ModeLoop {
		opts.DeniedTools = appendDefaultDenies(opts.DeniedTools)
	}
	return r.inner.Run(ctx, opts)
}

// Continue forwards to the inner runner. Same mode-gated deny
// injection as Run, against ContinueOpts.DeniedTools.
func (r *modeGatedRunner) Continue(ctx context.Context, sessionID string, prompt string, opts types.ContinueOpts) (*types.RunResult, error) {
	if r.mode() != types.ModeLoop {
		opts.DeniedTools = appendDefaultDenies(opts.DeniedTools)
	}
	return r.inner.Continue(ctx, sessionID, prompt, opts)
}

// Kill forwards unchanged. Killing a process isn't a tool call;
// there's nothing to gate.
func (r *modeGatedRunner) Kill(pid int) error {
	return r.inner.Kill(pid)
}

// appendDefaultDenies returns a fresh slice that contains existing
// entries followed by DefaultInteractiveDeniedTools. Never mutates
// existing.
func appendDefaultDenies(existing []string) []string {
	out := make([]string, 0, len(existing)+len(DefaultInteractiveDeniedTools))
	out = append(out, existing...)
	out = append(out, DefaultInteractiveDeniedTools...)
	return out
}
