// Package prompt assembles the guest-facing prompt string used by the
// orchestrator's chat/plan dispatch and the Execute runner's chained
// step dispatch. It is intentionally a leaf package: it depends only on
// internal/types for the Mode enum and accepts every context
// component as a pre-rendered string so it does not pull in the
// orchestrator's YAML/feature types (which would create an import
// cycle the moment execute.runner tried to reuse this builder).
//
// See plan decision D1 ("live re-read per step") and D10 (single PR)
// for why both dispatch paths now converge here.
package prompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

// ContextSources is the read-side handle the prompt builder needs so it
// can re-load the five LiveContext slices on every step instead of
// holding a stale snapshot captured at run-start. Implementations live
// in the orchestrator (which owns the convo buffer, shared context
// maps, project summary, and VOICE.md cache); the prompt package
// itself is a leaf so it can be imported by both orchestrator and
// execute without creating an import cycle.
//
// Every method must be safe to call concurrently with orchestrator
// updates: the execute runner may re-query while the user is typing
// in chat. Returning a freshly-formatted string (not a reference into
// internal state) keeps the contract trivially race-free.
//
// See plan decisions D1 (live re-read per step) and D12 (history
// timestamp filter for chain determinism).
type ContextSources interface {
	UserContext() string
	ProjectSummary() string
	// FeatureContext hydrates the feature section for the current
	// call site. upstreamArtifactIDs (optional) narrows the
	// "Available Artifacts" subsection so artifacts produced by the
	// directly upstream step render with full metadata while every
	// other feature artifact is summarized as a one-liner (plan
	// decision D11). An empty slice preserves the legacy behavior
	// where every artifact is rendered in full.
	FeatureContext(feature string, upstreamArtifactIDs []string) string
	SharedContextText(channelKey string) string
	HistoryText(channelKey string, before time.Time) string
}

// LoadLiveContext assembles a LiveContext by calling each ContextSources
// method in sequence. The before timestamp filters history so an
// in-flight chained run does not see user chat messages that landed
// after run-start (D12) -- pass time.Time{} (zero value) to disable
// filtering for chat-mode dispatch.
//
// The feature / channelKey arguments are pass-through: callers own
// the lookup logic (active feature on the orchestrator, channel kind
// for chat, "" for orchestrator-internal planning).
func LoadLiveContext(cs ContextSources, feature, channelKey string, before time.Time, upstreamArtifactIDs []string) LiveContext {
	if cs == nil {
		return LiveContext{}
	}
	return LiveContext{
		UserContext:    cs.UserContext(),
		ProjectSummary: cs.ProjectSummary(),
		FeatureContext: cs.FeatureContext(feature, upstreamArtifactIDs),
		SharedCtxText:  cs.SharedContextText(channelKey),
		HistoryText:    cs.HistoryText(channelKey, before),
	}
}

// LiveContext bundles the five context slices that are re-read fresh
// on every chained step (and supplied verbatim in chat dispatch). The
// caller is responsible for the actual re-read -- see LoadLiveContext
// -- so BuildGuestPrompt stays trivially testable.
type LiveContext struct {
	UserContext    string
	ProjectSummary string
	FeatureContext string
	SharedCtxText  string
	HistoryText    string
}

// GuestPromptOpts carries everything that varies between a Chat turn
// and a chained Execute step. All fields are optional; the zero value
// is a valid Chat-mode invocation with no feature/plan/story context.
type GuestPromptOpts struct {
	Mode        types.Mode
	ChannelKind string

	// Chat/Plan-mode fields.
	PlanContent      string
	StoryContextText string
	FocusText        string

	// Common shaping knobs.
	IncludeCharacterCommitment bool

	// Execute-mode fields (plan decisions D4, D5, D7).
	PlanContext             *PlanContextOpts
	PriorRevisionFeedback   *RevisionFeedback
	UpstreamArtifactsText   string
	FeatureContextHasReport bool
}

// PlanContextOpts renders the surgical plan-position section described
// in plan decision D5: one-liner for every prior step, full detail
// only for steps whose artifacts are listed as upstream inputs to the
// current step, plus an optional next-step hint.
type PlanContextOpts struct {
	PlanTitle          string
	ThisStepID         string
	ThisStepTitle      string
	ThisStepAgentID    string
	PriorStepSummaries []StepOneLiner
	UpstreamDetail     []CompletedStepDetail
	NextStep           *NextStepHint
}

