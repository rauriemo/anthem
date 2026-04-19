# Modes

Anthem is a project agent runtime with four first-class modes: **Chat**, **Plan**, **Execute**, and **Loop**. Every inbound message (and every internally-generated task) runs in exactly one mode. This document is the canonical reference for what each mode does, who decides what runs next, and how they are selected.

## Mode enum

Defined in `internal/types/task.go`:

```go
type Mode string

const (
    ModeChat    Mode = "chat"
    ModePlan    Mode = "plan"
    ModeExecute Mode = "execute"
    ModeLoop    Mode = "loop"
)
```

`Orchestrator.CurrentMode` is the observable state that channel adapters (Prism in particular) surface back to the user. It is updated at the point of routing, not deep inside a handler, so the UI mode indicator stays in sync with whatever Anthem is actually doing.

## Mode selection

Modes are selected explicitly. The inbound `req` frame carries the raw text; the router calls `detectMode(text)` which looks for `[system:<mode>]` tags:

| Tag | Mode |
|-----|------|
| `[system:chat]` | Chat |
| `[system:plan]` | Plan |
| `[system:plan:new]` | Plan (archive the current draft and start a fresh one; stripped before processing) |
| `[system:execute]` | Execute (compile + dispatch a plan) |
| `[system:loop]` | Loop (start the configured execution backend) |
| *(no tag)* | Chat (default) |

Tag detection is strict: `parseControlTags` only recognises `[system:…]` / `[model:…]` at the leading prefix of the message, ignoring tag-literals that appear inside backticks, fenced code blocks, or the middle of prose. Prism's chat-mode dropdown translates its UI selection into the right tag before sending. Legacy tags from the pre-refactor codebase (`[system:fast]`, `[system:agent]`, `[system:build]`, `[system:status]`) are accepted at the prefix and remapped for backward compatibility.

## Chat mode

**Purpose:** conversational replies, quick questions, short edits, on-the-fly guest mentions. Chat surfaces the active project scope directly by hydrating feature and shared context into every prompt, so the agent "sees" the project without needing tool calls for most questions.

**Who decides what runs:** the orchestrator agent, a persistent Claude session primed with `orchestrator.md` + `VOICE.md`. If `active_guests` are set, the router decides which guest(s) respond and what focus each gets (`routeToGuests`). Direct `@mentions` bypass the router.

**Context hydration:** before each turn, Chat injects three sources into the prompt:

1. **Project Context** -- `projectCtx.ProjectSummary` (from `project-summary.md`).
2. **Feature Context** -- `HydrateFeatureContext(projectRoot, activeFeature)`: the plan summary, recent decisions, last ~15 changelog entries, and current agent + artifact state from `.context/features/<feature>/`.
3. **Shared Context** -- the per-channel `SharedContext.Get(channelKind)` document.

Empty sources are omitted cleanly. Missing feature directories degrade silently.

**Guardrails:**

- **Turn budget:** `orchestrator.chat_max_turns` (default `2`) -- enough for the agent to produce a final answer and optionally make a single read-only tool call along the way.
- **Read-only tools allowed:** `Read`, `Grep`, `Glob`, `Bash`, `Task`, and configured MCP tools. The agent is instructed to prefer the hydrated context and reserve the one tool call for targeted lookups the hydrated context doesn't cover.
- **Write tools denied:** `Write`, `Edit`, `MultiEdit` are in `DeniedTools`. Edit or multi-step work is routed to Plan or Execute mode.
- No autonomous task dispatch, no tracker writes, no plan commits. Chat does not start Execute or Loop on its own.

**Outputs:** `stream` + `res` frames in the chat, optional `display` frames for visual content, and tool-call frames when the agent exercises its one read-only tool call.

Relevant code: `internal/orchestrator/orchestrator.go` (`HandleUserMessage`, `detectMode`, `handleLeanMessage`, `buildLeanPrompt`), `internal/orchestrator/featurecontext.go`, `internal/orchestrator/sharedcontext.go`, `internal/orchestrator/guestdispatch.go`.

## Plan mode

**Purpose:** produce an evidence-backed plan for a non-trivial change. The output is a markdown plan, stored and linkable, that feeds directly into Execute or Loop.

**Who decides what runs:** a dedicated plan pipeline with three phases:

