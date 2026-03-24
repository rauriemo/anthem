# Phase 3b Execution Prompts

These are self-contained prompts for Claude Code CLI, one per implementation step. Run them in order (1 through 11). Each prompt includes all the context Claude Code needs.

---

## Prompt 1: Channel Interface + Manager

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Create the channel abstraction layer in a new package `internal/channel/`.

### File 1: `internal/channel/channel.go`

Define these types:

```go
package channel

type IncomingMessage struct {
    ChannelKind string
    SenderID    string
    ThreadID    string
    Text        string
    Files       []File
    Timestamp   time.Time
    Raw         any
}

type File struct {
    Name     string
    Content  []byte
    MimeType string
}

type OutgoingMessage struct {
    Text     string
    ThreadID string
    Markdown bool
}

type Channel interface {
    Kind() string
    Start(ctx context.Context) error
    Send(ctx context.Context, msg OutgoingMessage) error
    Incoming() <-chan IncomingMessage
    Close() error
}
```

### File 2: `internal/channel/manager.go`

Implement a `Manager` struct with these methods:

- `NewManager(logger *slog.Logger) *Manager` -- constructor
- `Register(ch Channel)` -- add an adapter to the internal slice
- `Start(ctx context.Context) error` -- calls `Start(ctx)` on every registered channel, launches a goroutine per channel that reads from each channel's `Incoming()` and fans messages into a single merged `chan IncomingMessage` (buffered, size 64)
- `Broadcast(ctx context.Context, msg OutgoingMessage) error` -- calls `Send(ctx, msg)` on every registered channel, collects errors with `errors.Join`
- `Incoming() <-chan IncomingMessage` -- returns the merged incoming channel
- `Close() error` -- calls `Close()` on every registered channel, collects errors with `errors.Join`

The Manager must be safe for concurrent use. Use `sync.Mutex` to protect the channels slice. The merged incoming channel should be created in the constructor.

### File 3: `internal/channel/manager_test.go`

Write table-driven tests using a mock `Channel` implementation defined in the test file (not exported). The mock should:

- Have configurable `kind` string
- Store sent messages in a slice for assertions
- Have a controllable incoming channel
- Track whether `Start` and `Close` were called

Test cases:

1. `Register` adds channels, `Start` starts all of them
2. `Broadcast` sends to all registered channels
3. `Incoming` merges messages from multiple channels
4. `Close` closes all channels and collects errors
5. `Broadcast` with one failing channel still sends to others and returns combined error

Follow project conventions: `log/slog` for logging, `fmt.Errorf("context: %w", err)` for error wrapping, no global state, constructor injection. No unnecessary comments.
```

---

## Prompt 2: Channel Config

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Add channel and maintenance configuration to the config system. This involves two config sources: global credentials at `~/.anthem/channels.yaml` and per-project targets in WORKFLOW.md front matter.

### File 1: `internal/channel/config.go`

Create a new file in the existing `internal/channel/` package with:

```go
package channel

type ChannelsConfig struct {
    Slack *SlackCredentials `yaml:"slack,omitempty"`
}

type SlackCredentials struct {
    BotToken string `yaml:"bot_token"`
    AppToken string `yaml:"app_token"`
}
```

Add a function `LoadCredentials(path string) (*ChannelsConfig, error)` that:
- Reads the YAML file at `path`
- Unmarshals into `ChannelsConfig`
- Returns `nil, nil` if the file doesn't exist (not an error -- channels are optional)
- Returns wrapped error for any other read/parse failure

Add a function `DefaultCredentialsPath() (string, error)` that returns `~/.anthem/channels.yaml` using `os.UserHomeDir()`.

### File 2: Modify `internal/config/config.go`

Add these new config types for WORKFLOW.md front matter:

```go
type ChannelTargetConfig struct {
    Kind   string   `yaml:"kind"`
    Target string   `yaml:"target"`
    Events []string `yaml:"events"`
}