// StepOneLiner renders as a single bullet (`- s2 (walt): completed --
// "Animate idle"`). Deliberately minimal so a 30-step plan stays
// under a kilobyte in the Plan Context section.
type StepOneLiner struct {
	ID      string
	AgentID string
	Status  string
	Title   string
}

// CompletedStepDetail is rendered only when the step is referenced
// as upstream to the current step. It expands into the step
// description plus a compact list of the artifacts it produced.
type CompletedStepDetail struct {
	ID          string
	AgentID     string
	Description string
	Artifacts   []ArtifactSummary
	// FinalReport is the upstream agent's parsed context_report (if
	// any). The renderer surfaces it immediately after the step
	// description so downstream LLMs see concrete values (chosen
	// slug, artifact IDs, paths) instead of only the upstream step's
	// placeholder-laden description copied from the compiled plan.
	// Nil means the upstream agent didn't emit a parseable report
	// and no "Final report from <agent>" section is rendered.
	FinalReport *UpstreamReportSummary
}

// UpstreamReportSummary is the compact view of an upstream agent's
// context_report that downstream step prompts render. It is the
// subset of contextreport.ContextReport that carries the concrete
// values downstream agents need most (slug, artifact id/path,
// declared produces) without dragging the prompt package into a
// dependency on contextreport.
type UpstreamReportSummary struct {
	Summary      string
	Action       string
	ArtifactID   string
	ArtifactPath string
	Produces     []string
}

// ArtifactSummary is the compact artifact reference used inside
// CompletedStepDetail and the free-standing Upstream Artifacts
// section.
type ArtifactSummary struct {
	ID      string
	Kind    string
	Path    string
	Summary string
}

// NextStepHint tells the current step what hands the baton next, so
// agents can shape their context_report to match. Optional; omitted
// in tail-step prompts.
type NextStepHint struct {
	ID      string
	AgentID string
	Title   string
}

// RevisionFeedback surfaces the user's gate revise note on re-runs
// (plan decision D4). Only rendered when RevisionCount > 0.
type RevisionFeedback struct {
	GateID        string
	RevisionCount int
	UserNote      string
}

