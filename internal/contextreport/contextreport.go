// Package contextreport parses the optional structured report an agent may
// append to its final message (a JSON block keyed on "context_report").
// It is a shared peer of both internal/orchestrator and internal/execute so
// downstream packages can read reports without introducing an import cycle
// with orchestrator.
package contextreport

import (
	"encoding/json"
	"strings"
)

// ContextReport is the structured report an agent may emit at the end of its
// response. The parser extracts the last {"context_report": ...} block.
type ContextReport struct {
	Action    string          `json:"action"`
	Summary   string          `json:"summary"`
	Artifact  *ReportArtifact `json:"artifact,omitempty"`
	Progress  string          `json:"progress,omitempty"`
	BlockedOn string          `json:"blocked_on,omitempty"`
	Produces  []string        `json:"produces,omitempty"`
}

// ReportArtifact is the artifact sub-object within a context_report.
//
// Origin-bearing keys (origin, plan_id, run_id, step_id, agent_id,
// superseded_at) are deliberately NOT modeled: plan decision D13
// requires the origin block to be stamped only by the featurewriter
// call site, never by the agent. json.Unmarshal drops unknown keys
// silently, so any origin-shaped fields a well-meaning or malicious
// agent emits are discarded here before they ever reach
// ArtifactEntry. The writer then stamps the authoritative origin from
// the call-site tag (ExecuteOrigin / ChatOrigin) so there is exactly
// one source of truth for provenance.
type ReportArtifact struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Path        string            `json:"path"`
	Description string            `json:"description,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	DependsOn   []string          `json:"depends_on,omitempty"`
	Status      string            `json:"status,omitempty"`
}

type contextReportWrapper struct {
	ContextReport ContextReport `json:"context_report"`
}

// Parse extracts the last {"context_report": ...} JSON block from agent
// output. Returns nil (no error) when no block is found.
func Parse(output string) (*ContextReport, error) {
	block := extractLastContextReportJSON(output)
	if block == "" {
		return nil, nil
	}

	var wrapper contextReportWrapper
	if err := json.Unmarshal([]byte(block), &wrapper); err != nil {
		return nil, err
	}

	if wrapper.ContextReport.Action == "" && wrapper.ContextReport.Summary == "" {
		return nil, nil
	}

	return &wrapper.ContextReport, nil
}

// extractLastContextReportJSON finds the last top-level JSON object in the
// output that contains a "context_report" key. It uses brace-matching to
// handle nested objects and ignores JSON inside fenced code blocks.
func extractLastContextReportJSON(s string) string {
	var lastMatch string
	inFence := false
	i := 0
	for i < len(s) {
		if i+2 < len(s) && s[i] == '`' && s[i+1] == '`' && s[i+2] == '`' {
			inFence = !inFence
			i += 3
			continue
		}

		if inFence {
			i++
			continue
		}

		if s[i] != '{' {
			i++
			continue
		}

		depth := 0
		start := i
		for j := start; j < len(s); j++ {
			switch s[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					candidate := s[start : j+1]
					if strings.Contains(candidate, `"context_report"`) {
						lastMatch = candidate
					}
					i = j + 1
					goto next
				}
			}
		}
		i = len(s)
	next:
	}

	return lastMatch
}