1. **Scout** (turn-budgeted): read the file tree, identify 1-5 areas needing focused research.
2. **Explore** (parallel): the daemon spawns read-only Claude Code processes, one per area, each with a focused research question.
3. **Synthesize**: the plan agent receives all explorer findings and produces the markdown plan.

For trivial requests (scout returns 0 explore tasks), Plan falls back to a single-run consultation.

**Guardrails:** write tools (`Write`, `Edit`, `MultiEdit`) are denied across the entire pipeline. The plan agent uses a dedicated prompt that omits JSON actions and HTML display instructions, so the output is always fenced markdown inside `anthem-plan` blocks.

**Guest contributions in Plan mode.** Active guests can reply and emit `plan-edit` blocks, but they run with **all tools stripped**. Specifically:

- `dispatchSelectedGuests` forces `AllowedTools = []` when `mode == types.ModePlan` regardless of what the guest's frontmatter declares.
- `types.RunOpts.ToolsDisabled = true` is threaded through to the harness; `claude/driver.go` translates this to `--disallowedTools '*'`, which dominates every other tool-resolution path. Any new tool-resolution code must honor this flag first.
- The guest's prompt carries an explicit "Plan Mode Tool Policy" section telling the agent to respond in text only and to use `plan-edit` blocks for any plan changes.

This layered strip (prompt + harness flag + frontmatter override) is deliberate belt-and-suspenders: a guest summoned into Plan mode cannot execute tools even if one of the layers is accidentally weakened by a future refactor.

**Invite-intent detection is convenience, not authority.** `detectInviteIntent` matches a tight verb+name regex (`invite|add|bring in|pull in` followed by an adjacent token that resolves to a known guest id/name, with chained separators for "A and B"). The detection **is skipped entirely** when the message is in Plan or Execute mode, or when it is an execplan gate click, so a typed name in a Plan-mode message is treated as a reference, not a summons. The design principle is to bias toward false negatives over false positives: a missed auto-invite is one `@mention` to correct; a false-positive auto-activation burns tokens and (pre-L2) could fire tools on misread intent.

**Stable plan artifacts:** every plan has a persistent UUIDv4 `ID` in its YAML frontmatter, generated on first save and backfilled for legacy files on load. The Plan mode pipeline derives a stable `DisplayID = "plan:" + plan.Frontmatter.ID` when broadcasting the artifact to Prism. The displayStore upserts artifacts by `displayId`, so iterating on a plan replaces content *in place* in the same artifact slot rather than spamming new cards on every refinement — navigation position and action state are preserved across upserts. Random fallback `DisplayID`s are only used when no plan was persisted (e.g. malformed output).

**Starting a fresh plan:** to archive the current draft and begin a new one, send `[system:plan:new]` (Prism exposes this as the `/newplan` slash command, aliased to `/plan:new`). The handler marks the current draft `StatusArchived` (which is filtered out of `LatestDraft`) and then proceeds as a normal Plan turn on fresh content. Archived plans are kept on disk for audit; they simply stop showing up as the "current" draft.

**Outputs:**
- Markdown plan saved to `~/.anthem/plans/{project-slug}/` with YAML frontmatter (`ID`, status, `CompileGeneration`, `CompiledAt`, `RequiredProfiles`).
- A **plan artifact** broadcast with stable `DisplayID = "plan:<UUID>"`, which Prism upserts into the same slot on each revision.
- A side-effect: the active plan artifact is kept in the orchestrator's ProjectContext for Chat and Execute handoff.

Relevant code: `internal/orchestrator/planmode.go` (or equivalent), `internal/plans/store.go`, `internal/orchestrator/orchestrator.go` (`parseControlTags`, `[system:plan:new]` handler).

## Execute mode

**Purpose:** run an approved multi-agent handoff chain. Execute is the mode that answers "I have a plan with N steps across guests A, B, C -- now do it, and show me each step so I can approve before it advances."

**Markdown-to-ExecutionPlan compiler.** Plan mode produces markdown, but `PlanRunner` consumes a structured `ExecutionPlan`. Execute bridges the two with an orchestrator-agent compile pass (`OrchestratorAgent.ConsultCompilePlan`):

