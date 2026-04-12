package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/voice"
)

// respecSession tracks an active respec flow for a specific channel.
type respecSession struct {
	TargetFile string // e.g. "agents/orchestrator.md"
	AgentName  string // e.g. "orchestrator" or "miyazaki"
	SessionID  string // Claude session ID for multi-turn continuity
}

// respecStreamFilter suppresses JSON action blocks and fenced code blocks from
// the stream so the user only sees conversational text. It tracks brace depth
// across incremental deltas -- when depth > 0 the content is swallowed.
type respecStreamFilter struct {
	depth  int
	inCode bool // inside a ``` fence
	emit   func(string)
}

func newRespecStreamFilter(emit func(string)) *respecStreamFilter {
	return &respecStreamFilter{emit: emit}
}

func (f *respecStreamFilter) Write(delta string) {
	var out strings.Builder
	for _, ch := range delta {
		if ch == '`' {
			f.inCode = !f.inCode
			if f.depth == 0 {
				out.WriteRune(ch)
			}
			continue
		}
		if ch == '{' && !f.inCode {
			f.depth++
			continue
		}
		if ch == '}' && !f.inCode && f.depth > 0 {
			f.depth--
			continue
		}
		if f.depth > 0 {
			continue
		}
		out.WriteRune(ch)
	}
	if out.Len() > 0 {
		f.emit(out.String())
	}
}

// parseRespecTarget extracts the target from a [system:respec] tagged message.
// Returns "cancel" for [system:respec:cancel], a name for [system:respec:NAME],
// or "" for bare [system:respec] (meaning orchestrator).
func parseRespecTarget(text string) string {
	re := regexp.MustCompile(`\[system:respec(?::([^\]]*))?\]`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// hasActiveRespec reports whether the given channel has an active respec session.
func (o *Orchestrator) hasActiveRespec(channelKey string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.activeRespecs == nil {
		return false
	}
	_, ok := o.activeRespecs[channelKey]
	return ok
}

// handleRespecMessage is the main entry point for /respec flow messages.
func (o *Orchestrator) handleRespecMessage(ctx context.Context, msg channel.IncomingMessage, model string) {
	channelKey := msg.ChannelKind

	// Cancel handling
	if strings.Contains(msg.Text, "[system:respec:cancel]") {
		o.mu.Lock()
		delete(o.activeRespecs, channelKey)
		o.mu.Unlock()
		o.sendFollowUp(ctx, msg, "Respec canceled. Changes so far have been saved.")
		return
	}

	// Check for conflict: new [system:respec] tag while a session is already active
	if strings.Contains(msg.Text, "[system:respec") {
		o.mu.Lock()
		existing := o.activeRespecs[channelKey]
		o.mu.Unlock()
		if existing != nil {
			o.sendFollowUp(ctx, msg, "A respec is already in progress. Finish it or `/respec cancel` first.")
			return
		}
	}

	// If this is a continuation (no [system:respec] tag), route to active session
	o.mu.Lock()
	session := o.activeRespecs[channelKey]
	o.mu.Unlock()

	if session != nil && !strings.Contains(msg.Text, "[system:respec") {
		o.continueRespec(ctx, msg, model, session)
		return
	}

	// New respec flow: parse target
	target := parseRespecTarget(msg.Text)
	if target == "cancel" {
		o.sendFollowUp(ctx, msg, "No active respec to cancel.")
		return
	}

	var agentName, targetFile string
	if target == "" || target == "orchestrator" {
		agentName = "orchestrator"
		targetFile = filepath.Join(o.projectRoot(), "agents", "orchestrator.md")
	} else {
		if err := validateAgentFile(target + ".md"); err != nil {
			o.sendFollowUp(ctx, msg, fmt.Sprintf("Invalid agent name: %s", err))
			return
		}
		agentName = target
		targetFile = filepath.Join(o.projectRoot(), "agents", target+".md")
	}

	// Load current state
	currentFM, fmErr := guests.LoadAgentFrontmatter(targetFile)
	if fmErr != nil {
		o.logger.Warn("respec: failed to load frontmatter", "file", targetFile, "error", fmErr)
		currentFM = make(map[string]any)
	}

	currentVC, _ := voice.LoadFile(targetFile)
	var currentBody string
	if currentVC != nil {
		currentBody = currentVC.Raw
	}

	isNew := len(currentFM) == 0 && currentBody == ""
	userContext := o.voiceContent

	// Build the respec system prompt
	systemPrompt := buildRespecSystemPrompt(agentName, currentBody, currentFM, isNew, userContext)

	// Create/start session
	sess := &respecSession{
		TargetFile: targetFile,
		AgentName:  agentName,
	}
	o.mu.Lock()
	if o.activeRespecs == nil {
		o.activeRespecs = make(map[string]*respecSession)
	}
	o.activeRespecs[channelKey] = sess
	o.mu.Unlock()

	if o.orchAgent == nil {
		o.sendFollowUp(ctx, msg, "Respec requires the orchestrator agent to be configured.")
		o.mu.Lock()
		delete(o.activeRespecs, channelKey)
		o.mu.Unlock()
		return
	}

	cleanText := strings.Replace(msg.Text, "[system:respec]", "", 1)
	cleanText = regexp.MustCompile(`\[system:respec:[^\]]*\]`).ReplaceAllString(cleanText, "")
	cleanText = strings.TrimSpace(cleanText)
	if cleanText == "" || strings.EqualFold(cleanText, "Start respec") || strings.EqualFold(cleanText, "Start respec for "+agentName) {
		cleanText = "Begin the respec."
	}

	streamFilter := newRespecStreamFilter(func(delta string) {
		if o.channelMgr != nil {
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				StreamDelta: delta,
				ThreadID:    msg.ThreadID,
				StreamKind:  "chat",
			})
		}
	})

	result, err := o.orchAgent.runner.Run(ctx, types.RunOpts{
		Prompt:         systemPrompt + "\n\nUser: " + cleanText,
		Model:          model,
		PermissionMode: "bypassPermissions",
		MaxTurns:       o.orchAgent.planMaxTurns,
		OnStream:       streamFilter.Write,
	})
	if err != nil {
		o.logger.Warn("respec: initial LLM call failed", "error", err)
		o.sendFollowUp(ctx, msg, "Respec failed to start. Please try again.")
		o.mu.Lock()
		delete(o.activeRespecs, channelKey)
		o.mu.Unlock()
		return
	}

	o.orchAgent.recordResult(result)
	sess.SessionID = result.SessionID

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
			StreamKind: "chat",
		})
	}

	o.executeRespecActions(ctx, msg, result.Output, sess)
}

