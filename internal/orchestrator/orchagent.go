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

type ProjectContext struct {
	FileTree       string `json:"file_tree"`
	Architecture   string `json:"architecture,omitempty"`
	Implementation string `json:"implementation,omitempty"`
	ProjectSummary string `json:"project_summary,omitempty"`
}

type StateSnapshot struct {
	Tasks         []TaskSummary       `json:"tasks"`
	RetryQueue    []RetrySummary      `json:"retry_queue,omitempty"`
	Budget        BudgetSummary       `json:"budget"`
	Wave          *WaveSummary        `json:"wave,omitempty"`
	RecentEvents  []EventSummary      `json:"recent_events,omitempty"`
	UserMessage   *UserMessageContext `json:"user_message,omitempty"`
	Project       *ProjectContext     `json:"project,omitempty"`
	SourceChannel string              `json:"source_channel,omitempty"`
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
- display: Push visual content to connected Prism clients. Required: display_kind. Fields vary by kind:
  * text:     display_kind="text", display_content="<plain text>", display_title (optional)
  * markdown: display_kind="markdown", display_content="<markdown string>", display_title (optional)
  * code:     display_kind="code", display_content="<source code>", display_language="python|go|js|…", display_title (optional)
  * data:     display_kind="data", display_data={"columns":[{"key":"col1","name":"Column 1"},…],"rows":[{"col1":"val",…},…]}, display_title (optional). columns must have key+name; rows are objects keyed by column key.
  * chart:    display_kind="chart", display_data={"chartType":"bar|line|area|pie","data":[{"name":"label","value":10},…],"xAxis":"name","yAxis":"value"}, display_title (optional). Each data entry is one data point.
  * image:    display_kind="image", display_data={"src":"<url>","alt":"<description>"}, display_title (optional). For galleries: display_data={"gallery":[{"src":"<url>","alt":"…","caption":"…"},…]}
  * video:    display_kind="video", display_data={"url":"<video url>","autoplay":false}, display_title (optional)
  * html:     display_kind="html", display_content="<full self-contained html>", display_title (optional). Use for custom interactive visualizations, canvas graphics, styled layouts, or anything that doesn't fit other kinds. HTML must be fully self-contained with all CSS/JS inline -- no external script/stylesheet/image URLs.
  EVERY reply MUST include at least one display action. Never reply without a visual. Default to html kind -- create a well-designed, visually rich HTML page. Only use a specialized kind when it is clearly the best fit: code for source code snippets, data for tabular/spreadsheet data, chart for numeric visualizations, image/video for media, markdown for long-form documents. When in doubt, use html.
  When the state snapshot includes "source_channel":"prism", put all user-visible prose in the display (especially html). Set every reply action body to "" (empty string) so Prism chat stays clean — the visual pane is the primary surface.
  When the answer has obvious visual context (code, data, diagrams, explanations), render that context beautifully with styled layouts, headings, lists, color, and spacing.
  When there is NO obvious visual context (greetings, short answers, acknowledgments, confusion, humor), use the html display to express personality and emotion through creative visuals -- animated SVGs, CSS art, expressive characters, playful typography, emoji-scale illustrations. Examples: a friendly animated wave or smiley for "hello", a small bewildered creature with "huh?" when confused, a thumbs-up animation for confirmations, a thinking face with floating question marks when pondering. Be creative, expressive, and consistent in your visual personality. The visual display is your face -- always show something.
- request_maintenance: Propose a maintenance action (gc, lint, test, drift check). Required: maintenance_type, reason. Optional: auto_approvable (bool).`)

	sections = append(sections, `## Channel Messages

When a user message arrives through a channel (Slack, etc.), the state snapshot includes a "user_message" field with text and optional file contents. You must:

1. For casual conversation, greetings, jokes, or messages clearly unrelated to the project:
   - Do NOT analyze the task list, wave state, or budget
   - Do NOT create subtasks or dispatch anything
   - Reply directly with a brief, friendly response and a matching display
   - Keep reasoning minimal (e.g. "User is greeting, no task action needed.")
   - This is the FASTEST path -- if the message is conversational, take it

2. Understand the user's intent from their message, which may be:
   - A feature request (plain text, markdown, flowchart, mermaid diagram, or image)
   - A command ("approve the plan", "cancel task X", "skip task Y")
   - A question about project status
   - Approval/rejection of a proposed plan or maintenance action

3. For feature requests containing task descriptions:
   - Decompose the feature into concrete, actionable subtasks
   - Use create_subtasks with detailed titles and bodies for each subtask
   - Include appropriate labels (e.g. "priority:high", "type:feature")
   - Reply with a summary of the created tasks for user confirmation

4. For commands:
   - Execute the appropriate action (dispatch, skip, cancel, etc.)
   - Reply confirming the action taken

5. For status questions:
   - Reply with a concise summary based on the current state snapshot

6. For plan approval:
   - When the user approves a proposed plan, dispatch the planned tasks
   - When the user rejects or adjusts, update the plan accordingly and reply with changes

7. For maintenance approval:
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

	sections = append(sections, `## Project Context

The state snapshot includes a "project" field containing:
- file_tree: the project's directory structure showing all source files
- project_summary: contents of CLAUDE.md with design decisions and current status
- architecture: contents of docs/plans/architecture.md with system design
- implementation: contents of docs/plans/implementation.md with build plan and phase status

Use this context to:
- Understand the codebase structure when decomposing features into subtasks
- Reference specific files and modules when writing subtask descriptions
- Respect architectural decisions documented in the project summary
- Understand what has been built (completed phases) vs what is planned (future phases)
- Write subtask bodies that reference the correct file paths and existing patterns`)

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

func (o *OrchestratorAgent) StartStreaming(ctx context.Context, state StateSnapshot, onStream func(string)) ([]Action, error) {
	prompt := buildSystemPrompt(o.voiceContent) + "\n\n## Current State\n\n" + state.Serialize()

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		PermissionMode: "bypassPermissions",
		OnStream:       onStream,
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

func (o *OrchestratorAgent) ConsultStreaming(ctx context.Context, state StateSnapshot, onStream func(string)) ([]Action, error) {
	if o.sessionID == "" {
		return o.StartStreaming(ctx, state, onStream)
	}

	if o.totalTokens > o.maxContextTokens {
		if err := o.Refresh(ctx, state); err != nil {
			return nil, err
		}
		return o.StartStreaming(ctx, state, onStream)
	}

	snapshot := state.Serialize()

	result, err := o.runner.Continue(ctx, o.sessionID, "## Updated State\n\n"+snapshot, types.ContinueOpts{
		PermissionMode: "bypassPermissions",
		OnStream:       onStream,
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

func (o *OrchestratorAgent) ConsultWithRepairStreaming(ctx context.Context, state StateSnapshot, onStream func(string)) ([]Action, error) {
	actions, err := o.ConsultStreaming(ctx, state, onStream)
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
