# Anthem -- Claude Code Context

## What Is Anthem

Anthem is an open-source agent orchestrator for Claude Code -- an alternative to OpenAI Symphony with a key differentiator: a hybrid architecture where a Go daemon handles mechanical reliability (polling, process management, workspace isolation, retry, state) and an AI orchestrator agent (Phase 3) sits on top for intelligence (user communication, task decomposition, parallel planning). The orchestrator agent uses VOICE.md for personality and learns the user over time. Executor agents are headless coding workers that get harnesses (WORKFLOW.md, skills, MCP tools, constraints), not personality.

## Plans and Architecture Docs

Read these documents thoroughly before writing any code:

- `docs/plans/architecture.md` -- Full system architecture with mermaid diagrams, all 15 components, Go interface definitions, data types, CLI spec, cross-platform details, event bus, rate limiting, hook failure handling
- `docs/plans/implementation.md` -- Scaffold structure, implementation order with specific steps, phase breakdown, dependency list, testing strategy

These are the source of truth for what to build and how.

## Design Decisions (Locked In -- Do Not Change)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1. Build tags for platform-specific process management.
- **Hybrid architecture**: Go daemon = reliability (polling, process mgmt, workspace isolation, retry, state). Orchestrator agent = intelligence (Claude session with VOICE.md, task decomposition, wave planning). Executors = headless Claude Code workers with harnesses.
- **VOICE.md**: `~/.anthem/VOICE.md`. Orchestrator agent only, NOT executors.
- **Constraints**: Two-tier (`~/.anthem/constraints.yaml` + `system.constraints` in WORKFLOW.md) with meta-constraint protection.
- **Global state root**: `~/.anthem/` (VOICE.md, constraints.yaml, state.json, audit.db, plans/, voice-changelog.md)
- **GitHub auth**: `GITHUB_TOKEN` env var, fallback to `gh auth token`.
- **Dashboard**: Fulfilled by Prism (separate repo).
- **Testing**: Interface-based mocks, table-driven tests, `//go:build integration` tagged tests, `testdata/` fixtures.
- **Logging**: `log/slog` (Go stdlib) for structured logging.
- **Error handling**: Wrap with `fmt.Errorf("context: %w", err)`. Never swallow errors.
- **No global state in code**: Dependency injection via constructors.
- **Template engine**: sprig (`github.com/Masterminds/sprig/v3`) for WORKFLOW.md body rendering.
- **Orchestrator module pattern**: All dispatch/reconciliation/state logic in `orchestrator.go`.
- **Orchestrator modes**: Plan (markdown-only), Build (plan -> subtasks), Agent (full JSON actions). Falls back to mechanical dispatch on failure.
- **Contract-first tool surface**: 10 action types with schemas, risk levels, idempotency. JSON structured output.
- **Three-layer state**: Event Log (SQLite audit), State Snapshot (in-memory), Knowledge Artifacts (repo docs).
- **Channel system**: Pluggable adapters (Slack Socket Mode, Dispatch WebSocket, Prism WebSocket).
- **Agent profiles**: Named profiles (coder/architect/tester/debugger/explorer) composing MCP server refs + skill refs.
- **Explorer subagents**: Three-phase plan pipeline (scout -> parallel explore -> synthesize).

Full details in `docs/plans/architecture.md`.

## Coding Standards

- No unnecessary comments. Don't narrate what code does. Only comment non-obvious intent, trade-offs, or constraints.
- Table-driven tests for all unit tests.
- Every external dependency (GitHub API, Claude Code CLI, filesystem) is behind an interface.
- Wrap errors with context: `fmt.Errorf("loading config: %w", err)`
- Use `log/slog` for all logging.
- No global mutable state -- dependency injection via constructors.

## Current Status

**Phase**: Phase 4 — Frontier Implementation (**complete**)
**Working plan**: `docs/plans/frontier-implementation.md` — completed checklist with detailed steps and file paths.
**Scaffold**: Complete (all 11 steps done)
**Phase 1**: Complete. All 10 steps implemented and verified with a live GitHub issue (pickup -> Claude Code execution -> issue closure -> label lifecycle).