// continueRespec routes a user's follow-up answer to the active respec session.
func (o *Orchestrator) continueRespec(ctx context.Context, msg channel.IncomingMessage, model string, sess *respecSession) {
	if o.orchAgent == nil || sess.SessionID == "" {
		o.sendFollowUp(ctx, msg, "Respec session lost. Use `/respec` to start again.")
		o.mu.Lock()
		delete(o.activeRespecs, msg.ChannelKind)
		o.mu.Unlock()
		return
	}

	streamFilter := newRespecStreamFilter(func(delta string) {
		if o.channelMgr != nil {
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				StreamDelta: delta,
				ThreadID:    msg.ThreadID,
				StreamKind:  "chat",
			})
		}
	})

	result, err := o.orchAgent.runner.Continue(ctx, sess.SessionID, msg.Text, types.ContinueOpts{
		PermissionMode: "bypassPermissions",
		OnStream:       streamFilter.Write,
	})
	if err != nil {
		o.logger.Warn("respec: continue failed", "error", err)
		o.sendFollowUp(ctx, msg, "Respec encountered an error. Your changes so far are saved. Use `/respec` to start again.")
		o.mu.Lock()
		delete(o.activeRespecs, msg.ChannelKind)
		o.mu.Unlock()
		return
	}

	o.orchAgent.recordResult(result)
	sess.SessionID = result.SessionID

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
			StreamKind: "chat",
		})
	}

	o.executeRespecActions(ctx, msg, result.Output, sess)
}

// executeRespecActions parses any JSON actions in the LLM output and executes
// them immediately (write-per-phase). If the output signals completion, the
// session is cleared.
func (o *Orchestrator) executeRespecActions(ctx context.Context, msg channel.IncomingMessage, output string, sess *respecSession) {
	actions, _, err := parseActions(output)
	if err != nil {
		return
	}

	var metaUpdated bool
	var lastMetaAction Action
	for _, action := range actions {
		switch action.Type {
		case ActionUpdateAgentMeta:
			if action.AgentFile == "" {
				action.AgentFile = filepath.Base(sess.TargetFile)
			}
			if err := o.executeUpdateAgentMeta(ctx, action); err != nil {
				o.logger.Warn("respec: update_agent_meta failed", "error", err)
			} else {
				metaUpdated = true
				lastMetaAction = action
			}
		case ActionUpdateVoice:
			if action.AgentFile == "" {
				action.AgentFile = filepath.Base(sess.TargetFile)
			}
			if err := o.executeUpdateVoice(ctx, action); err != nil {
				o.logger.Warn("respec: update_voice failed", "error", err)
			}
		}
	}

	if metaUpdated {
		o.scanGuests()

		if o.channelMgr != nil && lastMetaAction.AgentName != "" {
			guestID := ""
			if lastMetaAction.AgentFile != "orchestrator.md" {
				guestID = strings.TrimSuffix(lastMetaAction.AgentFile, ".md")
			}
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				EventType: "agent_meta_updated",
				Text:      lastMetaAction.AgentName,
				GuestID:   guestID,
			})
		}
	}

	if strings.Contains(strings.ToLower(output), "respec complete") {
		o.mu.Lock()
		delete(o.activeRespecs, msg.ChannelKind)
		o.mu.Unlock()
	}
}