1. An Execute message without an inline `[execute:plan]` YAML block calls `handleExecuteMarkdown`, which resolves a markdown source with **inline-beats-stored-draft** precedence.
2. A per-plan-ID compile mutex (`compileInFlight`) blocks concurrent compiles of the same plan; overlapping requests get a short guidance reply rather than racing.
3. `ConsultCompilePlan` is given the markdown, the current active guest roster, and any prior compiled plan (for `revise`). The agent emits JSON for `ExecutionPlan`; the compiler prompt explicitly requires it to **preserve `step.id` and `gate.id` across revisions** so Prism's UI state, button wiring, and approval lineage do not thrash.
4. The result is validated against the active guest set (missing profile → explicit error reply), then persisted atomically as a **sidecar** next to the markdown: `<planPath>.execute.json` via `planStore.SaveCompiled`. `SaveCompiled` writes `<planPath>.execute.json.tmp`, bumps the plan's `Frontmatter.CompileGeneration`, stamps `CompiledAt` and `RequiredProfiles`, then atomically renames. A frontmatter-update failure rolls back the tmp file; a rename failure leaves a detectable drift state which `LoadCompiled` surfaces via `ErrCompiledSidecarDrift` (comparing embedded `Metadata.PlanGeneration` against current `Frontmatter.CompileGeneration`).
5. The compiled plan is broadcast as an `execplan` A2UI component with stable `displayId = "execplan:" + plan.Frontmatter.ID` — an independent namespace from `plan:<ID>`, so plan-mode iteration and execplan iteration never accidentally overwrite each other.

**Approval authority.** Prism's `ExecutionPlanView` renders the compiled plan with **Approve and dispatch / Revise / Abort** buttons. Buttons are the *only* way to advance a compiled plan in v1 — typed "approve"/"go"/"dispatch" free-text commands are explicitly **not** honored, so there is a single control plane for the state transition. Buttons emit a window message that the general dashboard rewrites to `[system:execute] [gate:<action>:execplan:<planID>:<compileGeneration>] <optional revision>`. `handleExecutePlanApproval` then:

- Parses the gate tag via `execPlanGateRe` and looks up the plan by stable `ID`.
- Rejects stale generations (user clicked an old artifact after a revise round-trip).
- Rejects sidecar drift (`ErrCompiledSidecarDrift`).
- Rejects rolled-back frontmatter.
- For `revise`, re-enters `handleExecuteMarkdown` with the user's feedback and the prior `ExecutionPlan` (preserving IDs).
- For `abort`, stops without dispatch.
- For `approve`, re-validates that every `RequiredProfile` is still present in the active guest roster (the roster may have changed between compile and approve) and then dispatches to `handleExecuteMessage` which hands off to `PlanRunner`.

**Who decides what runs at runtime.** Code. The `PlanRunner` in `internal/execute/runner.go` walks a validated `ExecutionPlan` step-by-step. The runner owns:

- Step state (`pending -> running -> completed/failed`).
- Dependency resolution (only steps whose `DependsOn` is completed are runnable).
- Gate state (open / resolved / aborted).
- Artifact collection and upstream injection.
- Event emission to Prism.
- Failure / pause semantics (no autonomous retries in v1).

**PlanRunner is the sole guest activator in Execute mode.** Orchestrator-driven guest dispatch is gated off for the duration of Execute mode (`dispatchBlockedByMode = preMsgMode == types.ModeExecute || isExecPlanGateAction(text)`). Instead, `PlanRunner` broadcasts `ActivateGuest` at the start of each step and `DeactivateGuest` at the end, so the Prism roster always reflects "this step's owner is working" while a step runs and returns to empty between steps. The pairing rules are:

- `runStep` start: broadcast `ActivateGuest` with the step's `AgentID`.
- No-gate completion: broadcast `DeactivateGuest` with reason `"step <id> complete"`.
- `handleGate` / `GateApprove`: broadcast `DeactivateGuest` ("step complete") — the next step's `ActivateGuest` will follow.
- `handleGate` / `GateRevise`: **retain** the chip. The same agent is about to re-run, and flicker between revise click and next StepStarted is exactly what this invariant is preventing.
- `handleGate` / `GateAbort` and `handleStepFailure` / `GateAbort`: broadcast `DeactivateGuest` ("step aborted").

**Plan→Execute roster handoff.** Before `PlanRunner` spawns, `handleExecutePlanApproval` broadcasts `DeactivateGuest` for every guest in `msg.ActiveGuests` with reason `"plan approved, entering execute"`. This closes the "who's working vs who's watching" ambiguity — after Approve, the Prism roster is empty and the only chip that appears belongs to the step currently running. Any code that spawns `PlanRunner` MUST broadcast the deactivation frames first; breaking this ordering re-introduces mixed-roster UI state.

