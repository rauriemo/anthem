package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/voice"
)

type TaskSummary struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Labels    []string `json:"labels,omitempty"`
	CostUSD   float64  `json:"cost_usd,omitempty"`
	Priority  int      `json:"priority"`
	DependsOn []string `json:"depends_on,omitempty"`
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
	Knowledge      string `json:"knowledge,omitempty"`
}

// PlanContext carries the active plan draft for plan/build mode consults.
type PlanContext struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// PlanMetaSummary is a lightweight entry for plan history in the snapshot.
type PlanMetaSummary struct {
	Path    string    `json:"path"`
	Title   string    `json:"title"`
	Status  string    `json:"status"`
	Updated time.Time `json:"updated"`
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
	ActivePlan    *PlanContext        `json:"active_plan,omitempty"`
	PlanHistory   []PlanMetaSummary   `json:"plan_history,omitempty"`
}

func (s StateSnapshot) Serialize() string {
	b, _ := json.MarshalIndent(s, "", "  ")
	return string(b)
}

// ExploreRequest is emitted by the scout phase to request a focused research subagent.
type ExploreRequest struct {
	Query string `json:"query"`
	Scope string `json:"scope"`
	Focus string `json:"focus"`
}

// ExploreResult carries findings from a single explorer subagent.
type ExploreResult struct {
	Query     string   `json:"query"`
	FilesRead []string `json:"files_examined,omitempty"`
	Findings  string   `json:"findings"`
	Gaps      []string `json:"gaps,omitempty"`
	Summary   string   `json:"summary"`
	Error     string   `json:"error,omitempty"`
}

// scoutResponse is the JSON output from the scout phase.
type scoutResponse struct {
	Reasoning   string           `json:"reasoning"`
	Explores    []ExploreRequest `json:"explores"`
	UserMessage string           `json:"user_message"`
}

type OrchestratorAgent struct {
	runner           agent.AgentRunner
	voiceContent     string
	logger           *slog.Logger
	sessionID        string
	totalTokens      int
	totalCostUSD     float64
	maxContextTokens int
	maxTurns         int
	planMaxTurns     int
	explorerMaxTurns int
	maxExplorers     int
}

