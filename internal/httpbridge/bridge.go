package httpbridge

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rauriemo/anthem/internal/guests"
)

// ToolConfig pairs a tool ID with its HTTP config for the bridge.
type ToolConfig struct {
	ID     string
	Config guests.HTTPToolConfig
}

// Run starts the MCP stdio server loop. It reads JSON-RPC 2.0 requests from
// r and writes responses to w. The bridge exposes one MCP tool per ToolConfig.
func Run(tools []ToolConfig, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeError(w, nil, -32700, "parse error")
			continue
		}

		switch req.Method {
		case "initialize":
			writeResult(w, req.ID, initializeResult())
		case "notifications/initialized":
			// Client notification, no response needed
		case "tools/list":
			writeResult(w, req.ID, toolsList(tools))
		case "tools/call":
			handleToolCall(w, req.ID, req.Params, tools)
		default:
			writeError(w, req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
		}
	}
	return scanner.Err()
}

// LoadConfigFromEnv reads ANTHEM_HTTP_BRIDGE_CONFIG (base64-encoded JSON map
// of tool ID -> HTTPToolConfig) from the environment.
func LoadConfigFromEnv() ([]ToolConfig, error) {
	encoded := os.Getenv("ANTHEM_HTTP_BRIDGE_CONFIG")
	if encoded == "" {
		return nil, fmt.Errorf("ANTHEM_HTTP_BRIDGE_CONFIG not set")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decoding bridge config: %w", err)
	}
	var raw map[string]guests.HTTPToolConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing bridge config: %w", err)
	}
	var tools []ToolConfig
	for id, cfg := range raw {
		tools = append(tools, ToolConfig{ID: id, Config: cfg})
	}
	return tools, nil
}

// JSON-RPC 2.0 types

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeResult(w io.Writer, id json.RawMessage, result any) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "%s\n", data)
}

func writeError(w io.Writer, id json.RawMessage, code int, msg string) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: msg}}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "%s\n", data)
}

// MCP protocol responses

func initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "anthem-http-bridge",
			"version": "1.0.0",
		},
	}
}

func toolsList(tools []ToolConfig) map[string]any {
	list := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		inputVars := ExtractInputVars(t.Config.RequestTemplate)
		properties := make(map[string]any, len(inputVars))
		required := make([]string, 0, len(inputVars))
		for _, v := range inputVars {
			properties[v] = map[string]any{"type": "string"}
			required = append(required, v)
		}

		// filename is always available for artifact save_to resolution
		if t.Config.ResponseArtifact != nil {
			if _, exists := properties["filename"]; !exists {
				if strings.Contains(t.Config.ResponseArtifact.SaveTo, "${input.filename}") {
					properties["filename"] = map[string]any{
						"type":        "string",
						"description": "Output filename for the generated artifact",
					}
					required = append(required, "filename")
				}
			}
		}

		desc := t.Config.Description
		if desc == "" {
			desc = fmt.Sprintf("HTTP %s %s", t.Config.Method, t.Config.URL)
		}

		list = append(list, map[string]any{
			"name":        fmt.Sprintf("http__%s__call", t.ID),
			"description": desc,
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
		})
	}
	return map[string]any{"tools": list}
}

func handleToolCall(w io.Writer, id json.RawMessage, params json.RawMessage, tools []ToolConfig) {
	var call struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		writeError(w, id, -32602, "invalid params")
		return
	}

	var tool *ToolConfig
	for i := range tools {
		if fmt.Sprintf("http__%s__call", tools[i].ID) == call.Name {
			tool = &tools[i]
			break
		}
	}
	if tool == nil {
		writeError(w, id, -32602, fmt.Sprintf("unknown tool: %s", call.Name))
		return
	}

	result, err := executeHTTPTool(tool.Config, call.Arguments)
	if err != nil {
		writeResult(w, id, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("Error: %s", err)},
			},
			"isError": true,
		})
		return
	}

	writeResult(w, id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": result},
		},
	})
}

func executeHTTPTool(cfg guests.HTTPToolConfig, args map[string]string) (string, error) {
	body, err := ResolveTemplate(cfg.RequestTemplate, args)
	if err != nil {
		return "", fmt.Errorf("resolving request template: %w", err)
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling request body: %w", err)
	}

	req, err := http.NewRequest(cfg.Method, cfg.URL, bytes.NewReader(bodyJSON))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if cfg.AuthTokenEnv != "" {
		token := os.Getenv(cfg.AuthTokenEnv)
		if token != "" {
			switch cfg.AuthScheme {
			case "bearer":
				req.Header.Set("Authorization", "Bearer "+token)
			case "api-key":
				req.Header.Set("x-goog-api-key", token)
			default:
				req.Header.Set("Authorization", "Bearer "+token)
			}
		}
	}

	timeout := 60 * time.Second
	if cfg.TimeoutMS > 0 {
		timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	if cfg.ResponseArtifact != nil {
		return handleArtifactResponse(cfg.ResponseArtifact, respBody, args)
	}

	return truncate(string(respBody), 4000), nil
}

// handleArtifactResponse extracts base64 image data from a Gemini-style response,
// decodes it, and writes it to the save_to path.
func handleArtifactResponse(artifact *guests.ArtifactTemplate, respBody []byte, args map[string]string) (string, error) {
	savePath, err := resolveString(artifact.SaveTo, args)
	if err != nil {
		return "", fmt.Errorf("resolving save_to path: %w", err)
	}

	b64Data, mimeType, err := extractInlineData(respBody)
	if err != nil {
		return "", fmt.Errorf("extracting image data: %w", err)
	}

	imageBytes, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return "", fmt.Errorf("decoding base64 image: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return "", fmt.Errorf("creating output directory: %w", err)
	}
	if err := os.WriteFile(savePath, imageBytes, 0644); err != nil {
		return "", fmt.Errorf("writing artifact: %w", err)
	}

	return fmt.Sprintf("Saved %s (%s, %d bytes) to %s", artifact.Type, mimeType, len(imageBytes), savePath), nil
}

// extractInlineData finds the first inlineData part in a Gemini API response.
func extractInlineData(body []byte) (b64 string, mimeType string, err error) {
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", fmt.Errorf("parsing Gemini response: %w", err)
	}
	for _, c := range resp.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData != nil && p.InlineData.Data != "" {
				return p.InlineData.Data, p.InlineData.MimeType, nil
			}
		}
	}
	return "", "", fmt.Errorf("no inlineData found in response")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
