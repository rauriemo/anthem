package orchestrator

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

// ParseContextReport extracts the last {"context_report": ...} JSON block from
// agent output. Returns nil (no error) when no block is found.
func ParseContextReport(output string) (*ContextReport, error) {
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