func NewOrchestratorAgent(runner agent.AgentRunner, voiceContent string, maxContextTokens int, maxTurns int, planMaxTurns int, explorerMaxTurns int, maxExplorers int, logger *slog.Logger) *OrchestratorAgent {
	if logger == nil {
		logger = slog.Default()
	}
	if maxTurns <= 0 {
		maxTurns = 10
	}
	if planMaxTurns <= 0 {
		planMaxTurns = 25
	}
	if explorerMaxTurns <= 0 {
		explorerMaxTurns = 10
	}
	if maxExplorers <= 0 {
		maxExplorers = 5
	}
	return &OrchestratorAgent{
		runner:           runner,
		voiceContent:     voiceContent,
		logger:           logger,
		maxContextTokens: maxContextTokens,
		maxTurns:         maxTurns,
		planMaxTurns:     planMaxTurns,
		explorerMaxTurns: explorerMaxTurns,
		maxExplorers:     maxExplorers,
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

You are an intelligent task orchestrator with codebase access. You receive a state snapshot of all tasks and can use built-in tools (Read, Grep, Glob) to explore the codebase before planning. After analysis, you propose actions as structured JSON. The daemon validates and executes them. You never execute tasks directly — you plan, analyze, and delegate.`)

	sections = append(sections, `## Actions

Available action types:
- dispatch: Start an executor for a task. Required: task_id. Optional: profile (one of "coder", "architect", "tester", "debugger"). Default: "coder". Use "architect" for read-only analysis, "tester" for writing tests, "debugger" for fixing review-failed tasks.
- skip: Skip a task this wave. Required: task_id, reason.
- comment: Post a comment on a tracker issue. Required: task_id, body.
- update_voice: Propose a VOICE.md section update. Required: section_name, section_content.
- request_approval: Flag a task for human review. Required: task_id.
- close_wave: Mark the current wave as exhausted. No extra fields.
- create_subtasks: Create subtasks as new tracker issues. Required: subtasks list with title, body, labels. Optional: depends_on (list of 1-based ordinal task numbers from this batch, e.g. [1, 2] means "depends on the 1st and 2nd subtasks"). The daemon remaps ordinals to real issue IDs after creation.
- promote_knowledge: Save architectural discoveries, recurring patterns, or solved edge cases to docs/exec-plans/. Required: summary (markdown content to save). Use after completing tasks that revealed non-obvious insights.
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
   - Keep reasoning minimal (e.g. "User is greeting, no task action needed.")
   - ALWAYS include a reply action with your conversational response in the body -- even for Prism channels. The "empty reply body" rule does NOT apply to casual conversation. Users expect to see your response in the chat.
   - For the display: produce a rich, visually expressive HTML page just like any other response. Use the same creative visual personality described above -- animated SVGs, CSS art, expressive characters, playful typography. The visual display is your face, even for casual chat.
   - This is the FAST path -- if the message is conversational, take it (skip task analysis) but still make the display beautiful

2. Understand the user's intent from their message, which may be:
   - A feature request (plain text, markdown, flowchart, mermaid diagram, or image)
   - A command ("approve the plan", "cancel task X", "skip task Y")
   - A question about project status
   - Approval/rejection of a proposed plan or maintenance action

3. For feature requests containing task descriptions:
   - Decompose the feature into concrete, actionable subtasks
   - Use create_subtasks with detailed titles and bodies for each subtask
   - Always include "todo" as the first label so the tracker picks up the issue, plus descriptive labels (e.g. "todo", "priority:high", "type:feature")
   - Use 1-based ordinal numbers in depends_on (e.g. [1, 2] = depends on subtasks #1 and #2 in this batch)
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
- Write subtask bodies that reference the correct file paths and existing patterns

The "knowledge" field (if present) contains summaries from previous runs -- architectural discoveries, recurring patterns, and solved edge cases. Use these to inform planning.`)

	sections = append(sections, `## Wave Model

When all frontier tasks are terminal or non-runnable, propose a close_wave action.`)

	sections = append(sections, `## Response Format

Respond with a single JSON object containing 'reasoning' (string) and 'actions' (array). Example:
{"reasoning": "Task 42 is ready, task 7 is blocked on approval.", "actions": [{"type": "dispatch", "task_id": "42"}, {"type": "request_approval", "task_id": "7"}]}`)

	return strings.Join(sections, "\n\n")
}

// buildPlanSystemPrompt builds a system prompt for plan/synthesis modes that
// omits all display/HTML instructions. This prevents the LLM from defaulting
// to HTML output when it should produce structured anthem-plan markdown.
func buildPlanSystemPrompt(voiceContent string) string {
	var sections []string

	if voiceContent != "" {
		sections = append(sections, voiceContent)
		sections = append(sections, voice.SelfEvolutionInstruction())
	}

	sections = append(sections, `## Role

You are an intelligent task orchestrator with codebase access. You receive a state snapshot of all tasks and can use built-in tools (Read, Grep, Glob) to explore the codebase before planning. In plan mode your output is plain markdown text — not JSON actions, not HTML, not display artifacts. You research thoroughly and produce structured plans.`)

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
- Write subtask bodies that reference the correct file paths and existing patterns

The "knowledge" field (if present) contains summaries from previous runs — architectural discoveries, recurring patterns, and solved edge cases. Use these to inform planning.`)

	return strings.Join(sections, "\n\n")
}

func (o *OrchestratorAgent) Start(ctx context.Context, state StateSnapshot) ([]Action, error) {
	prompt := buildSystemPrompt(o.voiceContent) + "\n\n## Current State\n\n" + state.Serialize()

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		PermissionMode: "bypassPermissions",
		MaxTurns:       o.maxTurns,
	})
	if err != nil {
		return nil, fmt.Errorf("orchestrator start: %w", err)
	}

	o.recordResult(result)
	o.warnIfHighTokens(result)

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

	o.recordResult(result)
	o.warnIfHighTokens(result)

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