type MaintenanceConfig struct {
    ScanIntervalMS         int      `yaml:"scan_interval_ms"`
    AutoApprove            []string `yaml:"auto_approve"`
    FailureThreshold       int      `yaml:"failure_threshold"`
    StaleThresholdHours    int      `yaml:"stale_threshold_hours"`
    CostAnomalyMultiplier  float64  `yaml:"cost_anomaly_multiplier"`
}
```

Add two new fields to the `Config` struct:

```go
Channels    []ChannelTargetConfig `yaml:"channels"`
Maintenance MaintenanceConfig     `yaml:"maintenance"`
```

Update `DefaultConfig()` to include sensible maintenance defaults:
- `ScanIntervalMS: 600000` (10 minutes)
- `FailureThreshold: 3`
- `StaleThresholdHours: 24`
- `CostAnomalyMultiplier: 2.0`
- `AutoApprove: nil` (empty, user must opt in)

### File 3: `internal/channel/config_test.go`

Test:
1. `LoadCredentials` with a valid YAML file containing slack bot_token and app_token
2. `LoadCredentials` with a non-existent file returns nil, nil
3. `LoadCredentials` with invalid YAML returns an error
4. `DefaultCredentialsPath` returns a path ending in `.anthem/channels.yaml`

Use `t.TempDir()` for test fixtures. Table-driven tests. Follow project conventions.
```

---

## Prompt 3: EventBridge

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Build the EventBridge that routes internal EventBus events to external channels.

### Context

The existing EventBus is defined in `internal/orchestrator/events.go`:

```go
type EventBus interface {
    Publish(event types.Event)
    Subscribe() <-chan types.Event
}
```

The Event type is in `internal/types/task.go`:

```go
type Event struct {
    Type      string
    TaskID    string
    Timestamp time.Time
    Data      any
}
```

The Channel Manager is in `internal/channel/manager.go` with method `Broadcast(ctx, OutgoingMessage) error`.

### File 1: `internal/channel/bridge.go`

Create an `EventBridge` struct:

```go
type EventBridge struct {
    manager      *Manager
    eventCh      <-chan types.Event
    allowedTypes map[string]bool
    logger       *slog.Logger
    cancel       context.CancelFunc
}
```

Constructor: `NewEventBridge(manager *Manager, eventCh <-chan types.Event, allowedTypes []string, logger *slog.Logger) *EventBridge`
- Converts allowedTypes slice to a `map[string]bool` for O(1) lookup
- If allowedTypes is nil or empty, allow all events

Methods:

- `Start(ctx context.Context)` -- launches a goroutine that reads from `eventCh`, filters by `allowedTypes`, formats via `FormatEvent`, and calls `manager.Broadcast`. The goroutine exits when ctx is canceled or eventCh is closed. Store the cancel func so `Close` can stop it.
- `Close()` -- cancels the internal context

Add a standalone function `FormatEvent(event types.Event) string` that formats events into human-readable markdown messages:
- `task.completed` -> "Task **{TaskID}** completed."
- `task.failed` -> "Task **{TaskID}** failed: {Data}" (if Data is a string)
- `wave.completed` -> "Wave completed."
- `task.waiting_approval` -> "Task **{TaskID}** needs approval."
- `task.budget_exceeded` -> "Task **{TaskID}** exceeded budget."
- `orchestrator.stopped` -> "Orchestrator shutting down."
- `maintenance.suggested` -> "Maintenance suggested: {Data}" (if Data is a string)
- Default: "Event: {Type}" with TaskID if present

### File 2: `internal/channel/bridge_test.go`

Tests:
1. EventBridge forwards allowed events to the manager
2. EventBridge filters out events not in allowedTypes
3. Empty allowedTypes means all events pass through
4. FormatEvent produces correct markdown for each event type
5. Close stops the goroutine

