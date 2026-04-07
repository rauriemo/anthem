package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/plans"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func newPlanTestOrch(t *testing.T, orchRunner *agent.MockRunner) (*Orchestrator, *testChannel) {
	t.Helper()
	tasks := []types.Task{
		{ID: "1", Title: "Existing Task", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 10, 25, 10, 5, testLogger())

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "owner/repo"
	cfg.Tracker.Labels.Active = []string{"todo"}

	home := t.TempDir()
	store, err := plans.NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        trk,
		Runner:         agent.NewMockRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})
	orch.planStore = store
	return orch, ch
}

func TestHandlePlanMessage_RoutesCorrectly(t *testing.T) {
	planOutput := "Here's a plan:\n```anthem-plan\n# My Plan\n\n## Tasks\n\n### 1. Do X\n- **Labels:** area:backend\n```\n"

	orchRunner := agent.NewMockRunner()
	callCount := 0
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		callCount++
		if strings.Contains(opts.Prompt, "Scout Mode") {
			// Scout phase: return empty explores to trigger fallback
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple request", "explores": [], "user_message": ""}`,
				TokensIn:  50, TokensOut: 20,
			}, nil
		}
		if !strings.Contains(opts.Prompt, "PLANNING mode") {
			t.Error("expected plan mode prompt suffix on fallback")
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    planOutput,
			TokensIn:  100, TokensOut: 50,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Build a login page",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()

	// Should have: ack, stream-done, display(plan)
	var planDisplay bool
	for _, msg := range sent {
		if msg.Display != nil {
			if dm, ok := msg.Display.(map[string]any); ok {
				if kind, ok := dm["kind"].(string); ok && kind == "plan" {
					planDisplay = true
					content, _ := dm["content"].(string)
					if !strings.Contains(content, "# My Plan") {
						t.Errorf("plan content missing expected heading, got %q", content)
					}
				}
			}
		}
	}
	if !planDisplay {
		t.Error("expected a plan display artifact to be sent")
	}
}

func TestHandlePlanMessage_SavesPlanFile(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Feature X\n\n## Tasks\n\n### 1. Task A\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	orch, _ := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Create feature X",
		Timestamp:   time.Now(),
	})

	metas, err := orch.planStore.List("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 plan saved, got %d", len(metas))
	}
	if metas[0].Status != plans.StatusDraft {
		t.Errorf("plan status = %q, want draft", metas[0].Status)
	}
}

