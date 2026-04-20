package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/harness"
	"github.com/rauriemo/anthem/internal/plans"
	"github.com/rauriemo/anthem/internal/prompt"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/conduit/pkg/mcpconfig"
)

type GuestSummary struct {
	ID          string
	Name        string
	Description string
}

type RoutingResult struct {
	Guests              []string          `json:"guests"`
	DirectedText        map[string]string `json:"directed_text,omitempty"`
	IncludeOrchestrator bool              `json:"include_orchestrator"`
	ContextUpdate       string            `json:"context_update"`
}

// GuestPromptOpts retains the legacy field set used by the chat
// dispatch paths and by the extensive guestdispatch_test.go suite. It
// is translated into prompt.GuestPromptOpts + prompt.LiveContext by
// buildGuestPrompt below, so callers in orchestrator do not need to
// know about the new prompt package.
type GuestPromptOpts struct {
	Mode           types.Mode
	ChannelKind    string
	PlanContent    string
	StoryContext   *StoryContext
	FocusText      string
	FeatureContext string
	UserContext    string
}

var planEditBlockRe = regexp.MustCompile("(?s)```plan-edit\\s*\\n(.*?)\\n```")

// defaultGuestMCPMaxTurns is used when orchestrator.guest_mcp_max_turns is unset (≤0).
// Kept for backwards-compatible tests that exercise the legacy helper.
const defaultGuestMCPMaxTurns = 16

// guestInvocationMaxTurns is a thin backwards-compat wrapper over
// guests.ResolveMaxTurns. New call sites should pass the GuestAgent
// directly to ResolveMaxTurns so per-agent max_turns overrides take
// effect. This helper resolves as if no override were declared.
func guestInvocationMaxTurns(mcpActive bool, configured int) int {
	return guests.ResolveMaxTurns(guests.GuestAgent{}, mcpActive, configured)
}

