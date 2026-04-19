# Guest Agents -- Anthem Implementation Spec

## Overview

Guest agents are lightweight persona definitions (markdown files with YAML frontmatter) that live in a project's `agents/` directory. Anthem scans this directory on boot, generates a `.agents-index.json` cache, advertises the roster to Prism via WebSocket, and injects the persona into the orchestrator prompt or dispatches the guest directly depending on the active mode.

**Mode integration (current):**

- **Chat mode** — `@mention` or `active_guests` selects which guests participate in the current round. Routing call decides directed text; guests run concurrently (semaphore 3). Structured invite intent (`/invite Tolkien`, `add Tolkien and Walt`) may auto-activate guests via `detectInviteIntent`; the detection biases toward false negatives and is skipped in Plan/Execute and on execplan gate clicks.
- **Plan mode** — orchestrator may author structured `ExecutionPlan`s that reference specific `agent_id`s from the guest roster. Active guests can also **reply and emit `plan-edit` blocks** for the current plan, but they run with **all tools stripped** (`RunOpts.ToolsDisabled = true`, `AllowedTools = []`, harness emits `--disallowedTools '*'`). No MCP, no HTTP tools, no Bash/Edit/Write. The guest's prompt carries an explicit "Plan Mode Tool Policy" section so the model is told this policy up front. Invite-intent auto-activation is disabled in this mode — a typed name is a reference, not a summons.
- **Execute mode** — `PlanRunner` is the sole authority for guest activation. It broadcasts `ActivateGuest`/`DeactivateGuest` per step so the Prism roster reflects "this step's owner is working" ephemerally. Guests run with their declared `allowed_tools` and full MCP access. Orchestrator-driven guest dispatch is gated off for the duration of Execute mode, and on the Plan→Execute handoff (`handleExecutePlanApproval`) the orchestrator broadcasts `DeactivateGuest` for every plan-time active guest before `PlanRunner` spawns so the roster starts empty.
- **Loop mode** — optional; guest roster is available to the `GitHubLoopBackend` just like in the pre-refactor orchestrator.

This spec covers all Anthem-side changes across all phases. Prism and Forge have their own specs.

## Shared Contract

### Agent file format

One markdown file per agent in `{project-root}/agents/`. YAML frontmatter + markdown body.

```yaml
---
# === Core identity (required) ===
name: Game Story Weaver
description: A creative and playful story writing assistant for video game narratives

# === LLM configuration (optional) ===
model: claude-opus-4-6
model_speed: standard          # "standard" | "fast"

# === Agent requirements (optional, maps to environment + local config) ===
requirements:
  internet: true
  packages:
    pip: [pandas, numpy]
    npm: []
  filesystem: read-write       # "read-write" | "read-only" | "none"

# === Tools and capabilities (optional, Managed Agents + Anthem v2) ===
tools:
  - type: agent_toolset_20260401
    configs:
      - name: web_search
        enabled: true
mcp_servers:
  - name: github-tools
    url: https://mcp.example.com/github
skills:
  - type: anthropic
    skill_id: xlsx
callable_agents:
  - game-animator

# === Anthem/Prism-specific (NOT synced to Managed Agents) ===
role: specialist
capabilities:
  - story arc design
  - branching narratives
  - character development
voice: google/Chirp3-HD-Kore
fallback_voice: edge/en-US-GuyNeural
icon: book
extra_context: |
  Project-specific instructions appended to the persona at load time.
---

We are Game Story Weaver -- a wildly creative, playful, and also
storytelling companion for video game writers...
```

Only `name` and `description` are required. Everything else is optional.

### MCP and HTTP tool declarations (Anthem + Claude Code)

For **Unity, semgrep, and other MCP servers**, guests may use flat frontmatter keys (parsed in `internal/guests/guests.go`):

- **`allowed_tools`** — list of strings; wildcards `mcp__{server-id}__*`, `http__{tool-id}__*`, or exact Claude Code tool names.
- **`mcp_servers`** — map keyed by **server id** (this id is the `mcp__{id}__` prefix in tool names). Values use `github.com/rauriemo/conduit/pkg/mcpconfig.MCPServerRef` (`type: stdio|http`, `command`, `args`, `env`, `url`, `headers`, etc.).
- **`http_tools`** — map of HTTP API tool configs (`HTTPToolConfig`); bearer auth only in v1 where validated.