// BuildGuestPrompt assembles the final prompt for a single guest
// invocation. The ordering is stable so snapshot tests can assert the
// full layout: persona -> character commitment -> user/project/feature
// context -> plan position (execute) -> session/history -> mode extras
// (plan mode policy, story bible, prism visual output) -> context
// reporting block -> the user message itself -> focus block.
func BuildGuestPrompt(persona string, live LiveContext, userMsg string, opts GuestPromptOpts) string {
	var sb strings.Builder

	sb.WriteString(persona)
	sb.WriteString("\n\n")

	if opts.IncludeCharacterCommitment {
		sb.WriteString("## Character Commitment\n\n")
		sb.WriteString("You ARE this character. Embody their creative philosophy, voice, and perspective at all times. ")
		sb.WriteString("Never break character. Never say you are an AI, a language model, or Claude. ")
		sb.WriteString("When asked personal questions, draw from your namesake's documented views, creative works, and known history. ")
		sb.WriteString("Your expertise, opinions, and personality are theirs.\n\n")
	}

	if live.UserContext != "" {
		sb.WriteString("## User Context\n\n")
		sb.WriteString(live.UserContext)
		sb.WriteString("\n\n")
	}

	if live.ProjectSummary != "" {
		sb.WriteString("## Project Context\n")
		sb.WriteString(live.ProjectSummary)
		sb.WriteString("\n\n")
	}

	if live.FeatureContext != "" {
		sb.WriteString(live.FeatureContext)
		sb.WriteString("\n\n")
	}

	if opts.PlanContext != nil {
		sb.WriteString(renderPlanContext(opts.PlanContext))
	}

	if opts.PriorRevisionFeedback != nil && opts.PriorRevisionFeedback.RevisionCount > 0 {
		sb.WriteString(renderRevisionFeedback(opts.PriorRevisionFeedback))
	}

	if opts.UpstreamArtifactsText != "" {
		sb.WriteString(opts.UpstreamArtifactsText)
		if !strings.HasSuffix(opts.UpstreamArtifactsText, "\n\n") {
			if strings.HasSuffix(opts.UpstreamArtifactsText, "\n") {
				sb.WriteString("\n")
			} else {
				sb.WriteString("\n\n")
			}
		}
	}

	if live.SharedCtxText != "" {
		sb.WriteString("## Session Context\n")
		sb.WriteString(live.SharedCtxText)
		sb.WriteString("\n\n")
	}

	if live.HistoryText != "" {
		sb.WriteString(live.HistoryText)
		sb.WriteString("\n")
	}

	if opts.Mode == types.ModePlan && opts.PlanContent != "" {
		sb.WriteString("## Current Plan\n\n")
		sb.WriteString(opts.PlanContent)
		sb.WriteString("\n\n## Your Task\n\n")
		sb.WriteString("Review and contribute to this plan from your area of expertise.\n")
		sb.WriteString("- To EDIT the plan, include your updated markdown in a ```plan-edit code block.\n")
		sb.WriteString("  Only include the sections you are changing.\n")
		sb.WriteString("- To COMMENT without editing, just respond normally.\n\n")
	}

	if opts.Mode == types.ModePlan {
		sb.WriteString("## Plan Mode Tool Policy\n\n")
		sb.WriteString("You MUST NOT call any tools this turn. Respond in text only. ")
		sb.WriteString("To modify the plan, emit a ```plan-edit fenced block. ")
		sb.WriteString("No MCP calls, no HTTP tools, no Bash, no Edit/Write. ")
		sb.WriteString("Tool access is stripped at the harness level — attempted tool calls will fail.\n\n")
	}

	if opts.StoryContextText != "" {
		sb.WriteString(opts.StoryContextText)
		if !strings.HasSuffix(opts.StoryContextText, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if opts.ChannelKind == "prism" {
		sb.WriteString(prismVisualOutputSection())
	}

	if live.FeatureContext != "" || opts.FeatureContextHasReport {
		sb.WriteString(contextReportingBlock())
	}

	fmt.Fprintf(&sb, "## User Message\n\n%s\n", userMsg)

	if opts.FocusText != "" {
		sb.WriteString("\n## Your Focus\n\n")
		fmt.Fprintf(&sb, "> %s\n\n", opts.FocusText)
		sb.WriteString("Respond to this specific instruction. The full message above is provided for context.\n")
	}

	if opts.Mode == types.ModeChat {
		sb.WriteString("\nBe concise.\n")
	}

	return sb.String()
}

func renderPlanContext(pc *PlanContextOpts) string {
	var sb strings.Builder
	sb.WriteString("## Plan Context\n\n")
	if pc.PlanTitle != "" {
		fmt.Fprintf(&sb, "Plan: %s\n", pc.PlanTitle)
	}
	if pc.ThisStepID != "" {
		fmt.Fprintf(&sb, "Current step: %s", pc.ThisStepID)
		if pc.ThisStepTitle != "" {
			fmt.Fprintf(&sb, " -- %s", pc.ThisStepTitle)
		}
		if pc.ThisStepAgentID != "" {
			fmt.Fprintf(&sb, " (assigned to %s)", pc.ThisStepAgentID)
		}
		sb.WriteString("\n")
	}

	if len(pc.PriorStepSummaries) > 0 {
		sb.WriteString("\n### Prior Steps (one-line summaries)\n")
		for _, s := range pc.PriorStepSummaries {
			fmt.Fprintf(&sb, "- %s", s.ID)
			if s.AgentID != "" {
				fmt.Fprintf(&sb, " (%s)", s.AgentID)
			}
			if s.Status != "" {
				fmt.Fprintf(&sb, ": %s", s.Status)
			}
			if s.Title != "" {
				fmt.Fprintf(&sb, " -- %s", s.Title)
			}
			sb.WriteString("\n")
		}
	}

	if len(pc.UpstreamDetail) > 0 {
		sb.WriteString("\n### Upstream Step Detail (directly preceding this step)\n")
		for _, d := range pc.UpstreamDetail {
			fmt.Fprintf(&sb, "\n#### %s (%s)\n", d.ID, d.AgentID)
			if d.Description != "" {
				sb.WriteString(d.Description)
				sb.WriteString("\n")
			}
			if d.FinalReport != nil {
				sb.WriteString(renderUpstreamReport(d.AgentID, d.FinalReport))
			}
			if len(d.Artifacts) > 0 {
				sb.WriteString("Artifacts produced:\n")
				for _, a := range d.Artifacts {
					fmt.Fprintf(&sb, "- %s", a.ID)
					if a.Kind != "" {
						fmt.Fprintf(&sb, " (%s)", a.Kind)
					}
					if a.Path != "" {
						fmt.Fprintf(&sb, " -- %s", a.Path)
					}
					if a.Summary != "" {
						fmt.Fprintf(&sb, " :: %s", a.Summary)
					}
					sb.WriteString("\n")
				}
			}
		}
	}

	if pc.NextStep != nil {
		sb.WriteString("\n### Next Step\n")
		fmt.Fprintf(&sb, "After this step, %s runs %s", pc.NextStep.AgentID, pc.NextStep.ID)
		if pc.NextStep.Title != "" {
			fmt.Fprintf(&sb, " (%s)", pc.NextStep.Title)
		}
		sb.WriteString(". Shape your context_report so they can pick up where you left off.\n")
	}

	sb.WriteString("\n")
	return sb.String()
}

// renderUpstreamReport emits a "Final report from <agent>" block
// underneath an upstream step's description so the downstream
// agent sees the concrete slug, artifact id, and path the upstream
// agent chose at runtime (instead of the placeholder-laden
// description copied from the compiled plan). Rendered only when
// at least one field is populated so an empty report does not
// create a bare header.
func renderUpstreamReport(agentID string, r *UpstreamReportSummary) string {
	if r == nil {
		return ""
	}
	has := r.Summary != "" || r.Action != "" || r.ArtifactID != "" || r.ArtifactPath != "" || len(r.Produces) > 0
	if !has {
		return ""
	}

	var sb strings.Builder
	label := agentID
	if label == "" {
		label = "upstream agent"
	}
	fmt.Fprintf(&sb, "\n##### Final report from %s\n", label)
	if r.Summary != "" {
		fmt.Fprintf(&sb, "- summary: %s\n", r.Summary)
	}
	if r.Action != "" {
		fmt.Fprintf(&sb, "- action: %s\n", r.Action)
	}
	if r.ArtifactID != "" || r.ArtifactPath != "" {
		sb.WriteString("- produced artifact:")
		if r.ArtifactID != "" {
			fmt.Fprintf(&sb, " %s", r.ArtifactID)
		}
		if r.ArtifactPath != "" {
			fmt.Fprintf(&sb, " at %s", r.ArtifactPath)
		}
		sb.WriteString("\n")
	}
	if len(r.Produces) > 0 {
		fmt.Fprintf(&sb, "- additional produces: %s\n", strings.Join(r.Produces, ", "))
	}
	return sb.String()
}

func renderRevisionFeedback(rf *RevisionFeedback) string {
	var sb strings.Builder
	sb.WriteString("## Prior Revision Feedback\n\n")
	fmt.Fprintf(&sb, "You are re-running this step after a gate revise (revision %d", rf.RevisionCount)
	if rf.GateID != "" {
		fmt.Fprintf(&sb, " on gate %s", rf.GateID)
	}
	sb.WriteString("). The user asked for:\n\n")
	if note := strings.TrimSpace(rf.UserNote); note != "" {
		fmt.Fprintf(&sb, "> %s\n\n", note)
	} else {
		sb.WriteString("> (no written note)\n\n")
	}
	sb.WriteString("Address this feedback in your next output. Do not regenerate anything outside the scope of the user's note.\n\n")
	return sb.String()
}

func prismVisualOutputSection() string {
	var sb strings.Builder
	sb.WriteString("## Visual Output\n\n")
	sb.WriteString("You are connected to Prism, a visual interface. EVERY response MUST include a visual artifact.\n")
	sb.WriteString("Put your visual content in a fenced ```html block:\n\n")
	sb.WriteString("```html\n")
	sb.WriteString("<div style=\"font-family: system-ui; padding: 2rem;\">\n")
	sb.WriteString("  <h1>Title</h1>\n")
	sb.WriteString("  <p>Self-contained HTML with all styles inline.</p>\n")
	sb.WriteString("</div>\n")
	sb.WriteString("```\n\n")
	sb.WriteString("The HTML block will be rendered as a rich visual artifact in the display pane.\n")
	sb.WriteString("HTML must be fully self-contained with all styles inline (no external stylesheets).\n")
	sb.WriteString("You can also use ```markdown blocks for formatted text artifacts.\n")
	sb.WriteString("Include a brief text explanation outside the block.\n\n")
	sb.WriteString("### Images from tools (Unity MCP, etc.)\n\n")
	sb.WriteString("When embedding captures as `<img src=\"...\">`, use a **single-line** `data:image/png;base64,<payload>` URL copied **verbatim** from tool output.\n")
	sb.WriteString("Do **not** insert line breaks, spaces, or line-wrapping inside the base64 payload — the browser will reject the URL (`ERR_INVALID_URL`).\n")
	sb.WriteString("Do not use placeholders, ellipses, or shortened base64; paste the full string from the tool result.\n\n")
	return sb.String()
}

func contextReportingBlock() string {
	var sb strings.Builder
	sb.WriteString("## Context Reporting\n\n")
	sb.WriteString("After your response, you may optionally include a structured report so the system can track your work.\n")
	sb.WriteString("Place this JSON block at the END of your response (after all conversational text):\n\n")
	sb.WriteString("```json\n")
	sb.WriteString("{\"context_report\": {\n")
	sb.WriteString("  \"action\": \"asset_created | document_edit | decision_made | task_completed\",\n")
	sb.WriteString("  \"summary\": \"Brief description of what you did\",\n")
	sb.WriteString("  \"artifact\": {\n")
	sb.WriteString("    \"id\": \"kebab-case-id\",\n")
	sb.WriteString("    \"type\": \"image/png | text/markdown | ...\",\n")
	sb.WriteString("    \"path\": \"relative/path/to/file\",\n")
	sb.WriteString("    \"description\": \"What this artifact is\",\n")
	sb.WriteString("    \"tags\": [\"tag1\", \"tag2\"],\n")
	sb.WriteString("    \"metadata\": {\"key\": \"value\"},\n")
	sb.WriteString("    \"depends_on\": [\"source-artifact-id\"],\n")
	sb.WriteString("    \"status\": \"draft\"\n")
	sb.WriteString("  },\n")
	sb.WriteString("  \"progress\": \"3/8 sprites complete\",\n")
	sb.WriteString("  \"blocked_on\": null,\n")
	sb.WriteString("  \"produces\": [\"artifact-id-1\", \"artifact-id-2\"]\n")
	sb.WriteString("}}\n")
	sb.WriteString("```\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- The report is optional. If you don't include one, a generic task_completed entry is created.\n")
	sb.WriteString("- Include \"artifact\" only when you create or modify a file.\n")
	sb.WriteString("- Use stable kebab-case IDs for all references.\n")
	sb.WriteString("- \"progress\" and \"blocked_on\" help other agents understand your status.\n")
	sb.WriteString("- Use canonical metadata keys: ")
	sb.WriteString("sprite (width, height, frames, palette, format), ")
	sb.WriteString("document (word_count, sections, format), ")
	sb.WriteString("scene (tile_count, layers, dimensions), ")
	sb.WriteString("audio (duration_ms, sample_rate, channels).\n")
	sb.WriteString("- Do NOT include any origin, plan_id, run_id, step_id, or agent_id fields; the system stamps those authoritatively.\n\n")
	return sb.String()
}

// RenderUpstreamArtifacts turns a slice of ArtifactSummary structs into
// the Upstream Artifacts markdown section. Separated from
// BuildGuestPrompt so the execute runner can cache/pre-format the
// payload once per step.
func RenderUpstreamArtifacts(arts []ArtifactSummary) string {
	if len(arts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Upstream Artifacts\n")
	for _, a := range arts {
		fmt.Fprintf(&sb, "- **%s**", a.Path)
		if a.Kind != "" {
			fmt.Fprintf(&sb, " (%s)", a.Kind)
		}
		if a.Summary != "" {
			fmt.Fprintf(&sb, ": %s", a.Summary)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