// DrainCost returns the cost accumulated since the last drain and resets it.
func (o *OrchestratorAgent) DrainCost() (tokensIn int, tokensOut int, costUSD float64) {
	t := o.totalCostUSD
	o.totalCostUSD = 0
	return 0, 0, t
}

func (o *OrchestratorAgent) warnIfHighTokens(result *types.RunResult) {
	if result == nil {
		return
	}
	totalTokens := result.TokensIn + result.TokensOut
	if totalTokens > 20000 {
		o.logger.Warn("orchestrator consult exceeded 20K tokens",
			"tokens_in", result.TokensIn,
			"tokens_out", result.TokensOut,
			"total", totalTokens,
		)
	}
}

func (o *OrchestratorAgent) recordResult(result *types.RunResult) {
	o.sessionID = result.SessionID
	o.totalTokens += result.TokensIn + result.TokensOut
	o.totalCostUSD += result.CostUSD
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

	o.recordResult(result)
	o.warnIfHighTokens(result)

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

	o.recordResult(result)
	o.warnIfHighTokens(result)

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

	o.recordResult(result)

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

	o.recordResult(result)

	actions, err = parseActions(result.Output)
	if err != nil {
		o.logger.Warn("repair parse also failed, falling back to mechanical dispatch", "error", err)
		return nil, nil
	}

	return actions, nil
}

// --- Plan-mode and Build-mode prompts ---

const planModePromptSuffix = `

## Plan Mode

You are in PLANNING mode. The user wants a well-researched, evidence-based plan before any work begins. You have READ-ONLY access — write tools (Write, Edit, MultiEdit) are disabled.

### Stage 1: Research (MANDATORY)

Before writing ANY plan, you MUST explore the codebase using your tools. You MUST make at least 5 tool calls (Read, Grep, Glob, Bash) before producing any plan output. This is non-negotiable.

Research checklist — verify each of these for the area in question:
- File structure and key modules in the affected area
- Existing implementations and patterns to follow
- Test coverage for files you will propose changing (look for test files, check what is and is not covered)
- Dependencies and imports that may be affected
- Configuration or environment requirements
- Similar patterns elsewhere in the codebase that should stay consistent

Do NOT skip this stage. Do NOT generate a plan from memory or the project context summary alone. The user chose Plan mode specifically for deep analysis.

### Stage 2: Synthesis

Only after thorough research, produce a structured markdown plan:
- A title heading (# Title)
- A "## Analysis" section summarizing what you found during research, citing specific files, functions, and line numbers
- A "## Tasks" section with numbered subsections (### 1. Task Title) each with:
  - **Labels:** always start with "todo", then descriptive labels (e.g. todo, area:frontend, priority:high)
  - **Profile:** recommended executor profile (coder, architect, tester, debugger)
  - **Depends on:** task numbers this depends on, or "none"
  - **Description:** detailed implementation steps referencing specific files and code you actually read, not assumed
- If an existing plan draft is in the state, refine it based on the user's feedback rather than starting from scratch.
- Do NOT output JSON actions. Do NOT create issues, dispatch, or execute anything.
- Wrap the plan in: ` + "```anthem-plan\n...\n```" + `
- You may include conversational commentary outside the fenced block.`

const buildModePromptSuffix = `

## Build Mode

The user has approved the plan below and wants to start building. Your job is ONLY to create GitHub issues — you do NOT execute the work yourself.

CRITICAL RULES:
- You MUST emit a create_subtasks action. This is mandatory. Without it, nothing happens.
- You MUST NOT pretend the work is already done. You are a planner, not an executor.
- You MUST NOT show completion summaries or checkmarks. The work has NOT been done yet.
- Even for a single small task, you MUST create at least one subtask issue.

Steps:
1. Read the plan content carefully.
2. Break it into one or more concrete GitHub issues using the create_subtasks action.
   - Use clear, actionable titles (e.g. "Remove /fast slash command from slashCommands.ts and update tests")
   - Write detailed implementation instructions in the body including file paths, line numbers, and acceptance criteria
   - Always include "todo" as the first label, then any descriptive labels (e.g. "type:cleanup", "priority:high")
   - Use 1-based ordinal numbers for depends_on; the daemon remaps ordinals to real issue IDs
3. After the create_subtasks action, include a reply action confirming what issues were created.
4. Respond in the normal JSON actions format: {"reasoning": "...", "actions": [...]}

The create_subtasks action is the ONLY way work gets dispatched to executors. If you skip it, nothing will be built.`

