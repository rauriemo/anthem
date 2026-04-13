package voice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
	fastLLMModel        = "claude-3-5-haiku-latest"
	maxHistoryTurns     = 10
	maxTokensVoice      = 300
)

// StreamEvent carries a single incremental event from the LLM stream.
type StreamEvent struct {
	TextDelta string
	ToolUse   *ToolCall
	Done      bool
}

// ToolCall represents a tool_use block returned by the model.
type ToolCall struct {
	Name  string
	Input json.RawMessage
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type toolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// FastLLM is a lightweight Anthropic Messages API client optimized for
// voice latency. It keeps a short conversation history and streams
// responses directly, bypassing the Claude CLI subprocess.
type FastLLM struct {
	apiKey  string
	model   string
	system  string
	tools   []toolDef
	logger  *slog.Logger
	client  *http.Client
	baseURL string

	mu      sync.Mutex
	history []chatMessage
}

// FastLLMOpts configures a new FastLLM instance.
type FastLLMOpts struct {
	APIKey       string
	SystemPrompt string
	Tools        []toolDef
	Logger       *slog.Logger
	HTTPClient   *http.Client
	BaseURL      string
}

// NewFastLLM creates a direct Anthropic API client for voice.
func NewFastLLM(opts FastLLMOpts) *FastLLM {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	base := opts.BaseURL
	if base == "" {
		base = anthropicAPIURL
	}
	return &FastLLM{
		apiKey:  opts.APIKey,
		model:   fastLLMModel,
		system:  opts.SystemPrompt,
		tools:   opts.Tools,
		logger:  opts.Logger,
		client:  opts.HTTPClient,
		baseURL: base,
	}
}

// HistoryLen returns the current number of turns in conversation history.
func (f *FastLLM) HistoryLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.history)
}

// StreamChat sends a user message and returns a channel of streaming events.
// Text deltas arrive incrementally for TTS piping. Tool calls arrive as
// complete events after the tool_use block is fully streamed.
func (f *FastLLM) StreamChat(ctx context.Context, userText string) (<-chan StreamEvent, error) {
	f.mu.Lock()
	f.history = append(f.history, chatMessage{Role: "user", Content: userText})
	if len(f.history) > maxHistoryTurns*2 {
		f.history = f.history[2:]
	}
	messages := make([]chatMessage, len(f.history))
	copy(messages, f.history)
	f.mu.Unlock()

	body := map[string]any{
		"model":      f.model,
		"max_tokens": maxTokensVoice,
		"stream":     true,
		"messages":   messages,
	}
	if f.system != "" {
		body["system"] = f.system
	}
	if len(f.tools) > 0 {
		body["tools"] = f.tools
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("fastllm: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("fastllm: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", f.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fastllm: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("fastllm: API returned %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamEvent, 64)
	go f.readSSE(ctx, resp.Body, ch)
	return ch, nil
}

func (f *FastLLM) readSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer body.Close()
	defer close(ch)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)

	var currentToolName string
	var toolInputBuf strings.Builder
	var assistantText strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type         string `json:"type"`
			ContentBlock *struct {
				Type  string `json:"type"`
				Name  string `json:"name,omitempty"`
				Text  string `json:"text,omitempty"`
				ID    string `json:"id,omitempty"`
				Input any    `json:"input,omitempty"`
			} `json:"content_block,omitempty"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text,omitempty"`
				PartialJSON string `json:"partial_json,omitempty"`
			} `json:"delta,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			f.logger.Warn("fastllm: failed to parse SSE event", "error", err, "data", data)
			continue
		}

		switch event.Type {
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				currentToolName = event.ContentBlock.Name
				toolInputBuf.Reset()
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				assistantText.WriteString(event.Delta.Text)
				select {
				case ch <- StreamEvent{TextDelta: event.Delta.Text}:
				case <-ctx.Done():
					return
				}
			case "input_json_delta":
				toolInputBuf.WriteString(event.Delta.PartialJSON)
			}

		case "content_block_stop":
			if currentToolName != "" {
				inputJSON := toolInputBuf.String()
				if inputJSON == "" {
					inputJSON = "{}"
				}
				select {
				case ch <- StreamEvent{ToolUse: &ToolCall{
					Name:  currentToolName,
					Input: json.RawMessage(inputJSON),
				}}:
				case <-ctx.Done():
					return
				}
				currentToolName = ""
				toolInputBuf.Reset()
			}

		case "message_stop":
			f.mu.Lock()
			if text := assistantText.String(); text != "" {
				f.history = append(f.history, chatMessage{Role: "assistant", Content: text})
				if len(f.history) > maxHistoryTurns*2 {
					f.history = f.history[2:]
				}
			}
			f.mu.Unlock()

			select {
			case ch <- StreamEvent{Done: true}:
			case <-ctx.Done():
			}
			return
		}
	}
}

// VoiceToolDefs returns the standard tool definitions for voice commands.
func VoiceToolDefs() []toolDef {
	return []toolDef{
		{
			Name:        "invite_agent",
			Description: "Invite a specialist agent to join the project and help with a task",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"agent_name": {"type": "string", "description": "Name of the agent to invite (e.g. noah, forge, eiji)"},
					"task_description": {"type": "string", "description": "What you want the agent to help with"}
				},
				"required": ["agent_name", "task_description"]
			}`),
		},
		{
			Name:        "delegate_task",
			Description: "Send a specific task or instruction to an agent that is already active",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"agent_name": {"type": "string", "description": "Name of the target agent"},
					"instruction": {"type": "string", "description": "The task or instruction to send"}
				},
				"required": ["agent_name", "instruction"]
			}`),
		},
		{
			Name:        "check_status",
			Description: "Check the current status of agents, tasks, or the project",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"scope": {"type": "string", "enum": ["project", "agent", "task"], "description": "What to check status of"},
					"name": {"type": "string", "description": "Optional: specific agent or task name"}
				},
				"required": ["scope"]
			}`),
		},
	}
}