Use the same mock Channel from manager_test.go (or create a similar one). Use a real Manager instance with a mock channel. Table-driven where applicable. Follow project conventions.
```

---

## Prompt 4: New Contract Actions

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Add two new contract actions to `internal/orchestrator/contract.go`: `ActionReply` and `ActionRequestMaintenance`.

### Modify `internal/orchestrator/contract.go`

1. Add two new ActionType constants:

```go
ActionReply              ActionType = "reply"
ActionRequestMaintenance ActionType = "request_maintenance"
```

2. Add them to `allActionTypes` slice.

3. Add two new fields to the `Action` struct:

```go
MaintenanceType string `json:"maintenance_type,omitempty"`
AutoApprovable  bool   `json:"auto_approvable,omitempty"`
```

The `Body` field already exists and is used by `ActionReply` for the response text. The `Reason` field already exists and is used by `ActionRequestMaintenance` for the description.

4. Update `RiskForAction`:
   - `ActionReply` -> `RiskLow`
   - `ActionRequestMaintenance` -> `RiskMedium`

5. Update `ValidateAction`:
   - `ActionReply`: requires `Body` (non-empty)
   - `ActionRequestMaintenance`: requires `MaintenanceType` (non-empty) and `Reason` (non-empty, used as description)

6. Update `IsIdempotent`:
   - `ActionReply` -> true (sending the same message twice is safe)
   - `ActionRequestMaintenance` -> false

7. Update `SchemaOnly`:
   - Remove `ActionCreateSubtasks` from SchemaOnly (it will be implemented in Prompt 6)
   - Keep `ActionPromoteKnowledge` as schema-only
   - `ActionReply` and `ActionRequestMaintenance` are NOT schema-only

### Modify `internal/orchestrator/contract_test.go`

Add test cases to existing tests (or create the file if it doesn't exist):

1. `ValidateAction` accepts valid `ActionReply` with body
2. `ValidateAction` rejects `ActionReply` without body
3. `ValidateAction` accepts valid `ActionRequestMaintenance` with maintenance_type and reason
4. `ValidateAction` rejects `ActionRequestMaintenance` without maintenance_type
5. `ValidateAction` rejects `ActionRequestMaintenance` without reason
6. `RiskForAction` returns correct levels for new actions
7. `IsIdempotent` returns correct values for new actions
8. `SchemaOnly` returns false for `ActionCreateSubtasks` now
9. `SchemaOnly` returns false for `ActionReply` and `ActionRequestMaintenance`

Table-driven tests. Follow project conventions.
```

---

## Prompt 5: Slack Adapter

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

First, add the Slack dependency:
```
go get github.com/slack-go/slack
```

Then implement the Slack channel adapter using Socket Mode.

### File 1: `internal/channel/slack/adapter.go`

Package `slack` (import path `github.com/rauriemo/anthem/internal/channel/slack`). Note: the package name `slack` will shadow the `github.com/slack-go/slack` import, so use an alias like `slackapi "github.com/slack-go/slack"` and `"github.com/slack-go/slack/socketmode"`.

Create an `Adapter` struct implementing `channel.Channel`:

```go
type Adapter struct {
    api       *slackapi.Client
    socket    *socketmode.Client
    channelID string
    incoming  chan channel.IncomingMessage
    logger    *slog.Logger
    cancel    context.CancelFunc
}
```

Constructor: `NewAdapter(botToken, appToken, channelID string, logger *slog.Logger) *Adapter`
- Creates slackapi.Client with `slackapi.OptionAppLevelToken(appToken)`
- Creates socketmode.Client from the api client
- Creates buffered incoming channel (size 64)

Methods:

- `Kind() string` -- returns `"slack"`
- `Start(ctx context.Context) error` -- creates a child context with cancel. Launches a goroutine that calls `socket.RunContext(ctx)`. Launches a second goroutine that reads from `socket.Events` channel and handles events:
  - For `socketmode.EventTypeEventsAPI`: acknowledge the event, then check if it's a `message` callback event. If so, extract text, user ID, thread timestamp, and channel. If the channel matches `channelID`, check for file attachments using `downloadFiles`. Build an `IncomingMessage` and send to `incoming` channel.
  - For `socketmode.EventTypeConnecting`, `socketmode.EventTypeConnected`, `socketmode.EventTypeConnectionError`: log at appropriate levels.
- `Send(ctx context.Context, msg channel.OutgoingMessage) error` -- posts a message using `api.PostMessageContext(ctx, channelID, slackapi.MsgOptionText(msg.Text, false))`. If `msg.ThreadID` is non-empty, add `slackapi.MsgOptionTS(msg.ThreadID)`. If `msg.Markdown` is true, the text is already markdown -- Slack handles mrkdwn natively via MsgOptionText.
- `Incoming() <-chan channel.IncomingMessage` -- returns the incoming channel
- `Close() error` -- calls cancel, returns nil