All phases (1, 2, 3a, 3b, C, 4, post-4) are complete. Key capabilities shipped: GitHub issue-driven dispatch, rules engine, retry/backoff, state persistence, config hot-reload, graceful shutdown, contract-based orchestrator agent (10 action types), SQLite audit log, wave-aware dispatch, task lifecycle state machine, Slack + Dispatch + Prism channel adapters, LLM token streaming, maintenance scanner, DAG edges, multi-LLM driver, executor-reviewer loop, specialist agent profiles, decision traces, orchestrator codebase awareness, explorer subagent pipeline, plan/build/agent modes, plan-card UI, model selection, embedded skills (6 SKILL.md packages via go:embed).

See `docs/plans/architecture.md` and `docs/plans/implementation.md` for implementation details of completed phases.

**Phase 5 (future)**: WhatsApp channel adapter, GoReleaser binaries, code signing, demo video.

## MCP Platform -- Active Build Scope

This section describes the current work-in-progress: integrating shared feature context, tool brokering, and artifact pipelines into the guest agent system.

### Architecture Overview

Three tool types for guest agents:
- **MCP tools** -- persistent, stateful connections to complex tool surfaces (Unity Editor via mcp-unity). Managed via Conduit Pool (`github.com/rauriemo/conduit/pkg/mcpclient`).
- **Brokered HTTP/API tools** -- simple request/response calls to external APIs (image generation, video generation, 3D conversion). Anthem brokers these natively.
- **Pure reasoning agents** -- story, design, review agents. No tools; value comes from seeing shared feature context.

Anthem is the brokered execution engine for both MCP and HTTP/API tool calls. The model never calls tools directly -- Anthem intercepts `tool_use` blocks, enforces allowlists, executes the call, and injects the result back.

### Phase 1a: Context + Policy (current priority)

**1. Shared Feature Context (`.context/`)**

Projects (e.g. RebelTower) contain a `.context/features/{feature-name}/` directory with four files:
- `plan.md` -- YAML frontmatter (`schema_version`, `feature`, `phase`, `owner`) + markdown body
- `decisions.yaml` -- structured decision log (`schema_version: "1"`, list of decisions with `id`, `date`, `decision`, `rationale`, `decided_by`, `status: draft|final|superseded`, `affects`)
- `artifacts.yaml` -- asset/output registry (`schema_version: "1"`, list of artifacts with `id`, `type`, `path`, `created_by`, `created_at`, `feature`, `status: draft|pending-review|approved|rejected`, `approved_by`, `description`, `source_artifact`, `tags`)
- `task-state.yaml` -- agent activity (`schema_version: "1"`, `feature`, `phase`, `updated_at`, `agents` map with `status: idle|active|blocked|done`, `current_task`, `last_output`, `last_updated`)

**2. Context Hydration**

On guest dispatch, Anthem reads `.context/features/{active-feature}/` and injects a context block into the guest system prompt:

```
## Feature Context: {feature name}
Phase: {phase}

### Plan Summary
[contents of plan.md body]

### Recent Decisions
[final entries from decisions.yaml]

### Available Artifacts
[entries from artifacts.yaml where status != rejected]

### Your Activity
[this agent's section from task-state.yaml]
```

Implementation: new function in `internal/orchestrator/` that reads `.context/` files from the project workspace, parses YAML/frontmatter, and constructs the injection string. Called before guest prompt construction in `dispatchSelectedGuests`.

The active feature is determined from WORKFLOW.md frontmatter (new `active_feature` field) or from user message context.

TODO (post-v1): add relevance filtering as artifacts.yaml grows (cap by recency, e.g. last 20).

**3. Guest Policy Fields**

Add to `internal/guests/guests.go`:

```go
type frontmatter struct {
    // ... existing fields ...
    AllowedTools []string                             `yaml:"allowed_tools"`
    MCPServers   map[string]mcpconfig.MCPServerRef    `yaml:"mcp_servers"`
    HTTPTools    map[string]HTTPToolConfig             `yaml:"http_tools"`
}

type GuestAgent struct {
    // ... existing fields ...
    AllowedTools []string                             `json:"allowed_tools,omitempty"`
    MCPServers   map[string]mcpconfig.MCPServerRef    `json:"mcp_servers,omitempty"`
    HTTPTools    map[string]HTTPToolConfig             `json:"http_tools,omitempty"`
}
```