The agent owns:
- Compiling human intent into the `ExecutionPlan` (typically during Plan mode).
- Producing per-step prompts and context.
- Doing the actual creative / analytical / implementation work in the step.

**ExecutionPlan schema** (`internal/execute/plan.go`):

```go
type ExecutionPlan struct {
    Steps    []PlanStep
    Gates    []ApprovalGate
    Metadata PlanMetadata
}

type PlanStep struct {
    ID          string
    AgentID     string     // must resolve to a guest in the project roster
    Description string
    DependsOn   string     // optional; v1 is linear, so usually just the previous step
    Status      StepStatus // pending / running / completed / failed
}

type ApprovalGate struct {
    ID        string
    AfterStep string
    Prompt    string
}

type PlanMetadata struct {
    Title           string
    Description     string
    CreatedAt       time.Time
    PlanGeneration  int        // matches Plan.Frontmatter.CompileGeneration at compile time; used to detect sidecar drift
}
```

`Validate(guestIDs)` enforces: non-empty steps, unique step IDs, known `AgentID`s, no cycles in `DependsOn`, unique gate IDs, gate `AfterStep` points to an existing step.

**Artifact flow:**

- Before each step, the runner calls `ArtifactProvider.MarkStepStart(stepID)` so the provider can snapshot state.
- After the step, the runner calls `ArtifactProvider.Collect(stepID)` to gather what the step produced.
- Before the next step, the runner calls `ArtifactProvider.Inject(nextStepID, upstreamArtifacts)` so the next guest can see what came before.

Two providers ship in v1:

- `ContextArtifactProvider` -- reads `.context/features/<feature>/artifacts.yaml` and writes `step-<id>-upstream.yaml` manifests. This is the preferred provider when the project uses the `.context/` convention (which Forge now scaffolds by default).
- `FilesystemArtifactProvider` -- fallback for projects without `.context/`. Uses file modtime snapshots to detect what changed.

**Approval gates:**

When the step after which a gate is configured completes, the runner emits `execution.gate_opened` with the collected artifacts and blocks. Prism renders the gate using the artifact summary (or, if the guest produced HTML, inside a sandboxed frame). Prism always owns the gate controls (`Approve` / `Revise` / `Abort`) -- never the HTML content. Anthem owns the gate state and records the resolution in the audit log.

**Execplan gate protocol** (stable, emitted by Prism buttons):

`[gate:<action>:execplan:<planID>:<compileGeneration>] <optional revision text>`

Where `<action>` is `approve`, `revise`, or `abort`. The `compileGeneration` prevents stale clicks: if the user clicked an artifact that has since been revised, the server rejects the tag instead of silently dispatching the wrong plan.

**Execution event protocol** (stable, consumed by Prism):

| Event | Emitted when | Payload shape |
|-------|--------------|---------------|
| `execution.plan_loaded` | Plan accepted and validated | `{title, step_count, gate_count}` |
| `execution.step_queued` | Step becomes eligible | `{step_id, agent_id, description}` |
| `execution.step_started` | Step dispatched | `{step_id, agent_id}` |
| `execution.step_completed` | Step finished, artifacts collected | `{step_id, agent_id, artifacts}` |
| `execution.step_failed` | Step errored, plan paused | `{step_id, agent_id, error}` |
| `execution.gate_opened` | Gate active, waiting | `{gate_id, prompt, artifacts, allowed_actions}` |
| `execution.gate_resolved` | Human resolved gate | `{gate_id, resolution, feedback}` |
| `execution.plan_completed` | Every step completed | `{title, total_steps, completed_steps}` |
| `execution.plan_aborted` | Plan aborted | `{title, total_steps, completed_steps}` |

Payloads are JSON-encoded in the `OutgoingMessage.Text` field; the `EventType` field carries the event name. Prism routes by `EventType`.

**Design principles locked in for Execute v1:**