Add a private helper `func (a *Adapter) downloadFiles(ctx context.Context, files []slackapi.File) []channel.File`:
- Iterates over slack files
- For each file, calls `a.api.GetFileInfoContext(ctx, file.ID, 0, 0)` if needed, then downloads via `http.Get(file.URLPrivateDownload)` with Authorization header `"Bearer " + botToken` (store botToken in the struct)
- Reads body into `channel.File{Name: file.Name, Content: body, MimeType: file.Mimetype}`
- On error, logs and skips the file
- Limit total downloaded content to 10MB to avoid memory issues

### File 2: `internal/channel/slack/adapter_test.go`

Since Socket Mode requires a real Slack connection, write unit tests for the parts that don't need a live connection:

1. `NewAdapter` creates adapter with correct kind
2. `Kind()` returns "slack"
3. `Incoming()` returns a non-nil channel
4. `Send` error path when API fails (mock the slack client -- define a minimal interface or use a test helper)

If full unit testing of Socket Mode isn't practical, add a test with `//go:build integration` tag that tests against a real Slack workspace. The integration test should be clearly documented but not required for CI.

Follow project conventions: `log/slog`, error wrapping, no global state.
```

---

## Prompt 6: create_subtasks Implementation

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Implement the `create_subtasks` contract action that was previously schema-only. This requires adding `CreateIssue` to the `IssueTracker` interface and implementing it in both tracker backends.

### Step 1: Modify `internal/tracker/tracker.go`

Add a new method to the `IssueTracker` interface:

```go
CreateIssue(ctx context.Context, title string, body string, labels []string) (string, error)
```

Returns the created issue ID (e.g. "123" for GitHub issue #123) and error.

### Step 2: Modify `internal/tracker/github/github.go`

Add `CreateIssue` method to `GitHubTracker`:

- Uses `g.client.Issues.Create(ctx, g.owner, g.repo, &gh.IssueRequest{Title: &title, Body: &body, Labels: &labels})`
- Returns `strconv.Itoa(issue.GetNumber())` as the ID
- Wraps errors with `fmt.Errorf("creating issue: %w", err)`

### Step 3: Modify `internal/tracker/local/local.go`

Add `CreateIssue` method to `LocalJSONTracker`:

- Loads the existing tasks from the JSON file
- Generates a new ID (e.g. `fmt.Sprintf("%d", len(tasks)+1)` or use a timestamp-based ID)
- Creates a new `types.Task` with the given title, body, labels, status `types.StatusQueued`, and `CreatedAt: time.Now()`
- Appends to the slice, saves back to the JSON file
- Returns the new ID

### Step 4: Modify `internal/orchestrator/contract.go`

Update `SchemaOnly` to remove `ActionCreateSubtasks` -- it is no longer schema-only:

```go
func SchemaOnly(actionType ActionType) bool {
    switch actionType {
    case ActionPromoteKnowledge:
        return true
    default:
        return false
    }
}
```

### Step 5: Modify `internal/orchestrator/orchestrator.go`

In `executeActions`, add a new case for `ActionCreateSubtasks` (it currently falls through to `SchemaOnly` check which logs and skips):

```go
case ActionCreateSubtasks:
    for _, sub := range action.Subtasks {
        createdID, err := o.tracker.CreateIssue(ctx, sub.Title, sub.Body, sub.Labels)
        if err != nil {
            o.logger.Warn("failed to create subtask", "title", sub.Title, "error", err)
            continue
        }
        o.logger.Info("created subtask", "id", createdID, "title", sub.Title)
    }
    o.recordAudit(ctx, "subtasks.created", "", strPtr("create_subtasks"))
```

This case must go BEFORE the `SchemaOnly` check in the function. Move the `SchemaOnly` check so it happens after all implemented action cases.

### Tests

Add or update tests:

1. `internal/tracker/github/github_test.go`: Test `CreateIssue` with an httptest server that accepts POST to `/repos/{owner}/{repo}/issues` and returns a created issue JSON
2. `internal/tracker/local/local_test.go`: Test `CreateIssue` creates a task in the JSON file with correct fields
3. `internal/orchestrator/contract_test.go`: Verify `SchemaOnly` no longer returns true for `ActionCreateSubtasks`
4. `internal/orchestrator/orchestrator_test.go` (or integration test): Test that `executeActions` with `ActionCreateSubtasks` calls `CreateIssue` on the tracker

Table-driven tests. Follow project conventions.
```

---

## Prompt 7: HandleUserMessage

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Add a `HandleUserMessage` method to the Orchestrator that processes inbound channel messages.