// mergeMCPServersForSelectedGuests merges anthem global mcp_servers with every
// selected guest's frontmatter mcp_servers (guest keys override global on collision).
// projectRoot is forwarded to HTTPToolsToMCPServers so the bridge can sandbox file reads.
func mergeMCPServersForSelectedGuests(global map[string]mcpconfig.MCPServerRef, guestIndex *guests.GuestIndex, selectedIDs []string, projectRoot string) map[string]mcpconfig.MCPServerRef {
	if guestIndex == nil {
		if len(global) == 0 {
			return nil
		}
		out := make(map[string]mcpconfig.MCPServerRef, len(global))
		for k, v := range global {
			out[k] = v
		}
		return out
	}
	merged := make(map[string]mcpconfig.MCPServerRef)
	for k, v := range global {
		merged[k] = v
	}
	for _, gid := range selectedIDs {
		ag, ok := guestIndex.Agents[gid]
		if !ok {
			continue
		}
		for k, v := range ag.MCPServers {
			merged[k] = v
		}
		for k, v := range harness.HTTPToolsToMCPServers(ag.HTTPTools, projectRoot) {
			merged[k] = v
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func routeToGuests(
	ctx context.Context,
	runner agent.AgentRunner,
	userMsg string,
	activeGuests []GuestSummary,
	history, sharedCtx string,
	logger *slog.Logger,
) *RoutingResult {
	var sb strings.Builder
	sb.WriteString("You are a routing assistant. Given a user message, select which specialist agents should respond.\n\n")
	sb.WriteString("## Active Specialists\n")
	for _, g := range activeGuests {
		fmt.Fprintf(&sb, "- %s (%s): %s\n", g.ID, g.Name, g.Description)
	}

	if sharedCtx != "" {
		sb.WriteString("\n## Session Context\n")
		sb.WriteString(sharedCtx)
		sb.WriteString("\n")
	}
	if history != "" {
		sb.WriteString("\n")
		sb.WriteString(history)
	}

	fmt.Fprintf(&sb, "\n## User Message\n%s\n", userMsg)
	sb.WriteString("\nRespond with JSON only:\n")
	sb.WriteString(`{"guests": ["id1", ...], "directed_text": {"id1": "extracted instruction", ...}, "include_orchestrator": false, "context_update": "..."}`)
	sb.WriteString("\n\nSelect guests whose expertise is relevant. Return empty guests array if none are relevant.\n")
	sb.WriteString("If the user addresses different specialists by name or clear intent, include a \"directed_text\" map where each key is a guest ID and the value is the specific instruction extracted verbatim from the user's message. ")
	sb.WriteString("Do not paraphrase — extract the user's actual words. If the message is general (not directed at specific specialists), omit \"directed_text\" entirely.\n")
	sb.WriteString("Also decide whether the host orchestrator should respond alongside the specialists. ")
	sb.WriteString("Set include_orchestrator to true ONLY if the message requires task management, system-level actions, or information the specialists cannot provide. ")
	sb.WriteString("For general conversation, creative work, and domain questions, set it to false — the specialists are the primary responders.\n")
	sb.WriteString("The context_update should summarize key decisions and facts from this conversation so far.\n")

	result, err := runner.Run(ctx, types.RunOpts{
		Prompt:         sb.String(),
		Model:          "claude-haiku-4-5",
		MaxTurns:       1,
		PermissionMode: "bypassPermissions",
	})
	if err != nil {
		logger.Warn("routing call failed, falling back to all guests", "error", err)
		return fallbackAllGuests(activeGuests)
	}

	var routing RoutingResult
	text := extractJSON(result.Output)
	if err := json.Unmarshal([]byte(text), &routing); err != nil {
		logger.Warn("routing call returned invalid JSON, falling back to all guests", "output", result.Output[:min(len(result.Output), 200)])
		return fallbackAllGuests(activeGuests)
	}

	return &routing
}

func fallbackAllGuests(activeGuests []GuestSummary) *RoutingResult {
	ids := make([]string, len(activeGuests))
	for i, g := range activeGuests {
		ids[i] = g.ID
	}
	return &RoutingResult{Guests: ids}
}

func updateContextAfterRound(
	ctx context.Context,
	runner agent.AgentRunner,
	history, currentSharedCtx string,
	sharedCtx *SharedContext,
	channelKey string,
	logger *slog.Logger,
) {
	var sb strings.Builder
	sb.WriteString("You are a session summarizer. Given the recent conversation history and the current session context, produce an updated session knowledge summary.\n\n")
	if currentSharedCtx != "" {
		sb.WriteString("## Current Session Context\n")
		sb.WriteString(currentSharedCtx)
		sb.WriteString("\n\n")
	}
	sb.WriteString(history)
	sb.WriteString("\nProduce a concise updated session summary capturing key decisions, facts, and ongoing topics. Plain text only, no JSON wrapper.\n")

	result, err := runner.Run(ctx, types.RunOpts{
		Prompt:         sb.String(),
		Model:          "claude-haiku-4-5",
		MaxTurns:       1,
		PermissionMode: "bypassPermissions",
	})
	if err != nil {
		logger.Warn("context update call failed", "error", err)
		return
	}
	if result.Output != "" {
		sharedCtx.Update(channelKey, strings.TrimSpace(result.Output))
	}
}

// buildGuestPrompt is a thin adapter over prompt.BuildGuestPrompt that
// preserves the long-standing signature used by orchestrator call sites
// and by guestdispatch_test.go. The actual assembly logic lives in the
// internal/prompt package so the execute runner can share it without
// dragging the orchestrator's feature-context yaml types into a chained
// step's dependency graph.
//
// Character Commitment is always on for chat/plan dispatch here, per
// plan decision D7 (parity with Execute).
func buildGuestPrompt(persona, projectSummary, sharedCtx, history, userMsg string, opts GuestPromptOpts) string {
	live := prompt.LiveContext{
		UserContext:    opts.UserContext,
		ProjectSummary: projectSummary,
		FeatureContext: opts.FeatureContext,
		SharedCtxText:  sharedCtx,
		HistoryText:    history,
	}
	return prompt.BuildGuestPrompt(persona, live, userMsg, prompt.GuestPromptOpts{
		Mode:                       opts.Mode,
		ChannelKind:                opts.ChannelKind,
		PlanContent:                opts.PlanContent,
		StoryContextText:           renderStoryContextText(opts.StoryContext),
		FocusText:                  opts.FocusText,
		IncludeCharacterCommitment: true,
	})
}

// renderStoryContextText flattens a *StoryContext into the "Story
// Bible" markdown block the prompt package stitches into the final
// output. Kept in orchestrator because StoryContext is sourced from
// orchestrator's StoryStore and the prompt package deliberately stays
// string-only.
func renderStoryContextText(sc *StoryContext) string {
	if sc == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Story Bible\n\n")
	sb.WriteString("### Project Context\n")
	sb.WriteString(sc.Config)
	sb.WriteString("\n\n")
	for name, content := range sc.Files {
		fmt.Fprintf(&sb, "### %s\n\n%s\n\n", name, content)
	}
	sb.WriteString("### Section Hashes\n\n")
	sb.WriteString("| section_id | file | rev |\n|---|---|---|\n")
	for file, secs := range sc.Sections {
		for id, hash := range secs {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", id, file, hash)
		}
	}
	sb.WriteString("\n## Story Edit Instructions\n\n")
	sb.WriteString("You are the narrative engine for this project.\n")
	sb.WriteString("Your edits are PROPOSALS -- the user accepts or rejects each one.\n")
	sb.WriteString("- To REPLACE: ```story-edit target=\"file.md\" section=\"id\" rev=\"hash\"\n")
	sb.WriteString("- To INSERT: ```story-edit target=\"file.md\" after=\"id\"\n")
	sb.WriteString("- To APPEND: ```story-edit target=\"file.md\" (no section or after)\n")
	sb.WriteString("- New sections MUST include <!-- id: snake_case --> anchor.\n")
	sb.WriteString("- To DISCUSS: respond normally, no fenced block.\n\n")
	return sb.String()
}

func extractPlanEdit(response string) (explanation, planMarkdown string, hasEdit bool) {
	loc := planEditBlockRe.FindStringIndex(response)
	if loc == nil {
		return response, "", false
	}

	matches := planEditBlockRe.FindStringSubmatch(response)
	if len(matches) < 2 {
		return response, "", false
	}

	planMarkdown = strings.TrimSpace(matches[1])
	explanation = strings.TrimSpace(response[:loc[0]] + response[loc[1]:])
	return explanation, planMarkdown, true
}

type guestDispatchParams struct {
	ctx              context.Context
	selectedIDs      []string
	msg              channel.IncomingMessage
	guestsDir        string
	runner           agent.AgentRunner
	sharedCtx        *SharedContext
	convoBuf         *ConvoBuffer
	channelMgr       *channel.Manager
	projectSummary   string
	featureContext   string
	userContext      string
	mode             types.Mode
	channelKind      string
	planContent      string
	planStore        *plans.Store
	planSlug         string
	guestIndex       *guests.GuestIndex
	storyStore       *StoryStore
	proposalStore    *ProposalStore
	directedText     map[string]string
	logger           *slog.Logger
	globalMCPServers map[string]mcpconfig.MCPServerRef
	projectRoot      string
	activeFeature    string
	guestMCPMaxTurns int
}

var planEditMu sync.Mutex

func dispatchSelectedGuests(p guestDispatchParams) {
	mcpRoundActive := false
	if p.guestIndex != nil {
		mergedMCP := mergeMCPServersForSelectedGuests(p.globalMCPServers, p.guestIndex, p.selectedIDs, p.projectRoot)
		mcpRoundActive = len(mergedMCP) > 0
		if mcpRoundActive && p.projectRoot != "" {
			if err := harness.WriteMCPConfig(p.projectRoot, mergedMCP); err != nil {
				p.logger.Warn("failed to write guest MCP config", "error", err)
			}
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)

	channelKey := p.msg.ChannelKind
	rounds := p.convoBuf.History(channelKey)
	sharedCtxText := p.sharedCtx.Get(channelKey)

	for _, guestID := range p.selectedIDs {
		guestID := guestID

		var agent guests.GuestAgent
		if p.guestIndex != nil {
			agent = p.guestIndex.Agents[guestID]
		}
		if agent.Name == "" {
			agent.Name = guestID
		}

		persona, err := guests.LoadPersona(p.guestsDir, guestID)
		if err != nil {
			p.logger.Warn("failed to load guest persona", "guest", guestID, "error", err)
			if p.channelMgr != nil {
				_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
					Text:     fmt.Sprintf("[%s failed to load]", agent.Name),
					GuestID:  guestID,
					ThreadID: p.msg.ThreadID,
				})
			}
			continue
		}

		model := agent.Model
		if model == "" {
			model = "claude-sonnet-4-5"
		}

		var storyCtx *StoryContext
		if agent.Role == "writer" && p.storyStore != nil {
			sc, err := p.storyStore.ReadContext()
			if err == nil {
				storyCtx = &sc
			}
		}

		isFirstTurn := !p.convoBuf.HasGuestSpoken(channelKey, guestID)
		var history string
		if isFirstTurn {
			// Onboarding sanitization: a newly-joined guest should not
			// receive the raw user history verbatim. The earlier behavior
			// piped an expanded transcript including planning chatter,
			// which was a leak vector — a guest asked to "draft sprites"
			// on the first turn could read the user's whole task list and
			// silently start executing it with tools.
			//
			// Known tradeoff: stripping raw history reduces first-turn
			// usefulness since the guest has near-zero context. This is
			// the safer default; a future enhancement (see the plan's
			// "Out of scope" section) swaps this preamble for a small
			// orchestrator-curated summary that gives context without
			// re-exposing tool-bearing instructions.
			history = "## Onboarding\n\n" +
				"You were just added to this conversation. Introduce yourself briefly (one sentence, in character) and wait for direction from the user before taking any action. " +
				"Do not execute tools on this turn. Do not assume any task from prior conversation — ask if you are unsure what the user needs from you.\n"
		} else {
			history = FormatHistory(rounds)
		}

		focusText := ""
		if dt, ok := p.directedText[guestID]; ok && dt != "" {
			focusText = dt
		}

		prompt := buildGuestPrompt(persona, p.projectSummary, sharedCtxText, history, p.msg.Text, GuestPromptOpts{
			Mode:           p.mode,
			ChannelKind:    p.channelKind,
			PlanContent:    p.planContent,
			StoryContext:   storyCtx,
			FocusText:      focusText,
			FeatureContext: p.featureContext,
			UserContext:    p.userContext,
		})

		var allowedTools []string
		if p.guestIndex != nil {
			allowedTools = resolveGuestTools(p.guestIndex.Agents[guestID])
		}

		// Plan mode: guests can reply and emit plan-edit blocks, but
		// must not invoke ANY tools (no MCP, no HTTP, no Bash/Edit).
		// We strip allowedTools to an explicit empty slice (not nil —
		// nil means "apply caller defaults" in some paths) AND set
		// RunOpts.ToolsDisabled as a belt-and-suspenders guarantee
		// the harness forwards `--disallowedTools '*'` regardless of
		// any other tool-resolution layer. See L2 of the mode-aware
		// guest activation plan for rationale.
		toolsDisabled := p.mode == types.ModePlan
		if toolsDisabled {
			allowedTools = []string{}
		}

		guestMaxTurns := guests.ResolveMaxTurns(agent, mcpRoundActive, p.guestMCPMaxTurns)

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var fullText strings.Builder
			streamed := false
			onStream := func(delta string) {
				fullText.WriteString(delta)
				streamed = true
				if p.channelMgr != nil {
					_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
						StreamDelta: delta,
						GuestID:     guestID,
						ThreadID:    p.msg.ThreadID,
					})
				}
			}

			runOpts := types.RunOpts{
				Prompt:         prompt,
				Model:          model,
				MaxTurns:       guestMaxTurns,
				PermissionMode: "bypassPermissions",
				OnStream:       onStream,
				ToolsDisabled:  toolsDisabled,
			}
			if len(allowedTools) > 0 {
				runOpts.AllowedTools = allowedTools
			}

			if p.activeFeature != "" {
				if stErr := SetTaskActive(p.projectRoot, p.activeFeature, guestID, p.msg.Text); stErr != nil {
					p.logger.Warn("failed to set task-state active", "guest", guestID, "error", stErr)
				}
			}

			result, err := p.runner.Run(p.ctx, runOpts)

			if p.activeFeature != "" {
				output := ""
				if result != nil && result.Output != "" {
					output = truncateOutput(result.Output, 200)
				}

				var stateOpts TaskStateUpdate
				rawOutput := ""
				if result != nil {
					rawOutput = result.Output
				}
				report, parseErr := ParseContextReport(rawOutput)
				if parseErr != nil {
					p.logger.Warn("failed to parse context_report", "guest", guestID, "error", parseErr)
				}

				if report != nil {
					if report.Artifact != nil {
						art := ArtifactEntry{
							ID:          report.Artifact.ID,
							Type:        report.Artifact.Type,
							Path:        report.Artifact.Path,
							CreatedBy:   guestID,
							CreatedAt:   time.Now().UTC().Format(time.RFC3339),
							Feature:     p.activeFeature,
							Status:      report.Artifact.Status,
							Description: report.Artifact.Description,
							Tags:        report.Artifact.Tags,
							Metadata:    report.Artifact.Metadata,
							DependsOn:   report.Artifact.DependsOn,
						}
						if art.Status == "" {
							art.Status = "draft"
						}
						if aErr := AppendArtifact(p.projectRoot, p.activeFeature, art, ChatOrigin{AgentID: guestID}); aErr != nil {
							p.logger.Warn("failed to register artifact", "guest", guestID, "error", aErr)
						}
					}

					action := report.Action
					if action == "" {
						action = "task_completed"
					}
					summary := report.Summary
					if summary == "" {
						summary = output
					}
					logEntry := ChangelogEntry{
						Agent:   guestID,
						Action:  action,
						Summary: summary,
					}
					if report.Artifact != nil {
						logEntry.ArtifactID = report.Artifact.ID
						logEntry.Tags = report.Artifact.Tags
					}
					if clErr := AppendChangelog(p.projectRoot, p.activeFeature, logEntry); clErr != nil {
						p.logger.Warn("failed to append changelog", "guest", guestID, "error", clErr)
					}

					stateOpts.Progress = report.Progress
					stateOpts.BlockedOn = report.BlockedOn
					stateOpts.Produces = report.Produces
				} else if output != "" {
					logEntry := ChangelogEntry{
						Agent:   guestID,
						Action:  "task_completed",
						Summary: output,
					}
					if clErr := AppendChangelog(p.projectRoot, p.activeFeature, logEntry); clErr != nil {
						p.logger.Warn("failed to append changelog", "guest", guestID, "error", clErr)
					}
				}

				if stErr := UpdateTaskState(p.projectRoot, p.activeFeature, guestID, "idle", output, stateOpts); stErr != nil {
					p.logger.Warn("failed to update task-state", "guest", guestID, "error", stErr)
				}
			}

			// Send stream-done
			if p.channelMgr != nil {
				_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
					StreamDone: true,
					GuestID:    guestID,
					ThreadID:   p.msg.ThreadID,
				})
			}

			if err != nil {
				p.logger.Warn("guest invocation failed", "guest", guestID, "error", err)
				if p.channelMgr != nil {
					_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
						Text:     fmt.Sprintf("[%s failed to respond]", agent.Name),
						GuestID:  guestID,
						ThreadID: p.msg.ThreadID,
					})
				}
				return
			}

			responseText := result.Output
			if responseText == "" {
				responseText = fullText.String()
			}

			// Extract prism-display blocks and broadcast as artifacts
			cleanText, displays := extractLeanDisplayBlocks(responseText)
			normalizeDisplayComponentsDataImageURLs(displays)
			var displayIDs []string
			if len(displays) > 0 {
				for _, comp := range displays {
					if p.channelMgr != nil {
						did := newDisplayID()
						displayIDs = append(displayIDs, did)
						_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
							Display:   comp,
							DisplayID: did,
							GuestID:   guestID,
							ThreadID:  p.msg.ThreadID,
						})
					}
				}
				responseText = cleanText
			}

			// Handle plan edits
			chatText := responseText
			if p.mode == types.ModePlan && p.planStore != nil {
				explanation, planMd, hasEdit := extractPlanEdit(responseText)
				if hasEdit {
					planEditMu.Lock()
					_, _ = p.planStore.Save(p.planSlug, guestID, planMd)
					planEditMu.Unlock()
					chatText = explanation
				}
			}

			// Handle story edits
			if p.storyStore != nil && p.proposalStore != nil {
				explanation, edits, hasEdits := extractStoryEdits(responseText)
				if hasEdits {
					var validEdits []StoryEdit
					var staleCount int
					for _, edit := range edits {
						if edit.Section != "" && edit.After != "" {
							staleCount++
							continue
						}
						if err := p.storyStore.CheckRevision(edit); err != nil {
							staleCount++
							continue
						}
						validEdits = append(validEdits, edit)
					}

					if len(validEdits) > 0 {
						proposal, _ := p.proposalStore.Stage(guestID, validEdits)
						if p.channelMgr != nil && proposal != nil {
							broadcastStoryDisplay(p.ctx, p.channelMgr, p.storyStore, proposal, guestID, p.msg.ThreadID)
						}
					}

					chatText = explanation
					if staleCount > 0 {
						chatText += fmt.Sprintf("\n\n(Warning) %d edit(s) rejected: content changed since last read.", staleCount)
					}
				}
			}

			// Send final text only when content differs from what was
			// streamed (e.g. display extraction, plan/story edits, or
			// displayIDs that need attaching).
			skipFinal := streamed && chatText == responseText && len(displayIDs) == 0
			if p.channelMgr != nil && chatText != "" && !skipFinal {
				_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
					Text:       chatText,
					GuestID:    guestID,
					ThreadID:   p.msg.ThreadID,
					DisplayIDs: displayIDs,
				})
			}

			p.convoBuf.RecordResponse(channelKey, agent.Name, guestID, responseText)
		}()
	}

	wg.Wait()
}