// ConsultPlan runs a plan-mode consultation. The LLM returns markdown, not JSON actions.
func (o *OrchestratorAgent) ConsultPlan(ctx context.Context, state StateSnapshot, model string, onStream func(string)) (string, error) {
	prompt := buildPlanSystemPrompt(o.voiceContent) + planModePromptSuffix + "\n\n## Current State\n\n" + state.Serialize()

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		Model:          model,
		PermissionMode: "bypassPermissions",
		MaxTurns:       o.planMaxTurns,
		DeniedTools:    []string{"Write", "Edit", "MultiEdit"},
		OnStream:       onStream,
	})
	if err != nil {
		return "", fmt.Errorf("plan consult: %w", err)
	}

	o.recordResult(result)
	o.warnIfHighTokens(result)
	return result.Output, nil
}

// ConsultBuild runs a build-mode consultation with the approved plan injected.
func (o *OrchestratorAgent) ConsultBuild(ctx context.Context, state StateSnapshot, model string, onStream func(string)) ([]Action, error) {
	prompt := buildSystemPrompt(o.voiceContent) + buildModePromptSuffix + "\n\n## Current State\n\n" + state.Serialize()

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		Model:          model,
		PermissionMode: "bypassPermissions",
		MaxTurns:       o.maxTurns,
		OnStream:       onStream,
	})
	if err != nil {
		return nil, fmt.Errorf("build consult: %w", err)
	}

	o.recordResult(result)
	o.warnIfHighTokens(result)

	actions, err := parseActions(result.Output)
	if err != nil {
		return nil, fmt.Errorf("build consult: parsing actions: %w", err)
	}
	return actions, nil
}

// --- Explorer subagent prompts ---

const scoutPromptSuffix = `

## Scout Mode

You are in SCOUT mode. Your job is to understand the user's request and identify which areas of the codebase need deep investigation. You are NOT producing a plan yet.

Steps:
1. Read the project file tree and key modules to understand the codebase structure.
2. Based on the user's request, identify 1-5 areas that need focused research by a specialist explorer agent.
3. For each area, write a focused research question, a directory scope, and a focus category.

Focus categories: "tests" (test coverage analysis), "security" (input validation, auth, path traversal), "architecture" (code structure, dependencies, patterns), "dependencies" (imports, package relationships), "patterns" (coding conventions, similar implementations to follow).

If the request is trivially simple (e.g. rename a variable, fix a typo) and needs NO deep research, return an empty explores array.

Respond with JSON only:
{"reasoning": "...", "explores": [{"query": "...", "scope": "...", "focus": "..."}], "user_message": "Brief message to the user about what you are researching (shown while explorers run)"}

Cross-focus rule: When the user asks about test coverage or test plans, ALWAYS include at least one "security" focus explore to verify that security boundaries have dedicated tests.

Examples of good explore requests:
- {"query": "What test coverage exists for authentication endpoints and middleware?", "scope": "backend/tests/", "focus": "tests"}
- {"query": "How is file serving implemented? Check path validation, allowed roots, traversal prevention.", "scope": "backend/", "focus": "security"}
- {"query": "What patterns does the frontend use for state management and API calls?", "scope": "frontend/src/", "focus": "architecture"}`

