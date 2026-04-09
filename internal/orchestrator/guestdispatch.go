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

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/plans"
	"github.com/rauriemo/anthem/internal/types"
)

const RoutingThreshold = 3

type GuestSummary struct {
	ID          string
	Name        string
	Description string
}

type RoutingResult struct {
	Guests              []string `json:"guests"`
	IncludeOrchestrator bool     `json:"include_orchestrator"`
	ContextUpdate       string   `json:"context_update"`
}

type GuestPromptOpts struct {
	Mode         string
	PlanContent  string
	StoryContext *StoryContext
}

var planEditBlockRe = regexp.MustCompile("(?s)```plan-edit\\s*\\n(.*?)\\n```")

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
	sb.WriteString("\nRespond with JSON only: {\"guests\": [\"id1\", ...], \"include_orchestrator\": false, \"context_update\": \"updated session summary\"}\n")
	sb.WriteString("Select guests whose expertise is relevant. Return empty guests array if none are relevant.\n")
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

func buildGuestPrompt(persona, projectSummary, sharedCtx, history, userMsg string, opts GuestPromptOpts) string {
	var sb strings.Builder

	sb.WriteString(persona)
	sb.WriteString("\n\n")

	if projectSummary != "" {
		sb.WriteString("## Project Context\n")
		sb.WriteString(projectSummary)
		sb.WriteString("\n\n")
	}

	if sharedCtx != "" {
		sb.WriteString("## Session Context\n")
		sb.WriteString(sharedCtx)
		sb.WriteString("\n\n")
	}

	if history != "" {
		sb.WriteString(history)
		sb.WriteString("\n")
	}

	if opts.Mode == "plan" && opts.PlanContent != "" {
		sb.WriteString("## Current Plan\n\n")
		sb.WriteString(opts.PlanContent)
		sb.WriteString("\n\n## Your Task\n\n")
		sb.WriteString("Review and contribute to this plan from your area of expertise.\n")
		sb.WriteString("- To EDIT the plan, include your updated markdown in a ```plan-edit code block.\n")
		sb.WriteString("  Only include the sections you are changing.\n")
		sb.WriteString("- To COMMENT without editing, just respond normally.\n\n")
	}

	if opts.StoryContext != nil {
		sb.WriteString("## Story Bible\n\n")
		sb.WriteString("### Project Context\n")
		sb.WriteString(opts.StoryContext.Config)
		sb.WriteString("\n\n")
		for name, content := range opts.StoryContext.Files {
			fmt.Fprintf(&sb, "### %s\n\n%s\n\n", name, content)
		}
		sb.WriteString("### Section Hashes\n\n")
		sb.WriteString("| section_id | file | rev |\n|---|---|---|\n")
		for file, secs := range opts.StoryContext.Sections {
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
	}

	fmt.Fprintf(&sb, "## User Message\n\n%s\n", userMsg)

	if opts.Mode == "fast" {
		sb.WriteString("\nBe concise.\n")
	}

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
	ctx            context.Context
	selectedIDs    []string
	msg            channel.IncomingMessage
	guestsDir      string
	runner         agent.AgentRunner
	sharedCtx      *SharedContext
	convoBuf       *ConvoBuffer
	channelMgr     *channel.Manager
	projectSummary string
	mode           string
	planContent    string
	planStore      *plans.Store
	planSlug       string
	guestIndex     *guests.GuestIndex
	storyStore     *StoryStore
	proposalStore  *ProposalStore
	logger         *slog.Logger
}

var planEditMu sync.Mutex

func dispatchSelectedGuests(p guestDispatchParams) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)

	channelKey := p.msg.ChannelKind
	history := FormatHistory(p.convoBuf.History(channelKey))
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

		prompt := buildGuestPrompt(persona, p.projectSummary, sharedCtxText, history, p.msg.Text, GuestPromptOpts{
			Mode:         p.mode,
			PlanContent:  p.planContent,
			StoryContext: storyCtx,
		})

		var allowedTools []string
		if p.guestIndex != nil {
			allowedTools = resolveGuestTools(p.guestIndex.Agents[guestID])
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Send stream-start indicator
			if p.channelMgr != nil {
				_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
					StreamDelta: "",
					GuestID:     guestID,
					ThreadID:    p.msg.ThreadID,
				})
			}

			var fullText strings.Builder
			onStream := func(delta string) {
				fullText.WriteString(delta)
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
				MaxTurns:       1,
				PermissionMode: "bypassPermissions",
				OnStream:       onStream,
			}
			if len(allowedTools) > 0 {
				runOpts.AllowedTools = allowedTools
			}

			result, err := p.runner.Run(p.ctx, runOpts)

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

			// Handle plan edits
			chatText := responseText
			if p.mode == "plan" && p.planStore != nil {
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

			// Only send a final text message when post-processing changed
			// the response (plan edits, story edits). The raw response was
			// already delivered via stream deltas.
			if p.channelMgr != nil && chatText != "" && chatText != responseText {
				_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
					Text:     chatText,
					GuestID:  guestID,
					ThreadID: p.msg.ThreadID,
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
	fp := agent.RequirementsFingerprint
	emptyFP := "sha256:" + fmt.Sprintf("%x", sha256Sum(nil))
	if fp == emptyFP || fp == "" {
		return nil
	}
	return []string{"WebSearch", "WebFetch"}
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