On dispatch, Anthem merges each guest’s `mcp_servers` with the daemon’s global `agent.mcp_servers` and writes **`{projectRoot}/.mcp.json`**. **Claude Code** loads MCP from that file and executes tools; Anthem does not broker `Pool.CallTool` in v1.

**Unity:** Prefer **Unity’s official MCP** (`com.unity.ai.assistant` 2.x): relay binary + `--mcp`. Example server id `unity-mcp` → tools like `mcp__unity-mcp__Unity_GetConsoleLogs`. See [Unity MCP get started](https://docs.unity3d.com/Packages/com.unity.ai.assistant@2.0/manual/unity-mcp-get-started.html) and RebelTower `agents/eiji.md`.

### `.agents-index.json` schema

Generated by Anthem on boot and on file change. Never hand-edited.

```json
{
  "version": 1,
  "generated_at": "2026-04-09T10:30:00Z",
  "agents": {
    "<slug>": {
      "name": "string",
      "description": "string",
      "role": "string | null",
      "capabilities": ["string"],
      "icon": "string | null",
      "model": "string | null",
      "requirements_fingerprint": "sha256:...",
      "scope": "project",
      "source": "cloud | local",
      "file": "filename.md"
    }
  }
}
```

- `slug`: kebab-cased filename without `.md` extension
- `scope`: always `"project"` now. Reserved for `"user-global"`, `"org"`, `"session"` later.
- `source`: `"cloud"` if the agent appears in `.managed-sync.json`, else `"local"`
- `requirements_fingerprint`: SHA-256 of normalized `requirements` section (for per-profile cloud environments)

### WebSocket protocol additions

**Auth/status messages** -- new `guest_agents` field:
```json
{
  "type": "auth_ok",
  "guest_agents": [
    {"id": "game-story-weaver", "name": "Game Story Weaver", "description": "...", "role": "specialist", "capabilities": ["story arc design"], "icon": "book", "scope": "project"}
  ]
}
```

**Request messages** -- new `active_guests` and `mention` fields:
```json
{"type": "req", "id": "<uuid>", "text": "...", "active_guests": ["game-story-weaver", "code-reviewer"], "mention": "game-story-weaver"}
```

**Response messages** -- new `guest_id` and `suggest_guest` fields:
```json
{"type": "res", "id": "<uuid>", "text": "...", "guest_id": "game-story-weaver"}
{"type": "res", "id": "<uuid>", "text": "Should I bring in the Game Story Weaver?", "suggest_guest": {"id": "game-story-weaver", "reason": "This topic involves narrative design"}}
```

### WORKFLOW.md additions

Optional fields in WORKFLOW.md frontmatter:
```yaml
guests:
  - game-story-weaver
  - code-reviewer
max_active_guests: 4
```

- `guests`: Allowlist. When present, only listed agents are advertised. When absent, all agents in `agents/` are available.
- `max_active_guests`: Soft cap on concurrent active guests per chat session (default 4).

## Phase 1 -- Foundation

### New package: `internal/guests/`

New package for guest agent scanning, parsing, and index generation.

**`internal/guests/guests.go`**:
- `type GuestAgent struct` -- discovery-tier metadata: ID, Name, Description, Role, Capabilities, Icon, Model, RequirementsFingerprint, Scope, Source, File
- `type GuestIndex struct` -- Version, GeneratedAt, Agents map[string]GuestAgent
- `func ScanDirectory(dir string) (GuestIndex, error)` -- scans `agents/` directory, parses YAML frontmatter from each `.md` file, computes requirements fingerprint, returns populated index
- `func WriteIndex(dir string, index GuestIndex) error` -- writes `.agents-index.json` atomically
- `func LoadIndex(dir string) (GuestIndex, error)` -- reads `.agents-index.json` (fast path for Prism)
- `func LoadPersona(dir string, slug string) (string, error)` -- reads full markdown body of agent file (activation tier, on-demand)
- `func ParseFrontmatter(data []byte) (GuestAgent, error)` -- parses YAML frontmatter from markdown file, extracts discovery-tier fields
- `func ComputeRequirementsFingerprint(requirements map[string]any) string` -- SHA-256 of normalized requirements section

**Frontmatter parsing**: Use `gopkg.in/yaml.v3` to parse the YAML between `---` delimiters. Only extract frontmatter fields; the markdown body below the closing `---` is the persona text.

**`internal/guests/guests_test.go`**: Table-driven tests for scanning, parsing, index generation, fingerprinting, persona loading. Use `testdata/agents/` fixtures.

### Integration into config loading

In `internal/config/loader.go` or a new `internal/config/guests.go`:
- On `LoadFile()` and config hot-reload, call `guests.ScanDirectory()` for `{project-root}/agents/`
- If `{project-root}/agents/` doesn't exist, fall back to `~/.anthem/agents/` (user-global scope)
- If `guests` allowlist exists in WORKFLOW.md, filter the index to only listed agents
- Store the `GuestIndex` on the config struct, accessible to the orchestrator and channel adapters

### Integration into Prism adapter

In `internal/channel/prism/adapter.go`:
- Include `guest_agents` array in the `auth_ok` response frame after successful authentication
- Data sourced from the loaded `GuestIndex` -- discovery-tier metadata plus voice config
- Wire format (`guestAgentInfo` struct) includes: `id`, `name`, `description`, `role`, `capabilities`, `icon`, `model`, `quotes`, `requirementsFingerprint`, `scope`, `source`, `voice_id`, `voice_model`, `voice_priority`
- `UpdateGuestIndex` converts `GuestAgent` to `guestAgentInfo`, preserving all voice fields from frontmatter
- On config reload, broadcast updated `guest_agents` to all connected Prism clients

### Testdata fixtures

Add `testdata/agents/` with 2-3 sample agent markdown files for testing:
- `testdata/agents/game-designer.md` (full frontmatter + body)
- `testdata/agents/code-reviewer.md` (minimal frontmatter, no requirements)
- `testdata/agents/incomplete.md` (missing required fields, for error testing)

## Phase 2 -- Chat Composition (Persona Injection)

### Orchestrator changes (`internal/orchestrator/`)

**Request handling** -- in `HandleUserMessage`:
- Parse `active_guests` array from incoming request
- Parse `mention` field from incoming request
- If `active_guests` is present: build a **compact roster summary** block for the orchestrator prompt:
  ```
  ## Active Guest Agents
  - game-story-weaver (specialist): A creative story writing assistant for video game narratives
  - code-reviewer (reviewer): Systematic code review across security, performance, and quality
  ```
  This summary is ~50 tokens per guest. Keeps total overhead small.
- If `mention` is present: load the **full persona body** via `guests.LoadPersona()` and append it to the prompt as:
  ```
  ## Responding as: Game Story Weaver
  [full markdown body from agent file]
  
  Respond in character as Game Story Weaver. Use their perspective, expertise, and communication style.
  ```
- If no `mention` but `active_guests` is present: the orchestrator can choose to respond as a guest or as itself. Include instruction:
  ```
  You have guest agents available. If the user's message is better addressed by a specialist, respond as that specialist and include a `guest_id` field. Otherwise respond as yourself.
  ```

**Response handling**:
- Parse the orchestrator's response for guest_id indication
- Include `guest_id` in the outgoing `res` frame when the orchestrator responds as a guest
- Include `suggest_guest` when the orchestrator identifies a relevant inactive agent

**Capability matching for suggestions**:
- On each user message, if `active_guests` doesn't include all available agents, check if any inactive agent's `capabilities` keywords match the message content
- If a strong match is found, include `suggest_guest` in the response with the agent ID and a reason string
- The orchestrator's prompt should include the full agents index (names + capabilities) to enable this matching

**WORKFLOW.md support**:
- Parse `guests` allowlist from WORKFLOW.md frontmatter
- Parse `max_active_guests` from WORKFLOW.md frontmatter (default 4)
- Pass max_active_guests to Prism via auth/status message for client-side enforcement

### Prism adapter updates

- Parse `active_guests` and `mention` fields from incoming `req` frames
- Include `guest_id` and `suggest_guest` fields in outgoing `res` frames
- These fields are optional -- omit when null/empty to maintain backward compatibility with existing Prism versions

## Phase 3 -- Unified Guest Dispatch (Shipped)

### Runtime architecture (`internal/orchestrator/`)

Guest dispatch is unified across all modes (fast, plan, agent). Three new files:

**`convobuffer.go`** -- Per-channel 10-round conversation history ring buffer.
- `ConvoRound` holds `UserMessage string` and `Responses []ConvoResponse` (speaker + truncated text).
- `RecordUserMessage(channelID, text)` finalizes current round and pushes a new one. FIFO eviction at `maxHistoryRounds` (10).
- `RecordResponse(channelID, speaker, text)` appends to the current round.
- `FormatHistory(channelID)` renders rounds newest-first as `[Round N] User: ... / Speaker: ...` with 200-char truncation.
- `FormatHistoryN(rounds, maxRounds, truncLen)` is a parameterized alternative to `FormatHistory` (same rendering shape, caller supplies round cap and truncation length).
- `HasGuestSpoken(key, guestID) bool` reports whether the given guest has spoken in the buffered history for the channel.

**`sharedcontext.go`** -- Per-channel in-memory session knowledge document.
- `Get(channelID) string` returns current context (empty string if none).
- `Update(channelID, content)` overwrites the document.
- Updated after every round: the routing call returns context updates; a post-round summarization call also runs when guests are active. Agent mode additionally extracts `context_update` from `OrchestratorResponse`.

**`guestdispatch.go`** -- Core routing, prompt building, and invocation logic.
- Routing always fires when 1+ guests are active (the router decides selection, focus, and orchestrator participation). `routeToGuests(ctx, runner, guests, userMsg, history, sharedCtx, plan)` performs a cheap model call and returns a `RoutingResult` with selected `Guests []GuestSummary`, `ContextUpdate string`, and `DirectedText map[string]string` for per-guest focus extraction. Falls back to broadcasting all guests on routing failure.
- `buildGuestPrompt(opts GuestPromptOpts)` assembles: persona body + project context + shared context + conversation history + user message. `GuestPromptOpts` includes `FocusText string` — when set, appends a `## Your Focus` section after the user message in the prompt. Mode-specific suffixes: plan mode includes active plan markdown and instructions for `plan-edit` code blocks; agent mode includes project state summary.
- `suggestGuestToInvite` provides post-round specialist suggestions; feature-flagged via `EnableGuestSuggestions`.
- `invokeGuestStreaming(ctx, runner, prompt, guestID, channelID, send)` runs `agent.AgentRunner.Run` with `OnStream` callback for real-time streaming via the Prism adapter.
- `extractPlanEdit(response)` parses `plan-edit` fenced code blocks from guest responses. Returns the content for application via `planStore.Save`. Thread-safe behind `planEditMu sync.Mutex`.
- `dispatchSelectedGuests(ctx, params)` manages concurrent guest invocations with a semaphore (max 3 concurrent), records responses in ConvoBuffer, applies plan edits, and handles errors per-guest.

### Orchestrator integration (`orchestrator.go`)

`HandleUserMessage` extended with:
- `convoBuf *ConvoBuffer` and `sharedCtx *SharedContext` initialized in `New()`.
- `detectMode(msg)` returns `"fast"`, `"plan"`, or `"agent"`.
- **@-mention fast path**: `handleGuestMention` bypasses orchestrator entirely, invokes the named guest with full context via `invokeGuestStreaming`, records in ConvoBuffer, sends response.
- **Unified dispatch**: When `active_guests` are set, `buildGuestSummaries` compiles the roster. `routeToGuests` always runs when guests are active (1+), deciding selection, directed text, and orchestrator participation. `directedText` is passed through `guestDispatchParams` to `dispatchSelectedGuests`. `dispatchSelectedGuests` runs in a goroutine alongside the normal orchestrator mode handler.
- **StateSnapshot extensions**: `ActiveGuestsSummary`, `SharedContext`, `ConversationHistory` injected when guests are present. `buildSystemPrompt` gains "Active Specialists" awareness section and "Response Format" guidance for `context_update` JSON field.
- **`finalizeGuestRound`**: Waits for guest goroutine, then always runs context update when guests are active, and conditionally calls `suggestFollowUp` when `EnableGuestSuggestions` is true.
- `guestsDir()` resolves `{project-root}/agents/` path.

### Wire protocol additions

- `sendStream` in `internal/channel/prism/adapter.go` now includes `GuestID` in `frame` struct for both `StreamDelta` and `StreamDone` messages.
- `OrchestratorResponse` extended with `ContextUpdate string` for SharedContext writes from agent mode.
- `OrchestratorAgent.lastResp` stores the latest response; `LastResponse()` accessor used by `HandleUserMessage` to extract `ContextUpdate`.

## Phase 4 -- Cloud Execution (Planned)

### Cloud backend preparation

Anthem doesn't directly call Managed Agents (that's Prism's job). But it needs to:
- Compile context and pass it to Prism via a new `dispatch_guest` action or frame type
- Include the `requirements_fingerprint` so Prism can select the right environment
- Receive streamed results back from Prism and route to the orchestrator for evaluation

## Testing Strategy

All new code must have table-driven unit tests. Follow existing patterns: interface-based mocks, `testdata/` fixtures, `fmt.Errorf` wrapping, `slog` logging.

### `internal/guests/` package tests

**Frontmatter parsing** (`ParseFrontmatter`):
- Valid full frontmatter (all fields populated)
- Minimal frontmatter (only `name` + `description`)
- Missing required fields (`name` missing, `description` missing) -- expect error
- Malformed YAML between delimiters -- expect error with context
- Missing closing `---` delimiter -- expect error
- Empty file -- expect error
- File with frontmatter but no markdown body -- valid, empty persona
- Non-UTF8 content in body -- should not crash, treat as opaque bytes
- Extra unknown fields in frontmatter -- silently ignored (forward compat)

**Directory scanning** (`ScanDirectory`):
- Normal directory with 2-3 valid agent files
- Empty `agents/` directory -- returns empty index, no error
- Directory does not exist -- returns error with path context
- Directory contains non-`.md` files (`.txt`, `.json`, subdirectories) -- ignored
- Mix of valid and invalid files -- valid ones indexed, invalid ones logged and skipped (not fatal)
- Permission error on a single file -- skip with warning, don't abort scan
- Large directory (parametrize with 50+ files if feasible) -- completes without excessive memory

**Index generation** (`WriteIndex`, `LoadIndex`):
- Round-trip: write then load produces identical GuestIndex
- Atomic write: partial write doesn't corrupt existing index (write to temp then rename)
- Load from missing file -- returns error (not empty index)
- Load from corrupted JSON -- returns error with context
- Index `version` field set correctly
- `generated_at` timestamp is recent (within 1 second of write)

**Persona loading** (`LoadPersona`):
- Returns full markdown body below closing `---`
- Agent file exists but body is empty -- returns empty string, no error
- Agent file does not exist -- returns error with slug context

**Requirements fingerprint** (`ComputeRequirementsFingerprint`):
- Deterministic: same requirements -> same hash every time
- Different requirements -> different hash
- Field ordering doesn't affect hash (normalize before hashing)
- Nil/empty requirements -> consistent fallback hash
- Fingerprint format is `sha256:<hex>`

**Fallback resolution**:
- Project has `agents/` -- only project agents returned, `~/.anthem/agents/` ignored
- Project has no `agents/` -- falls back to `~/.anthem/agents/`
- Neither directory exists -- empty roster, no error

**WORKFLOW.md allowlist filtering**:
- `guests` list present -- only listed agents in index
- `guests` list absent -- all agents in index
- `guests` contains nonexistent agent slug -- ignored (no error, just filtered out)
- `guests` is empty list -- empty roster (intentional lockdown)
- `guests` contains duplicates -- deduplicated

### Prism adapter tests (`internal/channel/prism/adapter_test.go`)

**Protocol backward compatibility**:
- `auth_ok` with `guest_agents` field -- new Prism client parses correctly
- `auth_ok` with `guest_agents` field -- old Prism client (no guest support) ignores unknown field gracefully. Test by verifying the JSON is valid and existing fields are unchanged.
- `req` with `active_guests` and `mention` fields -- adapter parses correctly
- `req` without `active_guests`/`mention` (old client) -- fields default to nil/empty, no error
- `res` with `guest_id` -- round-trip through adapter
- `res` with `display_ids` -- round-trip through adapter (artifact links for chat bubbles)
- `res` with `suggest_guest` object -- round-trip through adapter
- `res` without guest fields -- unchanged behavior (backward compat)

**Config reload broadcast**:
- When guest index is regenerated (agent file added/removed/changed), connected Prism clients receive updated `guest_agents` array
- Multiple connected Prism clients all receive the update

### Orchestrator tests (`internal/orchestrator/`)

**Persona injection** (Phase 2):
- Compact roster summary generated correctly for N active guests (verify format, ~50 tokens per guest)
- Full persona loaded and injected only for the @-mentioned guest
- No persona injected when `active_guests` is empty
- `mention` field routes to correct agent; response includes `guest_id`
- Unknown `mention` slug -- error response, not crash

**Capability matching** (Phase 2):
- Orchestrator suggests inactive agent when capabilities match conversation topic
- No suggestion when all relevant agents already active
- No suggestion when no agents match
- `suggest_guest` response includes `id` and `reason` string

**Context budget** (Phase 2):
- Roster summary stays within expected token budget as active guest count grows (test with 1, 2, 4 guests)
- Full persona injection doesn't exceed reasonable prompt size limits

**Guest dispatch tests** (`guestdispatch_test.go` -- Shipped):
- `extractPlanEdit`: with plan-edit block, without block, nested blocks
- `buildGuestPrompt`: all modes (fast/plan/agent), section inclusion/omission based on opts
- `extractJSON`: valid JSON, no JSON, nested objects, unclosed braces
- `fallbackAllGuests`: normal fallback, empty guest list
- Routing: verify `routeToGuests` runs whenever 1+ guests are active (no skip-by-count threshold)

**ConvoBuffer tests** (`convobuffer_test.go` -- Shipped):
- `RecordUserMessage` + `RecordResponse` basic flow
- FIFO eviction at 10 rounds (newest kept, oldest dropped)
- `History` returns rounds most-recent-first
- `FormatHistory` truncation at 200 chars, empty history
- `MultipleChannels`: independent channel isolation

**SharedContext tests** (`sharedcontext_test.go` -- Shipped):
- `GetEmpty`: returns empty string for unknown channel
- `UpdateAndGet`: round-trip write/read
- `Overwrite`: second update replaces first
- `MultipleChannels`: independent channel isolation

**OrchestratorAgent tests** (`orchagent_test.go` -- Updated):
- `parseActions` now returns 3 values: `([]Action, *OrchestratorResponse, error)`. All test call sites capture the `OrchestratorResponse` (or discard with `_`)

**Config hot-reload integration**:
- Adding a new `.md` file to `agents/` -- appears in next scan
- Removing a `.md` file -- disappears from index
- Modifying a `.md` file -- index updated with new metadata
- Renaming a file -- old slug removed, new slug added

### Testdata fixtures

- `testdata/agents/game-designer.md` -- full frontmatter (all fields) + multi-paragraph body
- `testdata/agents/code-reviewer.md` -- minimal frontmatter (name + description only), short body
- `testdata/agents/incomplete.md` -- missing `name` field, for error testing
- `testdata/agents/malformed.md` -- invalid YAML in frontmatter, for error testing
- `testdata/agents/no-body.md` -- valid frontmatter, empty body
- `testdata/agents/ignore-me.txt` -- non-markdown file, should be skipped by scanner