// buildRespecSystemPrompt constructs the system prompt for the respec conversation.
func buildRespecSystemPrompt(agentName string, currentBody string, currentFM map[string]any, isNew bool, userContext string) string {
	var b strings.Builder

	b.WriteString("## Role\n\n")
	if agentName == "orchestrator" {
		b.WriteString("You are respeccing this project's orchestrator agent. Guide the user through defining who this agent is and how it should behave.\n\n")
	} else {
		b.WriteString(fmt.Sprintf("You are respeccing the specialist agent \"%s\". Guide the user through defining this agent's identity, personality, scope, and coordination.\n\n", agentName))
	}

	if userContext != "" {
		b.WriteString("## User Context\n\n")
		b.WriteString(userContext)
		b.WriteString("\n\n")
	}

	if !isNew {
		b.WriteString("## Current Agent State\n\n")
		b.WriteString("This agent already exists. Show current values for each phase and ask what the user wants to change. If they say 'keep', skip that section.\n\n")
		if len(currentFM) > 0 {
			b.WriteString("### Current Frontmatter\n\n")
			for k, v := range currentFM {
				b.WriteString(fmt.Sprintf("- %s: %v\n", k, v))
			}
			b.WriteString("\n")
		}
		if currentBody != "" {
			b.WriteString("### Current Body\n\n")
			b.WriteString(currentBody)
			b.WriteString("\n\n")
		}
	}

	b.WriteString(`## Instructions

Guide the user through these phases, presenting 2-4 related questions per turn. After the user answers each phase, immediately emit JSON actions to write those sections. Do not wait until the end.

### Phase 1 — Core Identity
Ask about: name, one-sentence description, role (orchestrator/engineer/designer/writer/artist/scene-editor...), specialty or superpower.
After answers: emit an update_agent_meta action with agent_file, agent_name, agent_description, agent_role.

### Phase 2 — Personality
Ask about: communication style (direct/verbose/humorous/formal/casual), defining traits (opinionated/cautious/experimental), humor style, things they should never do.
After answers: emit an update_voice action writing a "## Personality" section to the agent file.

### Phase 3 — Focus & Scope
Ask about: primary mission, capabilities to advertise.`)

	if agentName != "orchestrator" {
		b.WriteString("\nAlso ask: what do they own, what they explicitly do NOT own (who handles that instead).")
	}

	b.WriteString(`
Ask about: anything they should never touch.
After answers: emit update_voice for "## Your Focus" and optionally update_agent_meta with agent_capabilities.

### Phase 4 — Coordination`)

	if agentName == "orchestrator" {
		b.WriteString(`
Ask about: which specialist agents exist or are planned, how delegation should work, any coordination rules.
After answers: emit update_voice for "## Coordination".`)
	} else {
		b.WriteString(`
Ask about: who their peers are, how handoffs work (files/artifacts/mentions), any coordination rules or boundaries.
After answers: emit update_voice for "## Coordination".`)
	}

	b.WriteString(`

### Phase 5 — Flavor (Optional)
Ask if the user wants to add finishing touches: characteristic quotes, an icon for the UI, creative inspirations.
After answers: emit update_agent_meta with agent_icon and/or agent_quotes if provided.

## Completion

After all phases are written, say "Respec complete!" and give a brief summary. When you say this, the session ends automatically.

## Action Format

Emit actions as a JSON object with "reasoning" and "actions" array:
{"reasoning": "...", "actions": [{"type": "update_agent_meta", "agent_file": "NAME.md", "agent_name": "...", ...}, {"type": "update_voice", "agent_file": "NAME.md", "section_name": "Personality", "section_content": "..."}]}

For update_voice with agent_file set, ALL sections go to that agent's file (not routed through the normal orchestrator/VOICE split).

## Key Rules

- Write changes immediately after each phase. Do not batch them at the end.
- If the user says "keep" for a phase, do NOT emit actions for it.
- Keep your questions conversational and friendly, not like a form.
- Present 2-4 questions per phase, not one at a time.
- Always set agent_file on every action so writes target the correct file.
`)

	return b.String()
}
