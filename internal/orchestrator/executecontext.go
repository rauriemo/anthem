package orchestrator

import (
	"time"

	"github.com/rauriemo/anthem/internal/prompt"
)

// executeContextSources is the adapter that lets the execute runner
// satisfy prompt.ContextSources by reading through the live
// orchestrator state (VOICE.md cache, project summary, shared context
// maps, conversation buffer) rather than a snapshot captured at
// run-start. Stored in a dedicated file so the orchestrator body stays
// focused on the chat/plan dispatch path; only NewPlanRunner in
// orchestrator.go touches this factory.
//
// The adapter holds a pointer back to the orchestrator so every method
// re-queries under the orchestrator mutex. Implementations intentionally
// avoid caching -- the whole point of plan decision D1 is that each
// step sees the freshest possible context, including side effects from
// upstream steps that just finished.
type executeContextSources struct {
	orch *Orchestrator
}

// ExecuteContextSources returns a prompt.ContextSources backed by this
// orchestrator. Exposed so execute.NewPlanRunner can be constructed in
// orchestrator.go without reaching into private fields; also used by
// tests that want to exercise the adapter without spinning up a full
// PlanRunner.
func (o *Orchestrator) ExecuteContextSources() prompt.ContextSources {
	if o == nil {
		return nil
	}
	return &executeContextSources{orch: o}
}

func (s *executeContextSources) UserContext() string {
	if s == nil || s.orch == nil {
		return ""
	}
	s.orch.mu.Lock()
	defer s.orch.mu.Unlock()
	return s.orch.voiceContent
}

func (s *executeContextSources) ProjectSummary() string {
	if s == nil || s.orch == nil {
		return ""
	}
	s.orch.mu.Lock()
	defer s.orch.mu.Unlock()
	if s.orch.projectCtx == nil {
		return ""
	}
	return s.orch.projectCtx.ProjectSummary
}

func (s *executeContextSources) FeatureContext(feature string, upstreamArtifactIDs []string) string {
	if s == nil || s.orch == nil || feature == "" {
		return ""
	}
	if len(upstreamArtifactIDs) == 0 {
		text, err := HydrateFeatureContext(s.orch.projectRoot(), feature)
		if err != nil {
			return ""
		}
		return text
	}
	text, err := HydrateFeatureContextForStep(s.orch.projectRoot(), feature, upstreamArtifactIDs)
	if err != nil {
		return ""
	}
	return text
}

func (s *executeContextSources) SharedContextText(channelKey string) string {
	if s == nil || s.orch == nil || channelKey == "" {
		return ""
	}
	if s.orch.sharedCtx == nil {
		return ""
	}
	return s.orch.sharedCtx.Get(channelKey)
}

// HistoryText returns the formatted conversation history for the
// given channel key, filtered to rounds whose CreatedAt is strictly
// before the cutoff. Passing the zero value disables filtering so
// chat dispatch sees the entire buffer. Chained-run dispatch passes
// the plan's RunStartedAt so downstream steps don't accidentally
// pick up user chat that landed mid-run (plan decision D12).
func (s *executeContextSources) HistoryText(channelKey string, before time.Time) string {
	if s == nil || s.orch == nil || channelKey == "" {
		return ""
	}
	if s.orch.convoBuf == nil {
		return ""
	}
	return FormatHistory(s.orch.convoBuf.HistoryBefore(channelKey, before))
}