func TestHandleBuildMessage_CreatesSubtasks(t *testing.T) {
	orchRunner := agent.NewMockRunner()

	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Build Mode") {
			return &types.RunResult{
				SessionID: "build-s1",
				Output: `{"reasoning": "Building from approved plan", "actions": [
					{"type": "create_subtasks", "subtasks": [{"title": "Task A", "body": "Do A", "labels": ["priority:high"]}]},
					{"type": "reply", "body": "Created 1 task from your plan."}
				]}`,
				TokensIn: 100, TokensOut: 80,
			}, nil
		}
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Build Test\n\n## Tasks\n\n### 1. Task A\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	// First create a plan
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Build something",
		Timestamp:   time.Now(),
	})

	// Get the saved plan path
	metas, _ := orch.planStore.List("owner/repo")
	if len(metas) == 0 {
		t.Fatal("expected plan to exist")
	}

	// Now build it
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t2",
		Text:        "[system:build] " + metas[0].Path,
		Timestamp:   time.Now(),
	})

	// Check plan status changed to done
	plan, err := orch.planStore.Load(metas[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Frontmatter.Status != plans.StatusDone {
		t.Errorf("plan status = %q, want done", plan.Frontmatter.Status)
	}

	// Verify a reply was sent
	sent := ch.sentMessages()
	var foundReply bool
	for _, msg := range sent {
		if msg.Text != "" && strings.Contains(msg.Text, "Created 1 task") {
			foundReply = true
		}
	}
	if !foundReply {
		t.Error("expected build confirmation reply")
	}

	// Verify the created task has "todo" auto-added (LLM only provided "priority:high")
	trk, ok := orch.tracker.(*tracker.MockTracker)
	if !ok {
		t.Fatal("expected MockTracker")
	}
	createdTask, _ := trk.GetTask(context.Background(), "2")
	if createdTask == nil {
		t.Fatal("expected task with ID '2' to be created")
	}
	if createdTask.Title != "Task A" {
		t.Errorf("created task title = %q, want 'Task A'", createdTask.Title)
	}
	hasTodo := false
	hasPriority := false
	for _, l := range createdTask.Labels {
		if l == "todo" {
			hasTodo = true
		}
		if l == "priority:high" {
			hasPriority = true
		}
	}
	if !hasTodo {
		t.Errorf("built task labels = %v, want 'todo' to be auto-added", createdTask.Labels)
	}
	if !hasPriority {
		t.Errorf("built task labels = %v, want 'priority:high' to be preserved", createdTask.Labels)
	}
}

func TestHandleBuildMessage_NoTracker_FallsBackToDraft(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Build Mode") {
			return &types.RunResult{
				SessionID: "build-s1",
				Output:    `{"reasoning": "building", "actions": [{"type": "reply", "body": "Done."}]}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Fallback Test\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	orch, _ := newPlanTestOrch(t, orchRunner)

	// Create a plan first
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] test",
		Timestamp:   time.Now(),
	})

	// Build without specifying path -- should find latest draft
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t2",
		Text:        "[system:build]",
		Timestamp:   time.Now(),
	})

	metas, _ := orch.planStore.List("owner/repo")
	if len(metas) == 0 {
		t.Fatal("expected plan to exist")
	}
	plan, _ := orch.planStore.Load(metas[0].Path)
	if plan.Frontmatter.Status != plans.StatusDone {
		t.Errorf("plan status = %q, want done", plan.Frontmatter.Status)
	}
}

func TestExtractPlanBlock(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard block",
			input: "Here's a plan:\n```anthem-plan\n# My Plan\n\n## Tasks\n```\nDone.",
			want:  "# My Plan\n\n## Tasks",
		},
		{
			name:  "no block returns empty",
			input: "Just some text without a plan block.",
			want:  "",
		},
		{
			name:  "block at end without closing",
			input: "```anthem-plan\n# Unterminated Plan",
			want:  "# Unterminated Plan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanBlock(tt.input)
			if got != tt.want {
				t.Errorf("extractPlanBlock() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizePlanOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "anthem-plan block extracted",
			raw:  "Some preamble\n```anthem-plan\n# My Plan\n\n## Tasks\n```\nTrailing",
			want: "# My Plan\n\n## Tasks",
		},
		{
			name: "pure JSON action response stripped",
			raw:  `{"reasoning": "I analyzed the codebase.", "actions": [{"type": "create_subtasks"}]}`,
			want: "I analyzed the codebase.",
		},
		{
			name: "markdown with embedded JSON block stripped",
			raw:  "# Plan Title\n\nSome analysis.\n\n{\"reasoning\": \"test\", \"actions\": [{\"type\": \"display\"}]}\n\n## Next Steps\n\nDo stuff.",
			want: "# Plan Title\n\nSome analysis.\n\n\n## Next Steps\n\nDo stuff.",
		},
		{
			name: "HTML block stripped",
			raw:  "# Plan\n\n<!DOCTYPE html>\n<html><body>Rich content</body></html>\n\n## Tasks\n\n- Item 1",
			want: "# Plan\n\n\n## Tasks\n\n- Item 1",
		},
		{
			name: "clean markdown passes through",
			raw:  "# Test Coverage Plan\n\n## Overview\n\nAnalysis here.\n\n## Tasks\n\n### 1. Add tests",
			want: "# Test Coverage Plan\n\n## Overview\n\nAnalysis here.\n\n## Tasks\n\n### 1. Add tests",
		},
		{
			name: "empty string returns empty",
			raw:  "",
			want: "",
		},
		{
			name: "pure JSON with no reasoning returns empty",
			raw:  `{"actions": [{"type": "close_wave"}]}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizePlanOutput(tt.raw)
			if got != tt.want {
				t.Errorf("sanitizePlanOutput() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestPlanStreamFilter(t *testing.T) {
	t.Run("forwards markdown", func(t *testing.T) {
		var out strings.Builder
		f := newPlanStreamFilter(func(s string) { out.WriteString(s) })
		f.Write("# Plan\n")
		f.Write("Some content\n")
		if out.String() != "# Plan\nSome content\n" {
			t.Errorf("expected markdown to pass through, got %q", out.String())
		}
	})

	t.Run("suppresses JSON action block", func(t *testing.T) {
		var out strings.Builder
		f := newPlanStreamFilter(func(s string) { out.WriteString(s) })
		f.Write("{\"reasoning\": \"analyzing\", \"actions\": []}")
		if out.String() != "" {
			t.Errorf("expected JSON to be suppressed, got %q", out.String())
		}
	})

	t.Run("suppresses HTML block", func(t *testing.T) {
		var out strings.Builder
		f := newPlanStreamFilter(func(s string) { out.WriteString(s) })
		f.Write("<!DOCTYPE html>\n<html><body>Hi</body></html>")
		if out.String() != "" {
			t.Errorf("expected HTML to be suppressed, got %q", out.String())
		}
	})
}

func TestExtractPlanTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"# Chat Mode Selector\n\n## Tasks\n", "Chat Mode Selector"},
		{"No heading here", ""},
		{"## Not H1\n# Actual Title\n", "Actual Title"},
	}

	for _, tt := range tests {
		got := extractPlanTitle(tt.input)
		if got != tt.want {
			t.Errorf("extractPlanTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractPlanOverview(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "paragraph after title",
			input: "# My Plan\n\nThis is the overview paragraph.\n\n## Tasks\n",
			want:  "This is the overview paragraph.",
		},
		{
			name:  "multi-line paragraph",
			input: "# My Plan\n\nFirst line\nsecond line\n\n## Tasks\n",
			want:  "First line second line",
		},
		{
			name:  "no paragraph",
			input: "# My Plan\n\n## Tasks\n",
			want:  "",
		},
		{
			name:  "no heading",
			input: "Just some text\n",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanOverview(tt.input)
			if got != tt.want {
				t.Errorf("extractPlanOverview() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractPlanTasks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "numbered headings with dots",
			input: "## Tasks\n\n### 1. Setup project\n\n### 2. Add tests\n\n### 3. Deploy\n",
			want:  []string{"Setup project", "Add tests", "Deploy"},
		},
		{
			name:  "numbered headings with parens",
			input: "### 1) First thing\n### 2) Second thing\n",
			want:  []string{"First thing", "Second thing"},
		},
		{
			name:  "no task headings",
			input: "# Plan\n\n## Overview\n\nSome text.\n",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanTasks(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("extractPlanTasks() returned %d tasks, want %d: %v", len(got), len(tt.want), got)
			}
			for i, task := range got {
				if task != tt.want[i] {
					t.Errorf("task[%d] = %q, want %q", i, task, tt.want[i])
				}
			}
		})
	}
}

func TestBuildPlanCard(t *testing.T) {
	content := "# My Plan\n\nThis is the overview.\n\n## Tasks\n\n### 1. Do X\n### 2. Do Y\n"
	card := buildPlanCard(content, "/plans/test.md")

	if !strings.HasPrefix(card, "[plan-card]") {
		t.Errorf("expected [plan-card] prefix, got %q", card[:30])
	}
	if !strings.HasSuffix(card, "[/plan-card]") {
		t.Errorf("expected [/plan-card] suffix, got %q", card[len(card)-30:])
	}
	if !strings.Contains(card, `"title":"My Plan"`) {
		t.Error("card missing title")
	}
	if !strings.Contains(card, `"overview":"This is the overview."`) {
		t.Error("card missing overview")
	}
	if !strings.Contains(card, `"Do X"`) || !strings.Contains(card, `"Do Y"`) {
		t.Error("card missing tasks")
	}
	if !strings.Contains(card, `"planPath":"/plans/test.md"`) {
		t.Error("card missing planPath")
	}
}

func TestExtractHTMLPlanTitle(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "simple h1",
			html: `<html><body><h1>Test Coverage Analysis</h1></body></html>`,
			want: "Test Coverage Analysis",
		},
		{
			name: "h1 with inner span",
			html: `<h1><span class='icon'>X</span> Coverage Report</h1>`,
			want: "X Coverage Report",
		},
		{
			name: "h1 with class attribute",
			html: `<h1 class="title">My Plan Title</h1>`,
			want: "My Plan Title",
		},
		{
			name: "no h1 returns empty",
			html: `<html><body><h2>Not a title</h2></body></html>`,
			want: "",
		},
		{
			name: "empty string returns empty",
			html: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHTMLPlanTitle(tt.html)
			if got != tt.want {
				t.Errorf("extractHTMLPlanTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractHTMLPlanTasks(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "plan-title divs",
			html: `<div class='plan-item'><div class='plan-title'>Backend: main.py tests</div></div>` +
				`<div class='plan-item'><div class='plan-title'>Frontend: WebSocket tests</div></div>`,
			want: []string{"Backend: main.py tests", "Frontend: WebSocket tests"},
		},
		{
			name: "strong tags in plan-item",
			html: `<div class='plan-item'><span class='plan-num'>1</span><div class='plan-content'><strong>Add unit tests</strong></div></div>` +
				`<div class='plan-item'><span class='plan-num'>2</span><div class='plan-content'><strong>Fix linter</strong></div></div>`,
			want: []string{"Add unit tests", "Fix linter"},
		},
		{
			name: "no plan items returns nil",
			html: `<html><body><p>Just a paragraph</p></body></html>`,
			want: nil,
		},
		{
			name: "nested HTML in plan-title stripped",
			html: `<div class='plan-title'><strong>Task</strong> with <em>emphasis</em></div>`,
			want: []string{"Task with emphasis"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHTMLPlanTasks(tt.html)
			if len(got) != len(tt.want) {
				t.Fatalf("extractHTMLPlanTasks() returned %d tasks, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("task[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestBuildPlanCardFromHTML(t *testing.T) {
	html := `<!DOCTYPE html><html><body>` +
		`<h1>Prism Test Coverage</h1>` +
		`<p class='subtitle'>Full audit of test coverage</p>` +
		`<div class='plan-item'><div class='plan-title'>Backend: main.py tests</div></div>` +
		`<div class='plan-item'><div class='plan-title'>Frontend: hook tests</div></div>` +
		`</body></html>`

	card := buildPlanCardFromHTML(html)

	if !strings.HasPrefix(card, "[plan-card]") || !strings.HasSuffix(card, "[/plan-card]") {
		t.Fatalf("expected [plan-card]...[/plan-card], got %q", card)
	}
	if !strings.Contains(card, `"title":"Prism Test Coverage"`) {
		t.Error("card missing title")
	}
	if !strings.Contains(card, `"overview":"Full audit of test coverage"`) {
		t.Error("card missing overview")
	}
	if !strings.Contains(card, `"Backend: main.py tests"`) {
		t.Error("card missing first task")
	}
	if !strings.Contains(card, `"Frontend: hook tests"`) {
		t.Error("card missing second task")
	}
	if !strings.Contains(card, `"planPath":""`) {
		t.Error("card should have empty planPath for HTML")
	}
}

func TestIsSubstantialOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "short conversational reply",
			out:  "The coverage looks good. I'd suggest focusing on WebSocket tests next.",
			want: false,
		},
		{
			name: "long HTML with plan structure",
			out:  "<html><body>" + strings.Repeat("x", 500) + "<div class='plan-item'>task</div></body></html>",
			want: true,
		},
		{
			name: "long markdown with tasks section",
			out:  strings.Repeat("x", 500) + "\n## Tasks\n### 1. Do thing",
			want: true,
		},
		{
			name: "long but generic text",
			out:  strings.Repeat("The project has good coverage. ", 30),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSubstantialOutput(tt.out)
			if got != tt.want {
				t.Errorf("isSubstantialOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFinalizePlan_SendsPlanCard(t *testing.T) {
	planOutput := "```anthem-plan\n# Coverage Plan\n\nOverview text here.\n\n## Tasks\n\n### 1. Add unit tests\n### 2. Fix linter\n```\n"

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    planOutput,
			TokensIn:  100, TokensOut: 50,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Create coverage plan",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var foundCard bool
	for _, msg := range sent {
		if strings.Contains(msg.Text, "[plan-card]") && strings.Contains(msg.Text, "[/plan-card]") {
			foundCard = true
			if !strings.Contains(msg.Text, `"title":"Coverage Plan"`) {
				t.Errorf("plan card missing title, got %q", msg.Text)
			}
			if !strings.Contains(msg.Text, `"Add unit tests"`) {
				t.Errorf("plan card missing tasks, got %q", msg.Text)
			}
		}
	}
	if !foundCard {
		t.Error("expected a [plan-card] message to be sent after plan finalization")
	}
}

func TestHandlePlanOutput_ConversationalReply(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple question", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		// Return conversational text, no anthem-plan block
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "The current test coverage looks good. You have 15 test files covering the main API endpoints. I'd suggest focusing on the WebSocket handler next.",
			TokensIn:  80, TokensOut: 40,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] How does the test coverage look?",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var foundReply bool
	var foundCard bool
	for _, msg := range sent {
		if strings.Contains(msg.Text, "test coverage looks good") {
			foundReply = true
		}
		if strings.Contains(msg.Text, "[plan-card]") {
			foundCard = true
		}
	}
	if !foundReply {
		t.Error("expected conversational text reply for non-plan output")
	}
	if foundCard {
		t.Error("did not expect a plan card for conversational reply")
	}
}

func TestHandlePlanOutput_HTMLArtifact(t *testing.T) {
	htmlOutput := `<!DOCTYPE html><html><body>` +
		`<h1>Test Coverage Analysis</h1>` +
		`<p class='subtitle'>Full audit of backend and frontend</p>` +
		strings.Repeat(`<div class='plan-item'><div class='plan-title'>Task item</div></div>`, 20) +
		`</body></html>`

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "need research", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    htmlOutput,
			TokensIn:  200, TokensOut: 100,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Create a test coverage analysis",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var foundCard bool
	for _, msg := range sent {
		if strings.Contains(msg.Text, "[plan-card]") && strings.Contains(msg.Text, "[/plan-card]") {
			foundCard = true
			if !strings.Contains(msg.Text, `"title":"Test Coverage Analysis"`) {
				t.Errorf("plan card missing title, got %q", msg.Text)
			}
			if !strings.Contains(msg.Text, `"planPath":""`) {
				t.Errorf("HTML plan card should have empty planPath, got %q", msg.Text)
			}
		}
	}
	if !foundCard {
		t.Error("expected a [plan-card] message for HTML artifact output")
	}

	// Verify display was broadcast as html kind
	displays := ch.sentDisplays()
	var foundHTML bool
	for _, d := range displays {
		if kind, ok := d["kind"].(string); ok && kind == "html" {
			foundHTML = true
		}
	}
	if !foundHTML {
		t.Error("expected display broadcast with kind=html for HTML plan output")
	}
}

func TestPlanModePromptContents(t *testing.T) {
	checks := []string{
		"PLANNING mode",
		"READ-ONLY",
		"Conversational reply",
		"Rich visual plan",
		"Structured markdown plan",
		"html kind",
		"Stage 1: Research",
		"Stage 2: Synthesis",
		"MUST explore",
		"at least 5 tool calls",
		"anthem-plan",
		"todo",
	}
	for _, check := range checks {
		if !strings.Contains(planModePromptSuffix, check) {
			t.Errorf("planModePromptSuffix missing %q", check)
		}
	}
}

func TestScoutPromptContents(t *testing.T) {
	checks := []string{
		"Scout Mode",
		"explores",
		"query",
		"scope",
		"focus",
		"trivially simple",
		"test coverage",
		"security",
	}
	for _, check := range checks {
		if !strings.Contains(scoutPromptSuffix, check) {
			t.Errorf("scoutPromptSuffix missing %q", check)
		}
	}
}

func TestSynthesisPromptContents(t *testing.T) {
	checks := []string{
		"Synthesis Mode",
		"Explorer agents",
		"anthem-plan",
		"MUST be backed by explorer findings",
	}
	for _, check := range checks {
		if !strings.Contains(synthesisPromptSuffix, check) {
			t.Errorf("synthesisPromptSuffix missing %q", check)
		}
	}
}

func TestConsultPlan_UsesPlanMaxTurns(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	var capturedOpts types.RunOpts
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Test\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", 100000, 10, 25, 10, 5, testLogger())
	_, err := oa.ConsultPlan(context.Background(), StateSnapshot{}, "", nil)
	if err != nil {
		t.Fatalf("ConsultPlan() error: %v", err)
	}

	if capturedOpts.MaxTurns != 25 {
		t.Errorf("ConsultPlan MaxTurns = %d, want 25 (planMaxTurns)", capturedOpts.MaxTurns)
	}

	wantDenied := []string{"Write", "Edit", "MultiEdit"}
	if len(capturedOpts.DeniedTools) != len(wantDenied) {
		t.Fatalf("ConsultPlan DeniedTools = %v, want %v", capturedOpts.DeniedTools, wantDenied)
	}
	for i, tool := range wantDenied {
		if capturedOpts.DeniedTools[i] != tool {
			t.Errorf("ConsultPlan DeniedTools[%d] = %q, want %q", i, capturedOpts.DeniedTools[i], tool)
		}
	}
}

func TestScoutPlan_ParsesExploreRequests(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "scout-s1",
			Output: `{"reasoning": "Need to check tests and security", "explores": [
				{"query": "Check test coverage for auth", "scope": "backend/tests/", "focus": "tests"},
				{"query": "Check path traversal prevention", "scope": "backend/", "focus": "security"}
			], "user_message": "Researching your codebase..."}`,
			TokensIn: 50, TokensOut: 30,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", 100000, 10, 25, 10, 5, testLogger())
	explores, userMsg, err := oa.ScoutPlan(context.Background(), StateSnapshot{}, "", nil)
	if err != nil {
		t.Fatalf("ScoutPlan() error: %v", err)
	}
	if len(explores) != 2 {
		t.Fatalf("expected 2 explores, got %d", len(explores))
	}
	if explores[0].Focus != "tests" {
		t.Errorf("explore[0].Focus = %q, want tests", explores[0].Focus)
	}
	if explores[1].Focus != "security" {
		t.Errorf("explore[1].Focus = %q, want security", explores[1].Focus)
	}
	if userMsg != "Researching your codebase..." {
		t.Errorf("user_message = %q, want 'Researching your codebase...'", userMsg)
	}
}

func TestScoutPlan_CapsAtMaxExplorers(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "scout-s1",
			Output: `{"reasoning": "many areas", "explores": [
				{"query": "q1", "scope": ".", "focus": "tests"},
				{"query": "q2", "scope": ".", "focus": "security"},
				{"query": "q3", "scope": ".", "focus": "architecture"}
			], "user_message": ""}`,
			TokensIn: 50, TokensOut: 30,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", 100000, 10, 25, 10, 2, testLogger())
	explores, _, err := oa.ScoutPlan(context.Background(), StateSnapshot{}, "", nil)
	if err != nil {
		t.Fatalf("ScoutPlan() error: %v", err)
	}
	if len(explores) != 2 {
		t.Errorf("expected explores capped at 2 (maxExplorers), got %d", len(explores))
	}
}

func TestParseExplorerFindings(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantSum string
		wantErr bool
	}{
		{
			name:    "valid findings block",
			input:   "Some text\n```explorer-findings\n{\"query\": \"test q\", \"findings\": \"found X\", \"summary\": \"X exists\"}\n```\nMore text",
			wantSum: "X exists",
		},
		{
			name:    "no findings block falls back to raw output",
			input:   "Just raw text about what I found",
			wantSum: "Just raw text about what I found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseExplorerFindings(tt.input)
			if err != nil {
				t.Fatalf("parseExplorerFindings() error: %v", err)
			}
			if result.Summary != tt.wantSum {
				t.Errorf("summary = %q, want %q", result.Summary, tt.wantSum)
			}
		})
	}
}

func TestBuildExplorerPrompt(t *testing.T) {
	req := ExploreRequest{
		Query: "Check test coverage",
		Scope: "backend/tests/",
		Focus: "tests",
	}
	prompt := BuildExplorerPrompt(req, "src/\n  main.go\n  lib/")
	checks := []string{
		"Research Agent",
		"Check test coverage",
		"tests",
		"backend/tests/",
		"explorer-findings",
		"main.go",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("explorer prompt missing %q", check)
		}
	}
}