### Context

The Orchestrator struct is in `internal/orchestrator/orchestrator.go`. It already has:
- `orchAgent *OrchestratorAgent` for consulting the AI
- `tracker` for issue operations
- `events` EventBus for publishing events
- `auditLogger` for recording events
- `executeActions(ctx, tasks, actions)` for executing proposed actions

The Channel Manager is in `internal/channel/manager.go` with `Broadcast(ctx, OutgoingMessage)` for sending replies.

The `StateSnapshot` is in `internal/orchestrator/orchagent.go`.

### Step 1: Extend `StateSnapshot` in `orchagent.go`

Add a new field to `StateSnapshot`:

```go
UserMessage *UserMessageContext `json:"user_message,omitempty"`
```

Add the new type:

```go
type UserMessageContext struct {
    Text  string   `json:"text"`
    Files []string `json:"files,omitempty"` // file contents as strings (text files) or "[image: filename.png]" placeholders
}
```

### Step 2: Add `HandleUserMessage` to Orchestrator

In `orchestrator.go`, add a new field to the `Orchestrator` struct:

```go
channelMgr *channel.Manager
```

Add it to `Opts`:

```go
ChannelManager *channel.Manager
```

Wire it in `New()`:
```go
channelMgr: opts.ChannelManager,
```

Add the method:

```go
func (o *Orchestrator) HandleUserMessage(ctx context.Context, msg channel.IncomingMessage) {
```

Implementation:
1. Log the incoming message at Info level (sender, text length, file count)
2. Fetch current tasks from tracker via `o.tracker.ListActive(ctx)`
3. Build a `StateSnapshot` via `o.buildStateSnapshot(tasks)`
4. Populate `snap.UserMessage` with the message text and file contents:
   - For text files (mime type starts with "text/" or is "application/json" or "application/yaml"): include content as string, truncated to 50KB
   - For images: include `"[image: filename.ext]"` as placeholder (Claude multimodal will handle via the session)
   - For other binary files: include `"[file: filename.ext, type: mime/type]"` as placeholder
5. Consult orchestrator agent via `o.orchAgent.ConsultWithRepair(ctx, snap)`
6. If consultation fails, send an error reply through `o.channelMgr.Broadcast`
7. If successful, call `o.executeActions(ctx, tasks, actions)` -- this handles dispatch, skip, comment, create_subtasks, etc.
8. For `ActionReply` actions, send the reply body through `o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{Text: action.Body, ThreadID: msg.ThreadID, Markdown: true})`
9. Record an audit event for the user message: `o.recordAudit(ctx, "channel.user_message", "", strPtr("handle_user_message"))`

Important: the `ActionReply` execution should be added to `executeActions` as a new case:

```go
case ActionReply:
    if o.channelMgr != nil {
        replyMsg := channel.OutgoingMessage{Text: action.Body, Markdown: true}
        if err := o.channelMgr.Broadcast(ctx, replyMsg); err != nil {
            o.logger.Warn("failed to send channel reply", "error", err)
        }
    }
    o.recordAudit(ctx, "channel.reply_sent", "", strPtr("reply"))
```

### Step 3: Add message listener loop

Add a method `StartChannelListener(ctx context.Context)` that runs as a goroutine:
- Reads from `o.channelMgr.Incoming()`
- Calls `o.HandleUserMessage(ctx, msg)` for each message
- Exits when ctx is canceled

### Tests

1. Test `HandleUserMessage` with a mock tracker, mock runner, and mock channel manager -- verify the orchestrator agent is consulted and replies are broadcast
2. Test that text file contents are included in the snapshot
3. Test that image files get placeholder strings
4. Test error path when orchestrator consultation fails -- error reply is sent

Use mock implementations. Table-driven where applicable. Follow project conventions. Import `channel` package as `"github.com/rauriemo/anthem/internal/channel"`.
```

---

## Prompt 8: Audit-Log Maintenance Scanner

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Build the maintenance scanner that periodically queries the audit log for health signals.

### Context

The AuditLogger interface is in `internal/audit/audit.go`:

```go
type AuditLogger interface {
    Record(ctx context.Context, event AuditEvent) error
    Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error)
    RecentByTask(ctx context.Context, taskID string, limit int) ([]AuditEvent, error)
    SummaryForWave(ctx context.Context, waveID string) (*WaveSummary, error)
    Close() error
}

