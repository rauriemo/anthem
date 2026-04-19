package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/execute"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/plans"
)

// --- Compile: markdown plan -> ExecutionPlan sidecar ---

// execPlanGateRe matches the extended gate tag Prism emits for execution-plan
// approval: [gate:<action>:execplan:<planID>:<compileGeneration>] with an
// optional trailing :skip=<id1>,<id2>,... that lets the UI drop approval
// gates the user toggled off before dispatch. Step-level gates inside
// PlanRunner still use the bare [gate:<action>] form handled by gateTagRe in
// orchestrator.go; the "execplan:" subtag is what tells us this action
// targets the compiled plan artifact, not an in-flight step gate.
//
// Capture groups: 1=action, 2=planID, 3=compileGeneration, 4=skip list
// (raw, comma-separated, may be empty or absent).
var execPlanGateRe = regexp.MustCompile(`\[gate:(approve|revise|abort):execplan:([^\]:]+):(\d+)(?::skip=([^\]]*))?\]`)

// isExecPlanGateAction returns true when the message carries an execplan gate
// tag. This is checked before the generic step-gate regex so execplan
// approvals are always routed through handleExecutePlanApproval rather than
// the PlanRunner-scoped resolver.
func isExecPlanGateAction(text string) bool {
	return execPlanGateRe.MatchString(text)
}

// execPlanGate is the parsed form of an execplan gate tag.
type execPlanGate struct {
	Action            execute.GateAction
	PlanID            string
	CompileGeneration int
	Feedback          string
	// SkippedGateIDs is the set of approval-gate IDs the user toggled OFF
	// in the Prism preview before dispatching. Parsed from the optional
	// :skip=<csv> suffix on the gate tag; empty when no skips were
	// requested. Unknown IDs are tolerated at apply time — see
	// handleExecutePlanApproval for the filter.
	SkippedGateIDs []string
}

func parseExecPlanGate(text string) (execPlanGate, bool) {
	m := execPlanGateRe.FindStringSubmatch(text)
	if len(m) < 4 {
		return execPlanGate{}, false
	}
	var gen int
	if _, err := fmt.Sscanf(m[3], "%d", &gen); err != nil {
		return execPlanGate{}, false
	}
	feedback := strings.TrimSpace(execPlanGateRe.ReplaceAllString(text, ""))
	var skipped []string
	if len(m) >= 5 && m[4] != "" {
		skipped = parseSkipList(m[4])
	}
	return execPlanGate{
		Action:            execute.GateAction(m[1]),
		PlanID:            m[2],
		CompileGeneration: gen,
		Feedback:          feedback,
		SkippedGateIDs:    skipped,
	}, true
}