func TestBuildExplorerPrompt_TestsFocusInstructions(t *testing.T) {
	req := ExploreRequest{
		Query: "What test coverage exists?",
		Scope: "backend/",
		Focus: "tests",
	}
	prompt := BuildExplorerPrompt(req, "")
	checks := []string{
		"route handlers/endpoints have dedicated tests",
		"service-layer tests exist but the endpoint/route",
		"security-critical functions",
		"partially tested files",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("tests focus prompt missing %q", check)
		}
	}
}

func TestBuildExplorerPrompt_SecurityFocusInstructions(t *testing.T) {
	req := ExploreRequest{
		Query: "Audit security boundaries",
		Scope: "backend/",
		Focus: "security",
	}
	prompt := BuildExplorerPrompt(req, "")
	checks := []string{
		"input validation function",
		"path traversal guard",
		"authentication middleware",
		"zero test coverage as CRITICAL",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("security focus prompt missing %q", check)
		}
	}
}

func TestBuildExplorerPrompt_DefaultFocusNoExtra(t *testing.T) {
	req := ExploreRequest{
		Query: "How is the code structured?",
		Scope: "src/",
		Focus: "architecture",
	}
	prompt := BuildExplorerPrompt(req, "")

	forbidden := []string{
		"route handlers/endpoints have dedicated tests",
		"path traversal guard",
		"zero test coverage as CRITICAL",
		"Test Coverage Deep-Dive",
		"Security Boundary Audit",
	}
	for _, check := range forbidden {
		if strings.Contains(prompt, check) {
			t.Errorf("architecture focus prompt should NOT contain %q", check)
		}
	}
}