- Code owns control flow. Agents never decide that a step is "done enough" or that a dependency is satisfied -- the runner does.
- Compile = agent, dispatch = code. The orchestrator agent is trusted to translate human intent into a structured `ExecutionPlan`. Everything after "approve" (step state, gates, artifact lineage, failure semantics) is deterministic.
- **Single approval authority.** Prism buttons are the only way to approve a compiled plan. Typed "approve"/"go"/"dispatch" text is explicitly ignored in Execute mode. This eliminates the two-control-planes race.
- **Generation-checked clicks.** Every button emission includes the `compileGeneration` it was rendered against; stale clicks are rejected server-side.
- **Stable IDs across revisions.** The compile prompt requires ID preservation so Prism can upsert `execplan:<UUID>` in place, and so button wiring, diffing, and gate state remain coherent across `revise` cycles.
- No silent retries. Failures pause the plan. Humans (or revision gates) decide whether to retry or abort.
- Linear chains only. Parallel DAG branches and `for_each` fan-out are deferred to Execute v2.
- Typed review surfaces are designed at the protocol level (`review_kind`, `template_hint` are reserved on gate payloads) but the v1 UI always falls back to a generic HTML-or-summary view with Prism-owned gate controls.

Relevant code: `internal/execute/plan.go`, `internal/execute/runner.go`, `internal/execute/artifacts.go`, `internal/execute/events.go`, `internal/orchestrator/orchagent.go` (`ConsultCompilePlan`), `internal/orchestrator/execcompile.go` (`handleExecuteMarkdown`, `handleExecutePlanApproval`), `internal/plans/compiled.go` (`SaveCompiled`, `LoadCompiled`).

### Execute v2 (deferred)

Out of scope for v1 but reserved in the schema / protocol:

- Parallel DAG branches beyond a single `DependsOn` edge.
- `for_each` fan-out (one step produces a manifest of N items; subsequent step runs once per item, fanning back in later).
- Richer artifact types (per-item regenerate, versioned revisions).
- Native Prism renderers for specific `review_kind`s (image gallery, animation preview, scene review, diff review).
- Per-step cost budgets and timeouts beyond the global agent caps.

## Loop mode

**Purpose:** opt-in autonomous backend. This is what Anthem used to be by default -- poll a work source, claim tasks, dispatch Claude Code workers, retry failures, close on completion.

**Who decides what runs:** an `ExecutionBackend` implementation. `internal/backend/backend.go` defines the interface:

```go
type ExecutionBackend interface {
    Start(ctx context.Context) error
    Stop()
    QueueWork(items []types.Task) error
    ActiveWork() []types.Task
    OnProgress(callback func(event BackendEvent))
}

type LoopHost interface {
    Tick(ctx context.Context)
    PollingIntervalMS() int
}
```

`GitHubLoopBackend` (`internal/backend/github.go`) is the shipping implementation. It wraps the old `tick()` loop: reconcile active runs, fetch eligible issues, apply rules, claim and dispatch, track completion, retry on failure.

**When Loop starts:**

- Explicit user request via `[system:loop]`.
- (Optional, project-level) auto-start when `WORKFLOW.md` has a `tracker:` block and `loop.auto_start: true` -- documented in the forge scaffolds.

**Guardrails:** Loop is the only mode that writes to the tracker (labels, comments, closures). It coexists with the conversational modes -- users can chat, plan, and execute handoff chains while Loop is also running issues in the background.

**Outputs:**

- Tracker updates (labels, comments, closures).
- Backend events via `OnProgress` (dispatch, completion, failure, retry-queued).
- Audit log entries for every decision and state transition.

Relevant code: `internal/backend/backend.go`, `internal/backend/github.go`, `internal/orchestrator/orchestrator.go` (`Run()` method wiring the backend via `LoopHost`).

## Mode coexistence

Modes are not mutually exclusive. A running Anthem can:

- Hold a Chat conversation while an Execute plan is paused at a gate.
- Have Loop polling GitHub in the background while the user is drafting a Plan in the foreground.
- Receive a new message mid-Execute -- that message runs in its own mode (usually Chat) and does not interrupt the runner.

Each mode has its own handler state. The shared substrate -- guest registry, `.context/`, channel pipe, audit log -- is concurrency-safe across all four.

## Adding a new mode

Only do this if the behavior genuinely does not fit any existing mode. New modes require:

1. A `Mode` constant in `internal/types/task.go`.
2. A tag in the `[system:<mode>]` grammar and a `detectMode` case.
3. A handler routed from `HandleUserMessage`.
4. A `current_mode` value Prism understands (update the mode dropdown and indicator).
5. An entry in this document.

Do not overload existing modes with sub-behaviors that belong in a separate mode. If Execute grows a non-linear scheduling variant, that is Execute v2, not a new mode.