// parseSkipList splits a comma-separated skip-list into a deduped, trimmed
// slice in input order. Empty tokens (trailing comma, double comma) are
// dropped so "g1,,g2," parses to []string{"g1","g2"}. The order is
// preserved so audit logs remain readable in the sequence the UI intended.
func parseSkipList(raw string) []string {
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// filterExecPlanGates returns a copy of gates with entries whose ID appears
// in skippedIDs removed. Unknown IDs (not present in gates) are ignored —
// the UI may have sent an ID for a gate that was already resolved or
// compiled away between preview and dispatch, which is fine because the
// observable effect (the gate does not open) is identical either way.
func filterExecPlanGates(gates []execute.ApprovalGate, skippedIDs []string) []execute.ApprovalGate {
	if len(skippedIDs) == 0 {
		return gates
	}
	skip := make(map[string]bool, len(skippedIDs))
	for _, id := range skippedIDs {
		skip[id] = true
	}
	out := make([]execute.ApprovalGate, 0, len(gates))
	for _, g := range gates {
		if skip[g.ID] {
			continue
		}
		out = append(out, g)
	}
	return out
}

// handleExecuteMarkdown is the Execute-mode entry point when the incoming
// message does not already carry an inline [execute:plan]...[/execute:plan]
// YAML body. Source precedence is explicit: an inline anthem-plan fenced
// block in the current message beats any stored LatestDraft, because a user
// who pasted plan markdown into an Execute-mode message clearly means "use
// this one." When neither inline nor stored markdown is present we reply
// with a no-draft pointer rather than silently doing nothing.
//
// The compile round-trip is:
//  1. Resolve markdown source (inline beats draft).
//  2. Acquire the per-plan-ID compile mutex, rejecting concurrent compiles
//     with a clear message. This avoids interleaved SaveCompiled writes.
//  3. Snapshot the active guest roster so the compiler's agent-id assignments
//     are constrained to guests that are actually in the room right now.
//  4. If a prior compiled sidecar exists, pass its body as PriorCompilation
//     so the compiler can reuse step/gate ids and produce a revise (not a
//     regeneration).
//  5. ConsultCompilePlan -> validate against the guest roster -> embed
//     planGeneration in the JSON body -> SaveCompiled (which bumps
//     CompileGeneration, CompiledAt, RequiredProfiles).
//  6. Broadcast an execplan:<planID> artifact so Prism can render the plan
//     preview with Approve/Revise/Abort buttons; the current
//     compile_generation is included so the button handler can reject stale
//     clicks.
//
// feedback is the body the user sent alongside the Execute trigger with
// control tags stripped. When non-empty AND a prior compilation exists this
// is treated as a revise instruction.
func (o *Orchestrator) handleExecuteMarkdown(ctx context.Context, msg channel.IncomingMessage, feedback string) {
	if o.orchAgent == nil {
		o.sendFollowUp(ctx, msg, "Execute mode requires the orchestrator agent to be configured.")
		return
	}
	if o.planStore == nil {
		o.sendFollowUp(ctx, msg, "Execute mode requires a plan store.")
		return
	}

	markdown, planPath, planID, source := o.resolveExecuteMarkdownSource(msg.Text)
	if markdown == "" {
		o.sendFollowUp(ctx, msg,
			"Execute mode needs a plan to compile. Switch to Plan mode to draft one, "+
				"or paste an ```anthem-plan``` block inline in this message.")
		o.recordAudit(ctx, "channel.execute_no_plan", "", strPtr("execute"))
		return
	}

	// Acquire per-plan compile mutex. Inline-only plans (not persisted) use
	// a synthetic ID derived from the markdown content so two parallel
	// inline submissions of the same text still collapse to one compile.
	mutexID := planID
	if mutexID == "" {
		mutexID = "inline:" + shortHashString(markdown)
	}
	o.compileMu.Lock()
	if o.compileInFlight[mutexID] {
		o.compileMu.Unlock()
		o.sendFollowUp(ctx, msg,
			"A compile is already running for this plan. Wait for it to finish before requesting another.")
		return
	}
	o.compileInFlight[mutexID] = true
	o.compileMu.Unlock()
	defer func() {
		o.compileMu.Lock()
		delete(o.compileInFlight, mutexID)
		o.compileMu.Unlock()
	}()

	guests := o.activeGuestProfiles()
	if len(guests) == 0 {
		o.sendFollowUp(ctx, msg,
			"Execute mode needs at least one active guest agent to assign work to. "+
				"Invite a guest and retry.")
		return
	}

	var priorBody string
	if planPath != "" {
		body, _, err := o.planStore.LoadCompiled(planPath)
		if err == nil {
			priorBody = string(body)
		} else if errors.Is(err, plans.ErrCompiledSidecarDrift) {
			// Drift means the sidecar existed but doesn't match the current
			// markdown generation. Still feed it in so the compiler can
			// preserve step/gate IDs; SaveCompiled will bump to a fresh
			// generation afterwards.
			priorBody = string(body)
		} else if !errors.Is(err, os.ErrNotExist) {
			o.logger.Warn("execute: load compiled sidecar", "error", err, "plan_path", planPath)
		}
	}

	compileResult, err := o.orchAgent.ConsultCompilePlan(ctx, CompilePlanInput{
		MarkdownPlan:     markdown,
		ActiveGuests:     guests,
		PriorCompilation: priorBody,
		Feedback:         feedback,
	})
	if err != nil {
		o.logger.Warn("execute: compile consult failed", "error", err)
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to compile plan: %v", err))
		o.recordAudit(ctx, "channel.execute_compile_failed", "", strPtr("execute"))
		return
	}
	// MissingProfile alias recovery: when the compile LLM bails with
	// "missing profile: X" but X actually resolves to one of the active
	// guests via its name or profile (common when the plan markdown
	// refers to a guest by display name like "Tower" instead of its id
	// "orchestrator"), retry the compile once with an explicit
	// Feedback hint naming the resolved id. This keeps plan-authoring
	// agnostic about whether the user wrote "Tower", "orchestrator", or
	// the agent's role in their markdown. A single retry is enough —
	// if the second pass still bails, surface the original error so
	// the user disambiguates explicitly.
	if compileResult.MissingProfile != "" {
		resolved := resolveGuestAlias(compileResult.MissingProfile, guests)
		if resolved != "" {
			o.logger.Info("execute: resolved missing profile alias, retrying compile",
				"missing", compileResult.MissingProfile,
				"resolved_id", resolved,
			)
			hint := fmt.Sprintf(
				"The plan referenced %q which matches an active guest with id %q. "+
					"Emit agent_id %q for every step that referenced %q.",
				compileResult.MissingProfile, resolved, resolved, compileResult.MissingProfile,
			)
			retryFeedback := hint
			if feedback != "" {
				retryFeedback = feedback + "\n\n" + hint
			}
			compileResult, err = o.orchAgent.ConsultCompilePlan(ctx, CompilePlanInput{
				MarkdownPlan:     markdown,
				ActiveGuests:     guests,
				PriorCompilation: priorBody,
				Feedback:         retryFeedback,
			})
			if err != nil {
				o.logger.Warn("execute: compile retry after alias resolution failed", "error", err)
				o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to compile plan: %v", err))
				o.recordAudit(ctx, "channel.execute_compile_failed", "", strPtr("execute"))
				return
			}
		}
	}
	if compileResult.MissingProfile != "" {
		o.sendFollowUp(ctx, msg,
			fmt.Sprintf("Cannot compile plan: no active guest can fulfill profile %q. "+
				"Invite a guest with that profile and retry.", compileResult.MissingProfile))
		o.recordAudit(ctx, "channel.execute_missing_profile", "", strPtr("execute"))
		return
	}
	compiled := compileResult.Plan

	guestIDs := make([]string, 0, len(guests))
	for _, g := range guests {
		guestIDs = append(guestIDs, g.ID)
	}
	// Validate alias recovery: even after the retry above (or on a
	// first-pass success) the LLM may still emit a step.agent_id that
	// happens to match a guest name or profile instead of the guest id
	// — e.g. a step.agent_id of "Tower" when the guest id is
	// "orchestrator". Walk the steps once and rewrite any such unique
	// alias to its id before Validate sees them. Unknown tokens that
	// don't resolve are left in place so Validate surfaces a clear
	// error pointing at the real offender.
	rewriteStepAliases(compiled, guests, o.logger)
	requiredProfiles := requiredProfilesFromPlan(compiled)
	if err := compiled.Validate(guestIDs); err != nil {
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Compiled plan failed validation: %v", err))
		o.recordAudit(ctx, "channel.execute_validate_failed", "", strPtr("execute"))
		return
	}

	if planPath == "" {
		// Inline-only mode: we got markdown from the message but had no
		// persisted draft. v1 does not persist inline-only compiles; surface
		// a clear message instead of broadcasting an artifact we can't
		// approve later.
		o.sendFollowUp(ctx, msg,
			"Inline ExecutionPlan compile is not yet supported. Save the plan in Plan mode first, then retry Execute.")
		return
	}

	// Embed the planGeneration the sidecar will claim after the bump so
	// LoadCompiled can detect drift after a partial rename failure.
	plan, err := o.planStore.Load(planPath)
	if err != nil {
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to reload plan for compile: %v", err))
		return
	}
	compiled.Metadata.PlanGeneration = plan.Frontmatter.CompileGeneration + 1

	body, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to serialize compiled plan: %v", err))
		return
	}

	newGen, err := o.planStore.SaveCompiled(planPath, body, requiredProfiles)
	if err != nil {
		o.logger.Warn("execute: save compiled sidecar", "error", err, "plan_path", planPath)
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to save compiled plan: %v", err))
		return
	}

	o.broadcastExecPlanArtifact(ctx, msg, planID, newGen, compiled, body, source, requiredProfiles)
	o.recordAudit(ctx, "channel.execute_plan_compiled", "", strPtr("execute"))
}