`MCPServerRef` comes from `github.com/rauriemo/conduit/pkg/mcpconfig` (Phase 1b import). Until then, define a local placeholder struct with the same shape.

`HTTPToolConfig` is Anthem-native:

```go
type HTTPToolConfig struct {
    URL              string            `yaml:"url" json:"url"`
    Method           string            `yaml:"method" json:"method"`
    AuthTokenEnv     string            `yaml:"auth_token_env,omitempty" json:"auth_token_env,omitempty"`
    AuthScheme       string            `yaml:"auth_scheme,omitempty" json:"auth_scheme,omitempty"` // v1: "bearer" only
    RequestTemplate  map[string]any    `yaml:"request_template,omitempty" json:"request_template,omitempty"`
    ResponseArtifact *ArtifactTemplate `yaml:"response_artifact,omitempty" json:"response_artifact,omitempty"`
    TimeoutMS        int               `yaml:"timeout_ms,omitempty" json:"timeout_ms,omitempty"`
    Description      string            `yaml:"description,omitempty" json:"description,omitempty"`
}

type ArtifactTemplate struct {
    Type   string `yaml:"type" json:"type"`
    SaveTo string `yaml:"save_to" json:"save_to"`
}
```

Validation: `auth_scheme` must be `"bearer"` or empty in v1. Reject other values at parse time.

**4. Replace resolveGuestTools**

Current `resolveGuestTools` in `guestdispatch.go` (line 585) uses a binary fingerprint check. Replace with:

```go
func resolveGuestTools(agent guests.GuestAgent) []string {
    if len(agent.AllowedTools) > 0 {
        return agent.AllowedTools
    }
    // Backward compat: fall back to fingerprint check for agents without allowed_tools
    fp := agent.RequirementsFingerprint
    emptyFP := "sha256:" + fmt.Sprintf("%x", sha256Sum(nil))
    if fp == emptyFP || fp == "" {
        return nil
    }
    return []string{"WebSearch", "WebFetch"}
}
```

**5. Allowlist Enforcement**

New function:

```go
func isToolAllowed(toolName string, allowedTools []string) bool
```

- Empty `allowedTools` with non-empty tool declarations = deny all (deny-by-default)
- Wildcard: `mcp__mcp-unity__*` matches all mcp-unity tools
- Wildcard: `http__image_gen__*` matches all image gen operations
- Exact: `WebSearch` matches only WebSearch
- No match = denied, error returned to Claude

### Phase 1b: Conduit Import (after Phase 1a validation)

After Phase 1a is validated end-to-end (dispatch a guest agent, confirm context injection works):

1. `go get github.com/rauriemo/conduit`
2. Replace `config.MCPServerConfig` (line 81 in `internal/config/config.go`) with `mcpconfig.MCPServerRef`
3. Replace `harness.WriteMCPConfig` (line 28 in `internal/harness/harness.go`) with `mcpbridge.WriteMCPConfig`
4. Remove local `mcpJSON`/`mcpServerEntry` structs from harness.go

### Phase 2: Brokered Execution (after Conduit import)

**Brokered MCP Execution:**
- On guest dispatch, if `MCPServers` is non-empty, connect Conduit Pool to each declared server
- In the agentic loop, when Claude emits `tool_use` with name starting `mcp__`: check allowlist, call `Pool.CallTool`, inject result
- Tool name format: `mcp__{server-name}__{tool-name}`

**Brokered HTTP/API Execution:**
- On guest dispatch, if `HTTPTools` is non-empty, expose each as a tool definition to the Claude API
- In the agentic loop, when Claude emits `tool_use` with name starting `http__`: check allowlist, render request template via `${input.*}` simple substitution (no Go `text/template`), sanitize `save_to` path (must stay within project root), execute HTTP request, save artifact, register in `artifacts.yaml`, inject result
- Tool name format: `http__{tool-name}__{operation}`