func TestSynthesizePlan_IncludesFindings(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	var capturedPrompt string
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedPrompt = opts.Prompt
		return &types.RunResult{
			SessionID: "synth-s1",
			Output:    "```anthem-plan\n# Synthesized Plan\n```",
			TokensIn:  50, TokensOut: 30,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", 100000, 10, 25, 10, 5, testLogger())
	findings := []ExploreResult{
		{Query: "Check tests", Summary: "No tests for /api/costs", Findings: "Detailed finding text"},
		{Query: "Check security", Error: "explorer timed out"},
	}
	output, err := oa.SynthesizePlan(context.Background(), StateSnapshot{}, findings, "", nil)
	if err != nil {
		t.Fatalf("SynthesizePlan() error: %v", err)
	}
	if !strings.Contains(output, "Synthesized Plan") {
		t.Error("expected plan output from synthesis")
	}
	if !strings.Contains(capturedPrompt, "No tests for /api/costs") {
		t.Error("synthesis prompt should include explorer findings summary")
	}
	if !strings.Contains(capturedPrompt, "explorer timed out") {
		t.Error("synthesis prompt should include explorer errors")
	}
	if !strings.Contains(capturedPrompt, "Synthesis Mode") {
		t.Error("synthesis prompt should include synthesis mode suffix")
	}
}

func TestBuildModePromptContents(t *testing.T) {
	checks := []string{
		"Build Mode",
		"create_subtasks",
		"MUST emit a create_subtasks",
		"1-based ordinal",
		"todo",
	}
	for _, check := range checks {
		if !strings.Contains(buildModePromptSuffix, check) {
			t.Errorf("buildModePromptSuffix missing %q", check)
		}
	}
}

func TestExtractModelTag(t *testing.T) {
	tests := []struct {
		input     string
		wantModel string
		wantClean string
	}{
		{"[model:claude-sonnet-4-6] hello", "claude-sonnet-4-6", "hello"},
		{"[system:fast] [model:claude-opus-4-6] hi", "claude-opus-4-6", "[system:fast] hi"},
		{"no model tag here", "", "no model tag here"},
		{"[model:haiku] ", "haiku", ""},
	}
	for _, tt := range tests {
		model, cleaned := extractModelTag(tt.input)
		if model != tt.wantModel {
			t.Errorf("extractModelTag(%q) model = %q, want %q", tt.input, model, tt.wantModel)
		}
		if cleaned != tt.wantClean {
			t.Errorf("extractModelTag(%q) cleaned = %q, want %q", tt.input, cleaned, tt.wantClean)
		}
	}
}