// resolveExecuteMarkdownSource enforces inline-beats-draft precedence.
// Returns (markdown, planPath, planID, source) where source is "inline",
// "draft", or "" if neither was available. planPath/planID are empty for
// inline-only markdown that has never been saved.
func (o *Orchestrator) resolveExecuteMarkdownSource(text string) (string, string, string, string) {
	if inline := extractPlanBlock(text); inline != "" {
		// When the inline block matches a current draft, we still prefer
		// inline but reuse the draft's stable path/ID so upsert works.
		if o.planStore != nil {
			if draft, _ := o.planStore.LatestDraft(o.projectSlug()); draft != nil {
				return inline, draft.Path, draft.Frontmatter.ID, "inline"
			}
		}
		return inline, "", "", "inline"
	}
	if o.planStore == nil {
		return "", "", "", ""
	}
	draft, err := o.planStore.LatestDraft(o.projectSlug())
	if err != nil || draft == nil {
		return "", "", "", ""
	}
	return draft.Body, draft.Path, draft.Frontmatter.ID, "draft"
}

// activeGuestProfiles snapshots the currently-indexed guest agents into the
// CompilePlanInput shape. The snapshot is intentionally taken at compile
// time, NOT approve time: Approval will re-validate against the same IDs
// recorded in Plan.Frontmatter.RequiredProfiles so a roster change between
// compile and approve is explicitly rejected instead of silently dispatched.
func (o *Orchestrator) activeGuestProfiles() []GuestProfile {
	out := make([]GuestProfile, 0)
	if o.guestIndex != nil {
		for id, g := range o.guestIndex.Agents {
			if id == guests.OrchestratorGuestID {
				// ScanDirectory excludes orchestrator.md from the guest
				// index in production; a test harness may still seed it.
				// Drop the index copy here so the synthetic entry below
				// (sourced from agents/orchestrator.md frontmatter) wins
				// and both code paths see the same shape.
				continue
			}
			out = append(out, GuestProfile{
				ID:          id,
				Name:        g.Name,
				Profile:     g.Role,
				Description: g.Description,
			})
		}
	}

	// Always append the orchestrator as a first-class roster entry. The
	// guest scan intentionally excludes orchestrator.md so the
	// orchestrator can't accidentally be invited/removed via the guest
	// lifecycle, but the compile LLM must still see it so plan authors
	// can route coordination and review steps to "Tower" (or whatever the
	// project names the orchestrator) without the MissingProfile alias
	// recovery path kicking in every compile. When orchestrator.md is
	// missing we still emit a synthetic entry with Tower-flavored
	// defaults so fresh projects compile plans that mention "Tower".
	orchEntry := GuestProfile{
		ID:      guests.OrchestratorGuestID,
		Profile: guests.OrchestratorGuestID,
	}
	if orchAgent, err := guests.ReadOrchestratorAgent(o.guestsDir()); err == nil {
		orchEntry.Name = orchAgent.Name
		orchEntry.Description = orchAgent.Description
	}
	if orchEntry.Name == "" && o.guestIndex != nil {
		if g, ok := o.guestIndex.Agents[guests.OrchestratorGuestID]; ok {
			orchEntry.Name = g.Name
			if orchEntry.Description == "" {
				orchEntry.Description = g.Description
			}
		}
	}
	if orchEntry.Name == "" {
		orchEntry.Name = "Tower"
	}
	if orchEntry.Description == "" {
		orchEntry.Description = "Project orchestrator. Coordinates guests, writes plans, and handles coordination or review steps that don't require a specialized guest."
	}
	out = append(out, orchEntry)

	// Deterministic ordering so compiler output stays diff-stable between
	// compile and revise even if the map iteration order shuffles.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// resolveGuestAlias tries to map a token the compile LLM produced (either as
// a "missing profile: X" bailout or as a step.agent_id that fails
// Validate) back to a real guest ID. It matches case-insensitively against
// each guest's ID, Name, and Profile fields. The returned string is the
// matched guest's ID when exactly one guest matches across all three
// fields; when zero guests match, or when two or more guests match (for
// example two agents sharing a profile), it returns "". Callers treat ""
// as "no safe rewrite" and surface the existing user-facing error so the
// user can disambiguate explicitly rather than silently routing to the
// wrong guest. The match is on exact-equality of the (trimmed, lowered)
// token — substring matches are intentionally NOT performed because they
// would conflate siblings like "walt" and "walter".
func resolveGuestAlias(token string, guests []GuestProfile) string {
	needle := strings.ToLower(strings.TrimSpace(token))
	if needle == "" {
		return ""
	}
	matches := make(map[string]struct{}, 2)
	for _, g := range guests {
		if strings.ToLower(g.ID) == needle ||
			strings.ToLower(g.Name) == needle ||
			strings.ToLower(g.Profile) == needle {
			matches[g.ID] = struct{}{}
		}
	}
	if len(matches) != 1 {
		return ""
	}
	for id := range matches {
		return id
	}
	return ""
}

// rewriteStepAliases walks plan.Steps and, for each step whose AgentID does
// not already match one of the active guest ids, attempts to resolve the
// AgentID through resolveGuestAlias. On a unique match the step's AgentID
// is rewritten in place to the canonical guest id; on ambiguous or no
// match it is left alone so Validate surfaces the failure. This is a
// second-chance layer on top of the compilePromptSuffix guidance and
// the MissingProfile retry path: even if the LLM emits "Tower" as a
// step.agent_id instead of "orchestrator", the plan still validates and
// dispatches correctly.
func rewriteStepAliases(plan *execute.ExecutionPlan, guests []GuestProfile, logger *slog.Logger) {
	if plan == nil || len(plan.Steps) == 0 || len(guests) == 0 {
		return
	}
	known := make(map[string]struct{}, len(guests))
	for _, g := range guests {
		known[g.ID] = struct{}{}
	}
	for i := range plan.Steps {
		agentID := plan.Steps[i].AgentID
		if agentID == "" {
			continue
		}
		if _, ok := known[agentID]; ok {
			continue
		}
		resolved := resolveGuestAlias(agentID, guests)
		if resolved == "" {
			continue
		}
		if logger != nil {
			logger.Info("execute: rewrote step agent_id alias",
				"step_id", plan.Steps[i].ID,
				"from", agentID,
				"to", resolved,
			)
		}
		plan.Steps[i].AgentID = resolved
	}
}

// requiredProfilesFromPlan extracts the sorted unique set of agent IDs used
// by a compiled plan. This is the roster the plan requires at dispatch
// time; SaveCompiled snapshots it into Frontmatter.RequiredProfiles so
// handleExecutePlanApproval can detect a guest leaving between compile and
// approve without having to re-run the compiler.
func requiredProfilesFromPlan(plan *execute.ExecutionPlan) []string {
	seen := make(map[string]bool, len(plan.Steps))
	for _, s := range plan.Steps {
		if s.AgentID != "" {
			seen[s.AgentID] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// broadcastExecPlanArtifact pushes the compiled plan to Prism as an
// execplan-kind display. The DisplayID ("execplan:<planID>") is stable per
// plan so revisions upsert the same artifact in place, mirroring how
// plan-kind artifacts behave for markdown plans.
func (o *Orchestrator) broadcastExecPlanArtifact(
	ctx context.Context,
	msg channel.IncomingMessage,
	planID string,
	compileGeneration int,
	plan *execute.ExecutionPlan,
	body []byte,
	source string,
	requiredProfiles []string,
) {
	if o.channelMgr == nil {
		return
	}
	// requiredProfiles is a defensive copy: the Prism preview renders
	// these as "Guests involved" so the user knows exactly which agents
	// will activate if they click Approve. We deliberately sort and
	// deduplicate upstream in requiredProfilesFromPlan so the UI can show
	// them in a stable order.
	profiles := append([]string(nil), requiredProfiles...)
	component := map[string]any{
		"kind":              "execplan",
		"planId":            planID,
		"compileGeneration": compileGeneration,
		"stepCount":         len(plan.Steps),
		"gateCount":         len(plan.Gates),
		"title":             plan.Metadata.Title,
		"description":       plan.Metadata.Description,
		"body":              string(body),
		"sourceMode":        source,
		"requiredProfiles":  profiles,
	}
	displayID := "execplan:" + planID
	_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
		Display:   component,
		DisplayID: displayID,
		ThreadID:  msg.ThreadID,
	})
}

// shortHashString returns the first 12 hex chars of sha256(text), used as a
// cheap stable key for the per-inline-content compile mutex. Two identical
// inline submissions produce the same mutex key so they serialize; two
// different inline submissions collide with probability ~2^-48, good enough
// for a runtime de-duplication guard.
func shortHashString(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:6])
}