func broadcastStoryDisplay(ctx context.Context, channelMgr *channel.Manager, store *StoryStore, proposal *Proposal, agentID, threadID string) {
	sc, err := store.ReadContext()
	if err != nil {
		return
	}

	activeFile := "narrative.md"
	var sectionsList []map[string]string
	for file, secs := range sc.Sections {
		for id, hash := range secs {
			sectionsList = append(sectionsList, map[string]string{
				"id":   id,
				"file": file,
				"rev":  hash,
			})
		}
	}

	files := make([]string, 0, len(sc.Files))
	for f := range sc.Files {
		files = append(files, f)
	}

	content := ""
	if c, ok := sc.Files[activeFile]; ok {
		content = c
	}

	component := map[string]any{
		"kind":    "story",
		"title":   activeFile,
		"content": content,
		"storyMeta": map[string]any{
			"status":     "draft",
			"sections":   sectionsList,
			"files":      files,
			"activeFile": activeFile,
		},
	}

	if proposal != nil {
		proposalSections := make([]map[string]any, 0, len(proposal.Edits))
		for _, edit := range proposal.Edits {
			secID := edit.Section
			if secID == "" {
				secID = edit.After
			}
			if secID == "" {
				if m := sectionAnchorRe.FindStringSubmatch(edit.Content); len(m) > 1 {
					secID = m[1]
				}
			}

			currentContent := ""
			if fileSections, ok := sc.Files[edit.Target]; ok {
				_, secs := parseSections(fileSections)
				for _, s := range secs {
					if s.ID == edit.Section {
						currentContent = s.Content
						break
					}
				}
			}

			proposalSections = append(proposalSections, map[string]any{
				"sectionId":       secID,
				"target":          edit.Target,
				"status":          proposal.Status[secID],
				"currentContent":  currentContent,
				"proposedContent": edit.Content,
			})
		}

		component["proposals"] = []map[string]any{
			{
				"proposalId": proposal.ID,
				"guestId":    proposal.GuestID,
				"sections":   proposalSections,
			},
		}
	}

	_ = channelMgr.Broadcast(ctx, channel.OutgoingMessage{
		Display:   component,
		DisplayID: newStoryDisplayID(),
		GuestID:   agentID,
		ThreadID:  threadID,
	})
}

