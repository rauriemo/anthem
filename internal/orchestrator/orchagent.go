package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/voice"
)

type TaskSummary struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Labels   []string `json:"labels,omitempty"`
	CostUSD  float64  `json:"cost_usd,omitempty"`
	Priority int      `json:"priority"`
}

type RetrySummary struct {
	ID        string `json:"id"`
	Attempts  int    `json:"attempts"`
	NextRetry string `json:"next_retry"`
	LastError string `json:"last_error"`
}

type BudgetSummary struct {
	TotalSpentUSD float64 `json:"total_spent_usd"`
	WaveSpentUSD  float64 `json:"wave_spent_usd"`
}

type WaveSummary struct {
	ID              string   `json:"id"`
	FrontierTaskIDs []string `json:"frontier_task_ids,omitempty"`
	Status          string   `json:"status"`
}

type EventSummary struct {
	Type      string `json:"type"`
	TaskID    string `json:"task_id,omitempty"`
	Timestamp string `json:"timestamp"`
}

type StateSnapshot struct {
	Tasks        []TaskSummary  `json:"tasks"`
	RetryQueue   []RetrySummary `json:"retry_queue,omitempty"`
	Budget       BudgetSummary  `json:"budget"`
	Wave         *WaveSummary   `json:"wave,omitempty"`
	RecentEvents []EventSummary `json:"recent_events,omitempty"`
}

func (s StateSnapshot) Serialize() string {
	b, _ := json.MarshalIndent(s, "", "  ")
	return string(b)
}

type OrchestratorAgent struct {
	runner           agent.AgentRunner
	voiceContent     string
	logger           *slog.Logger
	sessionID        string
	totalTokens      int
	maxContextTokens int
}

func NewOrchestratorAgent(runner agent.AgentRunner, voiceContent string, maxContextTokens int, logger *slog.Logger) *OrchestratorAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &OrchestratorAgent{
		runner:           runner,
		voiceContent:     voiceContent,
		logger:           logger,
		maxContextTokens: maxContextTokens,
	}
}

func (o *OrchestratorAgent) SetVoiceContent(content string) {
	o.voiceContent = content
}

func buildSystemPrompt(voiceContent string) string {
	var sections []string

	if voiceContent != "" {
		sections = append(sections, voiceContent)
		sections = append(sections, voice.SelfEvolutionInstruction())
	}

	sections = append(sections, `## Role

You are an orchestrator agent -- a stateless allocator. You receive a state snapshot of all tasks. You propose actions. The daemon validates and executes them. You never execute directly.`)

	sections = append(sections, `## Actions

Available action types:
- dispatch: Start an executor for a task. Required: task_id.
- skip: Skip a task this wave. Required: task_id, reason.
- comment: Post a comment on a tracker issue. Required: task_id, body.
- update_voice: Propose a VOICE.md section update. Required: section_name, section_content.
- request_approval: Flag a task for human review. Required: task_id.
- close_wave: Mark the current wave as exhausted. No extra fields.
- create_subtasks: (schema-only) Create subtasks. Required: subtasks list with title, body, labels.
- promote_knowledge: (schema-only) Promote knowledge to repo. Required: summary.`)

	sections = append(sections, `## Wave Model

When all frontier tasks are terminal or non-runnable, propose a close_wave action.`)

	sections = append(sections, `## Response Format

Respond with a single JSON object containing 'reasoning' (string) and 'actions' (array). Example:
{"reasoning": "Task 42 is ready, task 7 is blocked on approval.", "actions": [{"type": "dispatch", "task_id": "42"}, {"type": "request_approval", "task_id": "7"}]}`)

	return strings.Join(sections, "\n\n")
}

func (o *OrchestratorAgent) Start(ctx context.Context, state StateSnapshot) ([]Action, error) {
	prompt := buildSystemPrompt(o.voiceContent) + "\n\n## Current State\n\n" + state.Serialize()

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		PermissionMode: "bypassPermissions",
	})
	if err != nil {
		return nil, fmt.Errorf("orchestrator start: %w", err)
	}

	o.sessionID = result.SessionID
	o.totalTokens += result.TokensIn + result.TokensOut

	actions, err := parseActions(result.Output)
	if err != nil {
		return nil, fmt.Errorf("orchestrator start: parsing actions: %w", err)
	}

	return actions, nil
}

func (o *OrchestratorAgent) Consult(ctx context.Context, state StateSnapshot) ([]Action, error) {
	if o.sessionID == "" {
		return o.Start(ctx, state)
	}

	if o.totalTokens > o.maxContextTokens {
		if err := o.Refresh(ctx, state); err != nil {
			return nil, err
		}
		return o.Start(ctx, state)
	}

	snapshot := state.Serialize()

	result, err := o.runner.Continue(ctx, o.sessionID, "## Updated State\n\n"+snapshot, types.ContinueOpts{
		PermissionMode: "bypassPermissions",
	})
	if err != nil {
		return nil, fmt.Errorf("orchestrator consult: %w", err)
	}

	o.sessionID = result.SessionID
	o.totalTokens += result.TokensIn + result.TokensOut

	actions, err := parseActions(result.Output)
	if err != nil {
		return nil, fmt.Errorf("orchestrator consult: parsing actions: %w", err)
	}

	return actions, nil
}

func (o *OrchestratorAgent) Refresh(ctx context.Context, state StateSnapshot) error {
	o.logger.Info("refreshing orchestrator session", "old_session", o.sessionID, "total_tokens", o.totalTokens)
	o.sessionID = ""
	o.totalTokens = 0
	return nil
}

func parseActions(output string) ([]Action, error) {
	// Find the first { and its matching } using brace counting.
	start := -1
	for i, c := range output {
		if c == '{' {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, fmt.Errorf("no JSON object found in output")
	}

	depth := 0
	end := -1
	for i := start; i < len(output); i++ {
		switch output[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
		if end != -1 {
			break
		}
	}
	if end == -1 {
		return nil, fmt.Errorf("unmatched braces in JSON output")
	}

	var resp OrchestratorResponse
	if err := json.Unmarshal([]byte(output[start:end]), &resp); err != nil {
		return nil, fmt.Errorf("parsing orchestrator response: %w", err)
	}

	return resp.Actions, nil
}

const repairPrompt = `Your previous response was not valid JSON. Respond with ONLY a JSON object: {"reasoning": "...", "actions": [...]}`

func (o *OrchestratorAgent) ConsultWithRepair(ctx context.Context, state StateSnapshot) ([]Action, error) {
	actions, err := o.Consult(ctx, state)
	if err == nil {
		return actions, nil
	}

	o.logger.Warn("orchestrator response parse failed, attempting repair", "error", err)

	if o.sessionID == "" {
		o.logger.Warn("no session to repair, falling back to mechanical dispatch")
		return nil, nil
	}

	result, repairErr := o.runner.Continue(ctx, o.sessionID, repairPrompt, types.ContinueOpts{
		PermissionMode: "bypassPermissions",
	})
	if repairErr != nil {
		o.logger.Warn("repair continue failed, falling back to mechanical dispatch", "error", repairErr)
		return nil, nil
	}

	o.totalTokens += result.TokensIn + result.TokensOut

	actions, err = parseActions(result.Output)
	if err != nil {
		o.logger.Warn("repair parse also failed, falling back to mechanical dispatch", "error", err)
		return nil, nil
	}

	return actions, nil
}