const synthesisPromptSuffix = `

## Synthesis Mode

You are in SYNTHESIS mode. Explorer agents have investigated the codebase in parallel and their findings are provided below. Your job is to synthesize these findings into a well-structured plan.

CRITICAL: Every claim in your plan MUST be backed by explorer findings. Do not add tasks based on assumptions — only on verified evidence from the research below.

Produce a structured markdown plan:
- A title heading (# Title)
- A "## Analysis" section summarizing key findings from the explorers, citing specific files, functions, and line numbers they reported
- A "## Tasks" section with numbered subsections (### 1. Task Title) each with:
  - **Labels:** always start with "todo", then descriptive labels
  - **Profile:** recommended executor profile (coder, architect, tester, debugger)
  - **Depends on:** task numbers this depends on, or "none"
  - **Description:** detailed implementation steps referencing specific files and code the explorers actually found
- If explorers reported gaps or errors, note them in the Analysis section
- If an existing plan draft is in the state, refine it using the new research rather than starting from scratch
- Do NOT output JSON actions. Do NOT create issues, dispatch, or execute anything.
- Wrap the plan in: ` + "```anthem-plan\n...\n```" + `
- You may include conversational commentary outside the fenced block.`

// focusSpecificInstructions returns additional prompt guidance based on the
// explorer's focus category. Empty string for focus types with no extra guidance.
func focusSpecificInstructions(focus string) string {
	switch focus {
	case "tests":
		return "\n### Test Coverage Deep-Dive\n\n" +
			"- For each source file, check if route handlers/endpoints have dedicated tests — not just the underlying services or helpers\n" +
			"- Flag files where service-layer tests exist but the endpoint/route that calls them has NO test\n" +
			"- Check for security-critical functions (input validation, auth checks, path traversal guards) that lack dedicated tests\n" +
			"- Look INSIDE partially tested files for uncovered critical paths, not just files with zero tests\n\n"
	case "security":
		return "\n### Security Boundary Audit\n\n" +
			"- Check every input validation function, path traversal guard, authentication middleware, and authorization check\n" +
			"- For each security boundary, verify a dedicated test exists that exercises both allowed and denied cases\n" +
			"- Flag any security-critical function with zero test coverage as CRITICAL\n" +
			"- Check for hardcoded secrets, unsafe deserialization, and command injection vectors\n\n"
	default:
		return "\n"
	}
}

// BuildExplorerPrompt constructs a focused prompt for a single explorer subagent.
func BuildExplorerPrompt(req ExploreRequest, fileTree string) string {
	var b strings.Builder
	b.WriteString("## Codebase Research Agent\n\n")
	b.WriteString("You are a focused research agent investigating ONE specific question. Be thorough and exhaustive.\n\n")
	b.WriteString("### Research Question\n\n")
	b.WriteString(req.Query)
	b.WriteString("\n\n### Focus Area\n\n")
	b.WriteString(req.Focus)
	b.WriteString("\n\n### Scope\n\n")
	b.WriteString("Investigate within: " + req.Scope)
	b.WriteString("\n\n### Instructions\n\n")
	b.WriteString("- Use Read, Grep, Glob to explore the codebase exhaustively within your scope\n")
	b.WriteString("- Check EVERY relevant file, do not assume or skip\n")
	b.WriteString("- Cite specific file paths, function names, and line numbers\n")
	b.WriteString("- Do NOT make recommendations or create plans — just report what you find\n")
	b.WriteString("- Report what EXISTS, what is MISSING, and what PATTERNS you observe\n")
	b.WriteString(focusSpecificInstructions(req.Focus))
	b.WriteString("\n### Output Format\n\n")
	b.WriteString("End your response with a structured findings block:\n")
	b.WriteString("```explorer-findings\n")
	b.WriteString(`{"query": "your question", "files_examined": ["path1", "path2"], "findings": "detailed findings text", "gaps": ["gap1", "gap2"], "summary": "one paragraph summary"}`)
	b.WriteString("\n```\n")
	if fileTree != "" {
		b.WriteString("\n### Project File Tree\n\n```\n")
		b.WriteString(fileTree)
		b.WriteString("\n```\n")
	}
	return b.String()
}

// parseScoutResponse extracts explore requests from the scout's JSON output.
func parseScoutResponse(output string) (*scoutResponse, error) {
	start := strings.Index(output, "{")
	if start == -1 {
		return nil, fmt.Errorf("no JSON object found in scout output")
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
		return nil, fmt.Errorf("unmatched braces in scout output")
	}

	var resp scoutResponse
	if err := json.Unmarshal([]byte(output[start:end]), &resp); err != nil {
		return nil, fmt.Errorf("parsing scout response: %w", err)
	}
	return &resp, nil
}