func newStoryDisplayID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "story-" + hex.EncodeToString(b)
}

func resolveGuestTools(agent guests.GuestAgent) []string {
	if len(agent.AllowedTools) > 0 {
		return agent.AllowedTools
	}
	fp := agent.RequirementsFingerprint
	emptyFP := "sha256:" + fmt.Sprintf("%x", sha256Sum(nil))
	if fp == emptyFP || fp == "" {
		return nil
	}
	return []string{"WebSearch", "WebFetch"}
}

func isToolAllowed(toolName string, allowedTools []string) bool {
	for _, pattern := range allowedTools {
		if strings.HasSuffix(pattern, "*") {
			prefix := pattern[:len(pattern)-1]
			if strings.HasPrefix(toolName, prefix) {
				return true
			}
		} else if pattern == toolName {
			return true
		}
	}
	return false
}

func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// extractJSON attempts to pull the first JSON object from LLM output.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return s
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

type SuggestResult struct {
	GuestID string `json:"guest_id"`
	Reason  string `json:"reason"`
}

func suggestGuestToInvite(
	ctx context.Context,
	runner agent.AgentRunner,
	candidates []GuestSummary,
	history string,
	logger *slog.Logger,
) *SuggestResult {
	var sb strings.Builder
	sb.WriteString("You are deciding whether to suggest bringing ONE new specialist into a conversation. ")
	sb.WriteString("The specialists below are NOT currently in the chat. Only suggest one if:\n\n")
	sb.WriteString("1. A response in the conversation explicitly asks for or names a specialty that matches a candidate, OR\n")
	sb.WriteString("2. The conversation has reached a clear blocker or decision point that cannot progress without this specialist's core expertise.\n\n")
	sb.WriteString("Do NOT suggest if:\n")
	sb.WriteString("- The topic is merely adjacent to a candidate's expertise\n")
	sb.WriteString("- The user could easily bring them in themselves\n")
	sb.WriteString("- It would be \"nice to have\" rather than clearly necessary\n")
	sb.WriteString("- A similar specialty is already represented in the active chat\n\n")
	sb.WriteString("The test: would a human collaborator naturally say \"should we pull in X for this?\" without feeling annoying? If not, return empty.\n\n")
	sb.WriteString("## Available Specialists (not in chat)\n\n")
	for _, c := range candidates {
		fmt.Fprintf(&sb, "- %s (%s): %s\n", c.ID, c.Name, c.Description)
	}
	if history != "" {
		sb.WriteString("\n")
		sb.WriteString(history)
	}
	sb.WriteString("\nRespond with JSON only: {\"guest_id\": \"id\", \"reason\": \"one sentence\"} or {\"guest_id\": \"\", \"reason\": \"\"} if no suggestion.\n")

	result, err := runner.Run(ctx, types.RunOpts{
		Prompt:         sb.String(),
		Model:          "claude-haiku-4-5",
		MaxTurns:       1,
		PermissionMode: "bypassPermissions",
	})
	if err != nil {
		logger.Debug("suggest call failed", "error", err)
		return nil
	}

	var suggest SuggestResult
	text := extractJSON(result.Output)
	if err := json.Unmarshal([]byte(text), &suggest); err != nil {
		logger.Debug("suggest call returned invalid JSON", "output", result.Output[:min(len(result.Output), 200)])
		return nil
	}
	if suggest.GuestID == "" {
		return nil
	}
	return &suggest
}

