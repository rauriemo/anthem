package orchestrator

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/plans"
	"github.com/rauriemo/anthem/internal/types"
)

func TestParseControlTags_LeadingPrefixOnly(t *testing.T) {
	tags, body := parseControlTags("[system:plan] [model:claude-opus] draft it")
	if !tags["system:plan"] {
		t.Errorf("expected system:plan in tags, got %v", tags)
	}
	if !tags["model:claude-opus"] {
		t.Errorf("expected model:claude-opus in tags, got %v", tags)
	}
	if body != "draft it" {
		t.Errorf("body = %q, want %q", body, "draft it")
	}
}

func TestParseControlTags_IgnoresTagsInsideBody(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"plain body literal", "how do I trigger [system:plan:new] manually?"},
		{"inline code", "I saw `[system:plan:new]` somewhere"},
		{"fenced code", "Look at this:\n```\n[system:plan:new]\n```"},
		{"after plain-word prefix", "hello [system:plan]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tags, _ := parseControlTags(tc.input)
			for _, forbidden := range []string{"system:plan:new", "system:plan", "system:execute", "system:build", "system:chat", "system:loop", "system:fast", "system:status", "system:respec"} {
				if tags[forbidden] {
					t.Errorf("parseControlTags should not have recognized %q in body-positioned case %q; got tags=%v", forbidden, tc.input, tags)
				}
			}
		})
	}
}

func TestParseControlTags_LegacyAliasesAtPrefix(t *testing.T) {
	cases := []struct {
		input   string
		wantTag string
	}{
		{"[system:fast] hello", "system:fast"},
		{"[system:build] go", "system:build"},
		{"[system:status] howdy", "system:status"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			tags, _ := parseControlTags(tc.input)
			if !tags[tc.wantTag] {
				t.Errorf("expected %q in tags, got %v", tc.wantTag, tags)
			}
		})
	}
}

func TestParseControlTags_MultipleStackedAtPrefix(t *testing.T) {
	tags, body := parseControlTags("[system:plan] [system:plan:new] fresh start")
	if !tags["system:plan"] {
		t.Error("missing system:plan tag")
	}
	if !tags["system:plan:new"] {
		t.Error("missing system:plan:new tag")
	}
	if body != "fresh start" {
		t.Errorf("body = %q, want %q", body, "fresh start")
	}
}

func TestStripLeadingTag_RemovesOnlyTargetFromPrefix(t *testing.T) {
	out := stripLeadingTag("[system:plan] [system:plan:new] draft v2", "system:plan:new")
	if out != "[system:plan]  draft v2" && out != "[system:plan] draft v2" {
		t.Errorf("stripLeadingTag unexpected output: %q", out)
	}
}

func TestStripLeadingTag_LeavesBodyLiteralAlone(t *testing.T) {
	// Body-positioned literal must survive.
	in := "how do I use [system:plan:new]?"
	out := stripLeadingTag(in, "system:plan:new")
	if out != in {
		t.Errorf("stripLeadingTag altered body text: in=%q out=%q", in, out)
	}
}

func TestStripLeadingTag_NoopWhenAbsent(t *testing.T) {
	in := "[system:plan] hi"
	out := stripLeadingTag(in, "system:plan:new")
	if out != in {
		t.Errorf("stripLeadingTag should be a noop; in=%q out=%q", in, out)
	}
}

func TestHandlePlanNew_ArchivesAndDrafts(t *testing.T) {
	// First plan draft exists, then /newplan archives it and a subsequent Plan
	// message should start a fresh draft.
	orchRunner := agent.NewMockRunner()
	callCount := 0
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{SessionID: "scout-s", Output: `{"reasoning":"","explores":[],"user_message":""}`}, nil
		}
		callCount++
		return &types.RunResult{
			SessionID: "plan-s",
			Output:    "```anthem-plan\n# Plan v" + strconv.Itoa(callCount) + "\n\n## Tasks\n\n### 1. Work\n```",
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] first draft",
		Timestamp:   time.Now(),
	})
	firstID := planDisplayID(t, ch, "first")

	metas, _ := orch.planStore.List("owner/repo")
	if len(metas) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(metas))
	}
	firstPath := metas[0].Path

	ch.reset()

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] [system:plan:new] second idea",
		Timestamp:   time.Now(),
	})
	secondID := planDisplayID(t, ch, "second")

	if firstID == secondID {
		t.Errorf("expected new DisplayID after /newplan, got %q twice", firstID)
	}
	if !strings.HasPrefix(secondID, "plan:") {
		t.Errorf("second broadcast missing plan: prefix: %q", secondID)
	}

	p, err := orch.planStore.Load(firstPath)
	if err != nil {
		t.Fatalf("load first plan: %v", err)
	}
	if p.Frontmatter.Status != plans.StatusArchived {
		t.Errorf("first plan status = %q, want archived", p.Frontmatter.Status)
	}

	metas, _ = orch.planStore.List("owner/repo")
	if len(metas) != 2 {
		t.Fatalf("expected 2 plans total, got %d", len(metas))
	}
}

func TestHandlePlanNew_IgnoresBodyLiteral(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{SessionID: "scout", Output: `{"reasoning":"","explores":[],"user_message":""}`}, nil
		}
		return &types.RunResult{
			SessionID: "plan",
			Output:    "```anthem-plan\n# Ask\n\n## Tasks\n\n### 1. Do it\n```",
		}, nil
	}

	orch, _ := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] first draft please",
		Timestamp:   time.Now(),
	})

	metas, _ := orch.planStore.List("owner/repo")
	if len(metas) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(metas))
	}
	firstPath := metas[0].Path

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] how do I use `[system:plan:new]`?",
		Timestamp:   time.Now(),
	})

	p, err := orch.planStore.Load(firstPath)
	if err != nil {
		t.Fatalf("load first plan: %v", err)
	}
	if p.Frontmatter.Status == plans.StatusArchived {
		t.Errorf("draft was archived by a body-positioned tag literal; status = %q", p.Frontmatter.Status)
	}
}
