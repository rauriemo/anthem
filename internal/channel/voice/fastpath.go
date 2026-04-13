package voice

import "context"

// FastResponse is the result of a fast-path response attempt.
type FastResponse struct {
	Text     string
	Handled  bool // true if the fast path fully handled the turn
	AudioPCM []byte
}

// FastResponder is an optional accelerator for ultra-fast micro-responses.
// It sits in front of the orchestrator and handles simple conversational turns
// (acknowledgments, clarifications, yes/no) with ~200-300ms latency.
//
// If the fast responder can handle the turn, it returns Handled=true and the
// Voice Gateway speaks the response immediately without routing to the orchestrator.
// If it cannot handle the turn, it returns Handled=false and the normal pipeline runs.
//
// Implementations may include:
//   - Gemini Live session for instant audio-to-audio responses
//   - OpenAI Realtime API for conversational bridges
//   - A simple rule-based responder for acknowledgments
//
// The interface is intentionally minimal: the Voice Gateway owns floor control
// and Anthem owns routing. The fast responder is purely an optimization.
type FastResponder interface {
	Start(ctx context.Context) error

	// TryRespond attempts to handle a user transcript with a fast response.
	// Returns a FastResponse. If Handled is false, the caller should fall
	// through to the normal orchestrator pipeline.
	TryRespond(ctx context.Context, transcript string) (FastResponse, error)

	Close() error
}

// NullFastResponder is a no-op implementation that always falls through.
// Used when no fast-path model is configured.
type NullFastResponder struct{}

func (n *NullFastResponder) Start(context.Context) error { return nil }

func (n *NullFastResponder) TryRespond(_ context.Context, _ string) (FastResponse, error) {
	return FastResponse{Handled: false}, nil
}

func (n *NullFastResponder) Close() error { return nil }

// RuleBasedFastResponder handles simple conversational patterns without
// requiring an external model. Good for quick acknowledgments.
type RuleBasedFastResponder struct {
	rules []fastRule
}

type fastRule struct {
	patterns []string
	response string
}

// NewRuleBasedFastResponder creates a responder with default conversational rules.
func NewRuleBasedFastResponder() *RuleBasedFastResponder {
	return &RuleBasedFastResponder{
		rules: []fastRule{
			{patterns: []string{"yes", "yeah", "yep", "sure", "ok", "okay"}, response: "Got it."},
			{patterns: []string{"no", "nope", "nah"}, response: "Understood."},
			{patterns: []string{"thanks", "thank you", "cheers"}, response: "You're welcome."},
			{patterns: []string{"hello", "hi", "hey"}, response: "Hey there."},
			{patterns: []string{"go ahead", "proceed", "continue"}, response: "On it."},
		},
	}
}

func (r *RuleBasedFastResponder) Start(context.Context) error { return nil }

func (r *RuleBasedFastResponder) TryRespond(_ context.Context, transcript string) (FastResponse, error) {
	normalized := normalizeForMatch(transcript)
	for _, rule := range r.rules {
		for _, p := range rule.patterns {
			if normalized == p {
				return FastResponse{
					Text:    rule.response,
					Handled: true,
				}, nil
			}
		}
	}
	return FastResponse{Handled: false}, nil
}

func (r *RuleBasedFastResponder) Close() error { return nil }

func normalizeForMatch(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == ' ' {
			result = append(result, c)
		}
	}
	// Trim leading/trailing spaces
	start, end := 0, len(result)-1
	for start <= end && result[start] == ' ' {
		start++
	}
	for end >= start && result[end] == ' ' {
		end--
	}
	if start > end {
		return ""
	}
	return string(result[start : end+1])
}