**Template engine (v1):** `${input.prompt}` is replaced with the value of `input.prompt` from tool_use input. No expression evaluation, no conditionals. Prevents template injection.

**Path sanitization:** `response_artifact.save_to` is canonicalized after substitution. Reject any path that escapes the project root (`../` traversal).

**Artifact Registration:**
- Write to `artifacts.yaml` in the feature directory
- Use `sync.Mutex` keyed by feature path to serialize concurrent writes
- Set `status: pending-review`, `created_by: {agent-role}`
- Update `task-state.yaml` agent section on dispatch (active) and completion (idle)

**Unified Brokered Execution Loop:**

```
while Claude has not emitted final response:
    response = call Claude API with messages + tool definitions

    if response contains tool_use:
        for each tool_use block:
            if tool name starts with "mcp__":
                check allowlist -> Pool.CallTool (Conduit)
            elif tool name starts with "http__":
                check allowlist -> render template -> HTTP request -> save artifact
            else:
                check allowlist -> built-in tool handler (WebSearch, etc.)
            inject tool_result into conversation

    elif response is final text:
        update task-state.yaml
        send notification if configured
        break
```

**Notifications:** On agent completion, send structured message to Prism:

```json
{
  "type": "agent_completed",
  "agent": "2d-artist",
  "feature": "tower-defense-level-1",
  "artifacts": [{"id": "...", "path": "...", "status": "pending-review"}],
  "message": "Generated goblin enemy sprite sheet (4-frame walk cycle)"
}
```

### Security Constraints

- `auth_scheme`: v1 supports `bearer` only. Validate at frontmatter parse time.
- Auth tokens: NEVER written to files. Only env var names stored. Resolved via `os.Getenv` at call time. If env var is empty, fail with clear error.
- Allowlists: per-guest, not per-server. Deny-by-default.
- Path sanitization: `save_to` must resolve within project root. Reject `../` traversal.
- Audit: log every tool invocation (tool name, agent, timestamp, success/failure). Log failed permission checks.

### Key Files to Modify

- `internal/guests/guests.go` -- add `AllowedTools`, `MCPServers`, `HTTPTools` to frontmatter/GuestAgent structs, add `HTTPToolConfig`/`ArtifactTemplate` types
- `internal/orchestrator/guestdispatch.go` -- replace `resolveGuestTools`, add `isToolAllowed`, wire context hydration into dispatch, add brokered execution loop
- `internal/config/config.go` -- `MCPServerConfig` will be replaced with Conduit import (Phase 1b)
- `internal/harness/harness.go` -- `WriteMCPConfig` will be replaced with Conduit bridge (Phase 1b)
- New file: `internal/orchestrator/context.go` (or similar) -- context hydration logic
- New file: `internal/orchestrator/toolbroker.go` (or similar) -- brokered execution engine

### Testing Requirements

- Frontmatter parsing: verify `AllowedTools`, `MCPServers`, `HTTPTools` parse correctly, verify `auth_scheme` validation rejects non-bearer
- `isToolAllowed`: wildcard matching, exact matching, deny-by-default, empty allowlist
- Path sanitization: reject `../` traversal, accept valid paths
- Context hydration: read `.context/` files, verify injection format
- Artifact registration: append to `artifacts.yaml`, verify mutex serialization
- Brokered HTTP execution: template substitution, HTTP call, artifact save (integration test with httptest)
- Brokered MCP execution: Pool.CallTool mock (integration test)

Update this section as phases are completed.

## Guest Agents (Shipped)

Guest agents are lightweight persona definitions (markdown files with YAML frontmatter) in a project's `agents/` directory. Anthem scans this directory on boot, generates `.agents-index.json`, and advertises the roster to Prism via `guest_agents` on `auth_ok`.

Full spec: `docs/plans/guest-agents.md`

### Orchestration runtime (`internal/orchestrator/`)

