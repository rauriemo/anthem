package orchestrator

import (
	"context"
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
	Guests        []string `json:"guests"`
	ContextUpdate string   `json:"context_update"`
}

type GuestPromptOpts struct {
	Mode        string
	PlanContent string
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
	sb.WriteString("\nRespond with JSON only: {\"guests\": [\"id1\", ...], \"context_update\": \"updated session summary\"}\n")
	sb.WriteString("Select guests whose expertise is relevant. Return empty guests array if none are relevant.\n")
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
			continue
		}

		model := agent.Model
		if model == "" {
			model = "claude-sonnet-4-5"
		}

		prompt := buildGuestPrompt(persona, p.projectSummary, sharedCtxText, history, p.msg.Text, GuestPromptOpts{
			Mode:        p.mode,
			PlanContent: p.planContent,
		})

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

			result, err := p.runner.Run(p.ctx, types.RunOpts{
				Prompt:         prompt,
				Model:          model,
				MaxTurns:       1,
				PermissionMode: "bypassPermissions",
				OnStream:       onStream,
			})

			// Send stream-done
			if p.channelMgr != nil {
				_ = p.channelMgr.Broadcast(p.ctx, channel.OutgoingMessage{
					StreamDone: true,
					GuestID:    guestID,
					ThreadID:   p.msg.ThreadID,
				})
			}

			responseText := result.Output
			if responseText == "" {
				responseText = fullText.String()
			}

			if err != nil {
				p.logger.Warn("guest invocation failed", "guest", guestID, "error", err)
				return
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

			if p.channelMgr != nil && chatText != "" {
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