type QueryFilter struct {
    EventType string
    TaskID    string
    WaveID    string
    Since     time.Time
    Limit     int
}
```

The EventBus is in `internal/orchestrator/events.go` with `Publish(event types.Event)`.

The Event type is `types.Event{Type, TaskID, Timestamp, Data}`.

Maintenance config from `internal/config/config.go`:

```go
type MaintenanceConfig struct {
    ScanIntervalMS         int      `yaml:"scan_interval_ms"`
    AutoApprove            []string `yaml:"auto_approve"`
    FailureThreshold       int      `yaml:"failure_threshold"`
    StaleThresholdHours    int      `yaml:"stale_threshold_hours"`
    CostAnomalyMultiplier  float64  `yaml:"cost_anomaly_multiplier"`
}
```

### File 1: `internal/maintenance/scanner.go`

Package `maintenance`.

Define a `Signal` type:

```go
type SignalKind string

const (
    SignalRepeatedFailure SignalKind = "repeated_failure"
    SignalStaleTask       SignalKind = "stale_task"
    SignalBudgetAnomaly   SignalKind = "budget_anomaly"
    SignalDrift           SignalKind = "drift"
)

type Signal struct {
    Kind        SignalKind
    TaskID      string
    Description string
    AutoApprove bool
}
```

Create a `Scanner` struct:

```go
type Scanner struct {
    audit       audit.AuditLogger
    events      EventPublisher
    config      config.MaintenanceConfig
    logger      *slog.Logger
    cancel      context.CancelFunc
}

type EventPublisher interface {
    Publish(event types.Event)
}
```

Constructor: `NewScanner(audit audit.AuditLogger, events EventPublisher, cfg config.MaintenanceConfig, logger *slog.Logger) *Scanner`

Methods:

- `Start(ctx context.Context)` -- creates child context with cancel. Launches a goroutine with a ticker at `config.ScanIntervalMS` interval. Each tick calls `o.scan(ctx)`. Exits on ctx cancel.
- `Close()` -- calls cancel
- `scan(ctx context.Context) []Signal` -- runs all four checks, publishes signals as events, returns found signals
- `checkRepeatedFailures(ctx context.Context) []Signal` -- queries audit log for `task.failed` events in the last 24 hours. Groups by TaskID. Any task with >= `config.FailureThreshold` failures emits a signal. Description: "Task {ID} has failed {N} times in the last 24 hours."
- `checkStaleTasks(ctx context.Context) []Signal` -- queries for `task.dispatched` events where no corresponding `task.completed` or `task.failed` event exists within `config.StaleThresholdHours`. Description: "Task {ID} has been in {status} for over {N} hours."
- `checkBudgetAnomalies(ctx context.Context) []Signal` -- queries all cost events, computes average cost. Any task exceeding `config.CostAnomalyMultiplier * average` emits a signal. Description: "Task {ID} cost ${cost} exceeds {multiplier}x the average (${avg})."
- `checkDrift(ctx context.Context) []Signal` -- queries for tasks that have both a `task.completed` event AND a subsequent `task.dispatched` event (re-opened). Description: "Task {ID} was completed but has been re-dispatched."

For each signal, check if `signal.Kind` string representation is in `config.AutoApprove` to set `signal.AutoApprove`.

Published events use `types.Event{Type: "maintenance.suggested", TaskID: signal.TaskID, Data: signal}`.

### File 2: `internal/maintenance/scanner_test.go`

Create a mock AuditLogger and mock EventPublisher for testing.

Tests:
1. Scanner detects repeated failures when a task has failed >= threshold times
2. Scanner ignores tasks with failures below threshold
3. Scanner detects stale tasks beyond the configured hours
4. Scanner detects budget anomalies exceeding the multiplier
5. Scanner detects drift (completed then re-dispatched)
6. AutoApprove is set correctly based on config
7. Scanner publishes `maintenance.suggested` events for each signal
8. Start/Close lifecycle works without panics

Table-driven tests. Use `t.TempDir()` if needed. Follow project conventions.
```

---

## Prompt 9: Orchestrator Agent Prompt Extension

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Extend the orchestrator agent's system prompt in `internal/orchestrator/orchagent.go` to handle channel messages, multi-format input, reply actions, and maintenance decisions.

