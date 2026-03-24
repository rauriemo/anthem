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

type UserMessageContext struct {
	Text  string   `json:"text"`
	Files []string `json:"files,omitempty"`
}

type StateSnapshot struct {
	Tasks        []TaskSummary       `json:"tasks"`
	RetryQueue   []RetrySummary      `json:"retry_queue,omitempty"`
	Budget       BudgetSummary       `json:"budget"`
	Wave         *WaveSummary        `json:"wave,omitempty"`
	RecentEvents []EventSummary      `json:"recent_events,omitempty"`
	UserMessage  *UserMessageContext `json:"user_message,omitempty"`
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
- create_subtasks: Create subtasks as new tracker issues. Required: subtasks list with title, body, labels.
- promote_knowledge: (schema-only) Promote knowledge to repo. Required: summary.
- reply: Send a message back to the user through the communication channel. Required: body.
- request_maintenance: Propose a maintenance action (gc, lint, test, drift check). Required: maintenance_type, reason. Optional: auto_approvable (bool).`)

	sections = append(sections, `## Channel Messages

When a user message arrives through a channel (Slack, etc.), the state snapshot includes a "user_message" field with text and optional file contents. You must:

1. Understand the user's intent from their message, which may be:
   - A feature request (plain text, markdown, flowchart, mermaid diagram, or image)
   - A command ("approve the plan", "cancel task X", "skip task Y")
   - A question about project status
   - Approval/rejection of a proposed plan or maintenance action

2. For feature requests containing task descriptions:
   - Decompose the feature into concrete, actionable subtasks
   - Use create_subtasks with detailed titles and bodies for each subtask
   - Include appropriate labels (e.g. "priority:high", "type:feature")
   - Reply with a summary of the created tasks for user confirmation

3. For commands:
   - Execute the appropriate action (dispatch, skip, cancel, etc.)
   - Reply confirming the action taken

4. For status questions:
   - Reply with a concise summary based on the current state snapshot

5. For plan approval:
   - When the user approves a proposed plan, dispatch the planned tasks
   - When the user rejects or adjusts, update the plan accordingly and reply with changes

6. For maintenance approval:
   - When you receive maintenance signals, explain them clearly to the user
   - Wait for explicit approval before dispatching maintenance tasks`)

	sections = append(sections, `## Multi-Format Input

Users may describe features in multiple formats. Handle all of these:
- Plain text: direct feature description or command
- Markdown files: structured specs with sections, acceptance criteria, etc.
- Mermaid diagrams: flowcharts describing user flows or system architecture
- ASCII diagrams: text-based diagrams of flows or architecture
- Images: screenshots, whiteboard photos, flowchart images (described as [image: filename])
- Mixed: message text combined with one or more attached files

Always decompose complex features into small, independently executable tasks. Each task should be completable by a single executor agent session.`)

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