// handleExecutePlanApproval handles a [gate:<action>:execplan:<id>:<gen>]
// message. It is the sole dispatch path for compiled plans in v1 — typed
// approve/go/dispatch control text is NOT recognized, so the Prism UI is
// the only way to advance an ExecutionPlan from "compiled" to "running."
//
// Rejections:
//   - Stale compileGeneration (user clicked Approve on a revised artifact).
//   - Rolled-back frontmatter (gen regressed relative to sidecar).
//   - Sidecar drift (partial SaveCompiled, see plans.ErrCompiledSidecarDrift).
//   - Guest roster changed since compile (a required profile has left).
func (o *Orchestrator) handleExecutePlanApproval(ctx context.Context, msg channel.IncomingMessage) {
	gate, ok := parseExecPlanGate(msg.Text)
	if !ok {
		o.sendFollowUp(ctx, msg, "Malformed execute-plan gate tag.")
		return
	}
	if o.planStore == nil {
		o.sendFollowUp(ctx, msg, "Plan store unavailable; cannot resolve plan gate.")
		return
	}
	if gate.Action == execute.GateAbort {
		o.sendFollowUp(ctx, msg, "Plan execution aborted.")
		o.recordAudit(ctx, "channel.execute_plan_aborted", "", strPtr("execute"))
		return
	}

	planPath, err := o.findPlanByID(gate.PlanID)
	if err != nil {
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Plan %q not found: %v", gate.PlanID, err))
		return
	}
	plan, err := o.planStore.Load(planPath)
	if err != nil {
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to load plan: %v", err))
		return
	}

	if gate.CompileGeneration != plan.Frontmatter.CompileGeneration {
		o.sendFollowUp(ctx, msg, fmt.Sprintf(
			"This compiled plan is no longer current (clicked generation %d, latest is %d). "+
				"Re-open the plan in Prism and try again.",
			gate.CompileGeneration, plan.Frontmatter.CompileGeneration))
		o.recordAudit(ctx, "channel.execute_stale_approval", "", strPtr("execute"))
		return
	}

	body, embedded, err := o.planStore.LoadCompiled(planPath)
	if errors.Is(err, plans.ErrCompiledSidecarDrift) {
		o.sendFollowUp(ctx, msg, fmt.Sprintf(
			"Compiled sidecar drifted (frontmatter gen=%d, sidecar gen=%d). "+
				"Please recompile the plan before approving.",
			plan.Frontmatter.CompileGeneration, embedded))
		o.recordAudit(ctx, "channel.execute_sidecar_drift", "", strPtr("execute"))
		return
	}
	if err != nil {
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to load compiled plan: %v", err))
		return
	}

	if gate.Action == execute.GateRevise {
		go o.handleExecuteMarkdown(ctx, msg, gate.Feedback)
		return
	}

	// Action == approve. Re-validate roster against the snapshot taken at
	// compile time: if a required profile has left the room, reject
	// instead of dispatching a broken plan.
	current := o.activeGuestProfiles()
	currentIDs := make(map[string]bool, len(current))
	for _, g := range current {
		currentIDs[g.ID] = true
	}
	for _, req := range plan.Frontmatter.RequiredProfiles {
		if !currentIDs[req] {
			o.sendFollowUp(ctx, msg, fmt.Sprintf(
				"Cannot dispatch: required guest %q is no longer active. "+
					"Re-invite the guest and recompile the plan, or revise it.", req))
			o.recordAudit(ctx, "channel.execute_guest_set_changed", "", strPtr("execute"))
			return
		}
	}

	var compiled execute.ExecutionPlan
	if err := json.Unmarshal(body, &compiled); err != nil {
		o.sendFollowUp(ctx, msg, fmt.Sprintf("Failed to parse compiled plan: %v", err))
		return
	}

	// Apply the UI-side skip list BEFORE handoff so PlanRunner never sees
	// gates the user toggled off in the preview. Unknown IDs are ignored
	// (a gate may have been compiled away in a revise round-trip between
	// preview and dispatch — identical observable effect).
	if len(gate.SkippedGateIDs) > 0 {
		before := len(compiled.Gates)
		compiled.Gates = filterExecPlanGates(compiled.Gates, gate.SkippedGateIDs)
		if removed := before - len(compiled.Gates); removed > 0 {
			o.recordAudit(ctx, "channel.execute_gates_skipped",
				strings.Join(gate.SkippedGateIDs, ","), strPtr("execute"))
			o.logger.Info("execute: dropped gates before dispatch",
				"plan_id", gate.PlanID,
				"skipped_ids", gate.SkippedGateIDs,
				"removed", removed)
		}
	}

	// Plan->Execute roster handoff. Any guests who were active during Plan
	// mode (contributing plan-edit blocks, answering questions) are cleared
	// here BEFORE PlanRunner starts. PlanRunner is then the sole activator
	// of guests for the duration of execution: it activates each step's
	// AgentID on step start and deactivates on gate approve/abort. This
	// closes the "who's working vs who's watching" ambiguity — after
	// Approve, the Prism roster is empty and the only chip that appears
	// belongs to the step currently running.
	//
	// Any code that spawns PlanRunner MUST broadcast the deactivation
	// frames first. Breaking this ordering re-introduces mixed-roster UI
	// state (plan-time participants sitting next to PlanRunner step-owners)
	// which is exactly the ambiguity we locked this invariant in to
	// prevent.
	o.clearPlanTimeRosterForExecute(ctx, msg)

	o.handleExecuteMessageWithPlan(ctx, msg, &compiled, planPath, plan.Frontmatter.ID, plan.Frontmatter.CompileGeneration)
	o.recordAudit(ctx, "channel.execute_plan_dispatched", "", strPtr("execute"))
}