type ActivateResult struct {
	GuestID string
	Reason  string
}

// inviteVerbRe matches just the verb phrase that signals an invite intent.
// The accompanying name scan (inviteNameRe + chainSepRe) is run starting
// at the position immediately after this match so that only tokens
// adjacent to the verb are considered — phrases like "add some space
// between Miyazaki and Walt" do not match because the token immediately
// after "add" ("some") does not resolve to a known guest.
//
// Design principle — bias toward false negatives over false positives.
// Natural-language invite detection is inherently fuzzy. A missed
// auto-invite is cheap: the user types `/invite <name>` or `@<name>` and
// it resolves in one step. A false-positive auto-activation is expensive:
// a surprise participant joins the room, burns tokens, and (before the
// Plan-mode tool strip) could fire tools on misread intent.
// detectInviteIntent is a convenience, not an authority — when in doubt,
// return nothing and let the user state intent explicitly. Any future
// widening of these patterns must preserve this bias.
var (
	inviteVerbRe = regexp.MustCompile(`(?i)\b(?:invite|add|bring\s+in|pull\s+in)\s+(?:the\s+|our\s+)?`)
	inviteNameRe = regexp.MustCompile(`^@?([A-Za-z][\w-]*)`)
	// chainSepRe consumes a separator between chained names: "," "and"
	// "&" or their combinations with surrounding whitespace. After a
	// separator we re-scan inviteNameRe; a non-guest token ends the chain.
	chainSepRe = regexp.MustCompile(`^(?:\s*,\s*(?:and\s+)?|\s+and\s+|\s*&\s*)`)
)