- **ConvoBuffer** (`convobuffer.go`): Per-channel 10-round ring buffer. `RecordUserMessage` finalizes the current round and starts a new one; `RecordResponse` appends speaker-labeled responses. `FormatHistory` renders 3 most recent rounds with 200-char truncation (backward-compatible default). `FormatHistoryN(rounds, maxRounds, truncLen)` provides parameterized formatting. `HasGuestSpoken(key, guestID)` detects first-turn guests for expanded history context. Fed into routing calls and guest prompts.
- **SharedContext** (`sharedcontext.go`): Per-channel in-memory session knowledge document. Updated every round across all modes. The routing call returns context updates; a post-round summarization call also runs when guests are active. Agent mode additionally extracts `context_update` from `OrchestratorResponse`.
- **Guest dispatch** (`guestdispatch.go`): Unified dispatch for all modes (fast/plan/agent). Routing always fires when 1+ guests are active via `routeToGuests` (haiku decides selection, directed text, and orchestrator participation). `RoutingResult` includes `DirectedText map[string]string` for per-guest focus extraction. `GuestPromptOpts.FocusText` appends a `## Your Focus` section after the user message. Fallback: if routing fails, broadcast to all with no focus. First-turn guests receive expanded history (10 rounds, 800-char truncation). `dispatchSelectedGuests` manages concurrency (semaphore of 3), plan edit serialization (`planEditMu`), and ConvoBuffer recording. `suggestGuestToInvite` evaluates post-round specialist suggestions (feature-flagged via `EnableGuestSuggestions`).
- **Wire protocol**: `active_guests` and `mention` on inbound `req` frames; `guest_id` on outbound `res` and `stream` frames (including `StreamDelta` and `StreamDone`). `display_ids` on outbound `res` frames when the response includes artifacts alongside text (collected during artifact broadcast, attached to the text message for frontend linking). @-mention bypasses routing and orchestrator.
- **StateSnapshot extensions**: `ActiveGuestsSummary`, `SharedContext`, `ConversationHistory` injected when guests are active. System prompt gains "Active Specialists" awareness section. `OrchestratorResponse.ContextUpdate` extracted and applied to SharedContext.

Key Anthem responsibilities:
- `internal/guests/` package: scan `agents/`, parse frontmatter, generate index, load persona on demand
- WebSocket protocol: `guest_agents` in auth, `active_guests`/`mention` in req, `guest_id`/`suggest_guest` in res
- Orchestrator: compressed context injection (roster summary for all active guests, full persona only for the responding guest), capability-matching suggestions
- Config: `guests` allowlist and `max_active_guests` in WORKFLOW.md frontmatter
- Fallback: `~/.anthem/agents/` for projects without an `agents/` directory

## Reference: Prism Channel Protocol

Prism uses the same WebSocket JSON frame protocol as Dispatch, extended with:
- `display` frames: `{"type":"display","component":{...},"thread":"<id>"}` for A2UI structured visual content. Display components include `kind:plan` (markdown plan artifacts).
- `stream` frames (Phase C): `{"type":"stream","text":"<delta>","thread":"<id>","done":false}` for incremental LLM output. When `done: true`, Prism knows the stream is complete and can finalize the message.
- `plan-card` messages: `[plan-card]{"title":"...","overview":"...","tasks":[...],"planPath":"..."}[/plan-card]` embedded in `res` frame text. The Prism backend (`_handle_anthem_message` in `main.py`) converts `res` frames to `chat` type before forwarding to the frontend. Prism's `PlanCard.tsx` parses these and renders a structured card with title, task list, "View Plan" (opens artifact pane), model dropdown, and "Build" button.

The Prism adapter at `internal/channel/prism/adapter.go` handles `Send()` for `res`, `event`, `display`, and `stream` frame types.

## Reference: OpenAI Symphony

When making implementation decisions, reference Symphony's codebase for proven patterns:
- Repository: https://github.com/openai/symphony
- Language: Elixir (GenServer-based orchestrator)
- Spec: `SPEC.md` in repo root
- Key patterns: single-module orchestrator, tracker adapters, workspace isolation, config parsing from markdown front matter
- Key difference: Symphony has no personality/voice concept. Anthem adds the orchestrator agent layer on top (Phase 3).
