package claude

import (
	"encoding/json"
)

// StreamEvent represents a single event from Claude Code's stream-json output.
type StreamEvent struct {
	Type      string          `json:"type"`
	SubType   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Content   string          `json:"content,omitempty"`
	// Event is set for type "stream_event" when using --include-partial-messages.
	Event *struct {
		Type  string `json:"type,omitempty"` // e.g. content_block_delta
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text,omitempty"`
			PartialJSON string `json:"partial_json,omitempty"`
		} `json:"delta,omitempty"`
	} `json:"event,omitempty"`

	// Result event fields (present when type == "result")
	IsError    bool            `json:"is_error,omitempty"`
	DurationMS int             `json:"duration_ms,omitempty"`
	NumTurns   int             `json:"num_turns,omitempty"`
	ResultText json.RawMessage `json:"result,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	TotalCost  float64         `json:"total_cost_usd,omitempty"`
	Usage      *UsageData      `json:"usage,omitempty"`
}

// UsageData contains token usage from a result event.
type UsageData struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ParseStreamEvent parses a single line of stream-json output.
func ParseStreamEvent(line []byte) (*StreamEvent, error) {
	line = trimBOM(line)
	if len(line) == 0 {
		return nil, nil
	}

	var event StreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// StreamEventStreamingPayload returns incremental assistant text from a stream_event line, if any.
// Matches content_block_delta + text_delta and input_json_delta + partial_json shapes from stream-json.
func StreamEventStreamingPayload(e *StreamEvent) string {
	if e == nil || e.Type != "stream_event" || e.Event == nil || e.Event.Delta == nil {
		return ""
	}
	d := e.Event.Delta
	if d.Text != "" {
		return d.Text
	}
	if d.PartialJSON != "" {
		return d.PartialJSON
	}
	return ""
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