func detectInviteIntent(text string, activeGuests []string, guestIndex *guests.GuestIndex) []ActivateResult {
	if guestIndex == nil {
		return nil
	}

	activeSet := make(map[string]struct{}, len(activeGuests))
	for _, id := range activeGuests {
		activeSet[id] = struct{}{}
	}

	// Build a lower-case lookup from both guest IDs and names so the
	// captured token can resolve by either. Names win over IDs on
	// collision (guest authors usually refer to "Tolkien", not to the
	// slug "narrative-writer").
	lookup := make(map[string]string, len(guestIndex.Agents)*2)
	for id, ag := range guestIndex.Agents {
		lookup[strings.ToLower(id)] = id
		if ag.Name != "" {
			lookup[strings.ToLower(ag.Name)] = id
		}
	}

	verbMatches := inviteVerbRe.FindAllStringIndex(text, -1)
	if len(verbMatches) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var results []ActivateResult

	for _, vm := range verbMatches {
		pos := vm[1]
		for pos < len(text) {
			nameLoc := inviteNameRe.FindStringSubmatchIndex(text[pos:])
			if nameLoc == nil {
				break
			}
			token := strings.ToLower(text[pos+nameLoc[2] : pos+nameLoc[3]])
			id, ok := lookup[token]
			if !ok {
				// The first non-resolving token ends the chain. This
				// is the "bias toward false negatives" guardrail: a
				// verb followed by an unknown token is not treated as
				// an invite at all.
				break
			}
			if _, active := activeSet[id]; !active {
				if _, dup := seen[id]; !dup {
					seen[id] = struct{}{}
					ag := guestIndex.Agents[id]
					name := ag.Name
					if name == "" {
						name = id
					}
					results = append(results, ActivateResult{
						GuestID: id,
						Reason:  fmt.Sprintf("User asked to invite %s", name),
					})
				}
			}
			pos += nameLoc[1]

			sep := chainSepRe.FindStringIndex(text[pos:])
			if sep == nil {
				break
			}
			pos += sep[1]
		}
	}

	if len(results) == 0 {
		return nil
	}
	return results
}