### Modify `buildSystemPrompt` in `orchagent.go`

The current system prompt has sections: Role, Actions, Wave Model, Response Format.

Update the **Actions** section to include the two new action types:

```
- reply: Send a message back to the user through the communication channel. Required: body.
- request_maintenance: Propose a maintenance action (gc, lint, test, drift check). Required: maintenance_type, reason. Optional: auto_approvable (bool).
```

Remove the "(schema-only)" annotation from `create_subtasks` since it is now implemented.

Add a new section **## Channel Messages** after the Actions section:

```
## Channel Messages

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
   - Wait for explicit approval before dispatching maintenance tasks
```

Add a new section **## Multi-Format Input** after Channel Messages:

```
## Multi-Format Input

Users may describe features in multiple formats. Handle all of these:
- Plain text: direct feature description or command
- Markdown files: structured specs with sections, acceptance criteria, etc.
- Mermaid diagrams: flowcharts describing user flows or system architecture
- ASCII diagrams: text-based diagrams of flows or architecture
- Images: screenshots, whiteboard photos, flowchart images (described as [image: filename])
- Mixed: message text combined with one or more attached files

Always decompose complex features into small, independently executable tasks. Each task should be completable by a single executor agent session.
```

### Tests

Update or create tests in `internal/orchestrator/orchagent_test.go`:

1. Verify `buildSystemPrompt` output contains "reply:" action description
2. Verify `buildSystemPrompt` output contains "request_maintenance:" action description
3. Verify `buildSystemPrompt` output contains "Channel Messages" section
4. Verify `buildSystemPrompt` output contains "Multi-Format Input" section
5. Verify `create_subtasks` is no longer marked as "(schema-only)" in the prompt

Follow project conventions.
```

---

## Prompt 10: main.go Wiring

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Wire all Phase 3b components into `cmd/anthem/main.go`.

### Context

The existing `runCmd` function in `cmd/anthem/main.go` already:
- Bootstraps `~/.anthem/`
- Loads config from WORKFLOW.md
- Creates tracker, runner, workspace manager, event bus, state path, audit logger, orchestrator agent
- Creates `orchestrator.New(orchestrator.Opts{...})`
- Starts config watcher
- Runs `orch.Run(ctx)`

### Changes to `runCmd` in `cmd/anthem/main.go`

After audit logger creation and before orchestrator creation, add:

1. **Load channel credentials**:
```go
credPath, err := channel.DefaultCredentialsPath()
if err != nil {
    return fmt.Errorf("resolving channel credentials path: %w", err)
}
channelCreds, err := channel.LoadCredentials(credPath)
if err != nil {
    return fmt.Errorf("loading channel credentials: %w", err)
}
```

2. **Create Channel Manager**:
```go
chanManager := channel.NewManager(logger)
```

3. **Register Slack adapter if configured**:
```go
if channelCreds != nil && channelCreds.Slack != nil && len(cfg.Channels) > 0 {
    for _, chCfg := range cfg.Channels {
        if chCfg.Kind == "slack" {
            slackAdapter := slackch.NewAdapter(
                channelCreds.Slack.BotToken,
                channelCreds.Slack.AppToken,
                chCfg.Target,
                logger,
            )
            chanManager.Register(slackAdapter)
            logger.Info("registered slack channel", "target", chCfg.Target)
        }
    }
}
```

Import the slack package as `slackch "github.com/rauriemo/anthem/internal/channel/slack"`.

4. **Start Channel Manager**:
```go
if err := chanManager.Start(ctx); err != nil {
    logger.Warn("failed to start channel manager, continuing without channels", "error", err)
}
defer chanManager.Close()
```

5. **Create EventBridge**:
```go
var bridgeAllowedEvents []string
if len(cfg.Channels) > 0 {
    bridgeAllowedEvents = cfg.Channels[0].Events // use first channel config's event filter
}
eventBridge := channel.NewEventBridge(chanManager, events.Subscribe(), bridgeAllowedEvents, logger)
eventBridge.Start(ctx)
defer eventBridge.Close()
```

6. **Create Maintenance Scanner**:
```go
scanner := maintenance.NewScanner(auditLogger, events, cfg.Maintenance, logger)
scanner.Start(ctx)
defer scanner.Close()
```

Import maintenance as `"github.com/rauriemo/anthem/internal/maintenance"`.

7. **Pass Channel Manager to Orchestrator Opts**:
Add `ChannelManager: chanManager` to the `orchestrator.Opts{...}`.

8. **Start Channel Listener goroutine** after orchestrator creation but before `orch.Run(ctx)`:
```go
go orch.StartChannelListener(ctx)
```

9. **Update `bootstrapAnthemDir`** (or `bootstrapDir`): No changes needed -- `channels.yaml` is optional and not auto-created.

### Import additions

Add these imports:
```go
"github.com/rauriemo/anthem/internal/channel"
slackch "github.com/rauriemo/anthem/internal/channel/slack"
"github.com/rauriemo/anthem/internal/maintenance"
```

### Verify

After making changes, run `go build ./cmd/anthem` to verify compilation. Run `go vet ./...` and `go test ./...` to check for issues.

Follow project conventions: no unnecessary comments, constructor injection, error wrapping.
```