// clearPlanTimeRosterForExecute broadcasts a DeactivateGuest frame for every
// guest in msg.ActiveGuests so the Prism roster is empty at the moment
// PlanRunner begins. This is the Plan->Execute handoff invariant: after
// Approve on the execplan artifact, only PlanRunner may activate guests.
//
// Ordering guarantee: every DeactivateGuest broadcast completes before
// this function returns, which means the subsequent PlanRunner.Run
// ActivateGuest frames will appear strictly after all deactivations on the
// wire. Prism relies on this ordering to avoid a "Kaplan re-joins after
// Miyazaki" flash in the roster sidebar.
func (o *Orchestrator) clearPlanTimeRosterForExecute(ctx context.Context, msg channel.IncomingMessage) {
	if o.channelMgr == nil || len(msg.ActiveGuests) == 0 {
		return
	}
	const reason = "plan approved, entering execute"
	for _, guestID := range msg.ActiveGuests {
		if guestID == "" {
			continue
		}
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			DeactivateGuest: &channel.DeactivateGuest{
				ID:     guestID,
				Reason: reason,
			},
		})
	}
}

// findPlanByID scans the store for a plan with the given stable ID. The
// project slug is resolved from the current Orchestrator context so
// cross-project plan IDs cannot be approved from the wrong runtime.
func (o *Orchestrator) findPlanByID(planID string) (string, error) {
	if o.planStore == nil {
		return "", fmt.Errorf("plan store unavailable")
	}
	metas, err := o.planStore.List(o.projectSlug())
	if err != nil {
		return "", err
	}
	for _, m := range metas {
		if m.ID == planID {
			return m.Path, nil
		}
	}
	return "", fmt.Errorf("no plan with id %q", planID)
}