// parseExplorerFindings extracts the explorer-findings JSON block from explorer output.
func parseExplorerFindings(output string) (*ExploreResult, error) {
	const marker = "```explorer-findings"
	idx := strings.Index(output, marker)
	if idx == -1 {
		return &ExploreResult{
			Findings: output,
			Summary:  truncateForSummary(output, 500),
		}, nil
	}
	start := idx + len(marker)
	if start < len(output) && output[start] == '\n' {
		start++
	}
	endMarker := strings.Index(output[start:], "```")
	var jsonStr string
	if endMarker == -1 {
		jsonStr = output[start:]
	} else {
		jsonStr = output[start : start+endMarker]
	}
	jsonStr = strings.TrimSpace(jsonStr)

	var result ExploreResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return &ExploreResult{
			Findings: output,
			Summary:  truncateForSummary(output, 500),
		}, nil
	}
	return &result, nil
}

func truncateForSummary(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ScoutPlan runs the scout phase: identifies areas needing deep research.
func (o *OrchestratorAgent) ScoutPlan(ctx context.Context, state StateSnapshot, model string, onStream func(string)) ([]ExploreRequest, string, error) {
	prompt := buildPlanSystemPrompt(o.voiceContent) + scoutPromptSuffix + "\n\n## Current State\n\n" + state.Serialize()

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		Model:          model,
		PermissionMode: "bypassPermissions",
		MaxTurns:       8,
		DeniedTools:    []string{"Write", "Edit", "MultiEdit"},
		OnStream:       onStream,
	})
	if err != nil {
		return nil, "", fmt.Errorf("scout plan: %w", err)
	}

	o.recordResult(result)

	resp, err := parseScoutResponse(result.Output)
	if err != nil {
		o.logger.Warn("scout response parse failed, falling back to single-run plan", "error", err)
		return nil, "", err
	}

	cap := o.maxExplorers
	if len(resp.Explores) > cap {
		resp.Explores = resp.Explores[:cap]
	}

	return resp.Explores, resp.UserMessage, nil
}

// SynthesizePlan runs the synthesis phase: produces a plan from explorer findings.
func (o *OrchestratorAgent) SynthesizePlan(ctx context.Context, state StateSnapshot, findings []ExploreResult, model string, onStream func(string)) (string, error) {
	var findingsText strings.Builder
	findingsText.WriteString("\n\n## Explorer Findings\n\n")
	for i, f := range findings {
		findingsText.WriteString(fmt.Sprintf("### Explorer %d: %s\n\n", i+1, f.Query))
		if f.Error != "" {
			findingsText.WriteString(fmt.Sprintf("**Error:** %s\n\n", f.Error))
			continue
		}
		if f.Summary != "" {
			findingsText.WriteString(fmt.Sprintf("**Summary:** %s\n\n", f.Summary))
		}
		if f.Findings != "" {
			findingsText.WriteString(f.Findings + "\n\n")
		}
		if len(f.Gaps) > 0 {
			findingsText.WriteString("**Gaps:** " + strings.Join(f.Gaps, "; ") + "\n\n")
		}
		if len(f.FilesRead) > 0 {
			findingsText.WriteString(fmt.Sprintf("**Files examined:** %d\n\n", len(f.FilesRead)))
		}
	}

	prompt := buildPlanSystemPrompt(o.voiceContent) + synthesisPromptSuffix + findingsText.String() + "\n\n## Current State\n\n" + state.Serialize()

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		Model:          model,
		PermissionMode: "bypassPermissions",
		MaxTurns:       o.planMaxTurns,
		DeniedTools:    []string{"Write", "Edit", "MultiEdit"},
		OnStream:       onStream,
	})
	if err != nil {
		return "", fmt.Errorf("synthesize plan: %w", err)
	}

	o.recordResult(result)
	o.warnIfHighTokens(result)
	return result.Output, nil
}