---

## Prompt 11: Documentation

```
Read CLAUDE.md thoroughly, then docs/plans/architecture.md and docs/plans/implementation.md.

Update all documentation files to reflect Phase 3b completion.

### File 1: `CLAUDE.md`

In the "Current Status" section:
- Change Phase 3b from "(next)" to "(**complete**)"
- Add a detailed summary under "Phase 3b completed" following the same format as Phase 3a's summary. Include:
  - Channel system: Channel interface, Manager, EventBridge
  - Slack adapter with Socket Mode
  - Multi-format task decomposition via create_subtasks implementation
  - Audit-log maintenance scanner with configurable thresholds and auto-approve
  - New contract actions: ActionReply, ActionRequestMaintenance
  - HandleUserMessage on Orchestrator for inbound channel processing
  - Channel config: global credentials (~/.anthem/channels.yaml) + per-project targets in WORKFLOW.md
  - New packages: internal/channel/, internal/channel/slack/, internal/maintenance/
  - New dependency: github.com/slack-go/slack

- Update "Phase 4 (next)" to show it's the next phase

### File 2: `docs/plans/architecture.md`

In the implementation history section:
- Add Phase 3b as completed with the same level of detail as Phase 3a
- List all 11 implementation steps and what was done in each

### File 3: `docs/plans/implementation.md`

In the implementation steps section:
- Mark Phase 3b steps as completed
- Add verification notes for each step (files created, tests passing)

### File 4: `README.md`

- Update the upcoming phases section to show Phase 3b as complete
- Add brief descriptions of the channel system, maintenance scanner, and multi-format decomposition to the features list
- If there's a configuration section, add examples for `channels.yaml` and WORKFLOW.md `channels:` / `maintenance:` blocks

Follow project conventions. No unnecessary comments in documentation -- keep it factual and reference-oriented.
```

---

## Execution Order

Run prompts 1-11 in sequence. Each prompt builds on the previous ones:

| Prompt | Depends On | Creates/Modifies |
|--------|-----------|-----------------|
| 1 | Nothing | `internal/channel/channel.go`, `manager.go`, `manager_test.go` |
| 2 | Prompt 1 types | `internal/channel/config.go`, `config_test.go`, `internal/config/config.go` |
| 3 | Prompt 1 Manager | `internal/channel/bridge.go`, `bridge_test.go` |
| 4 | Existing contract.go | `internal/orchestrator/contract.go`, `contract_test.go` |
| 5 | Prompt 1 Channel interface | `internal/channel/slack/adapter.go`, `adapter_test.go` |
| 6 | Prompt 4, existing tracker | `tracker.go`, `github.go`, `local.go`, `contract.go`, `orchestrator.go` |
| 7 | Prompts 1,4,6 | `orchagent.go`, `orchestrator.go` |
| 8 | Existing audit, events | `internal/maintenance/scanner.go`, `scanner_test.go` |
| 9 | Prompts 4,7 | `internal/orchestrator/orchagent.go`, `orchagent_test.go` |
| 10 | All above | `cmd/anthem/main.go` |
| 11 | All above | `CLAUDE.md`, `architecture.md`, `implementation.md`, `README.md` |

After each prompt, run `go build ./cmd/anthem && go vet ./... && go test ./...` to verify.
