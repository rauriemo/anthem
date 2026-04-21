package httpbridge

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

// LoadRootFromEnv reads ANTHEM_HTTP_BRIDGE_ROOT from the environment and
// canonicalizes it. Falls back to os.Getwd() if unset.
func LoadRootFromEnv() (string, error) {
	root := os.Getenv("ANTHEM_HTTP_BRIDGE_ROOT")
	if root == "" {
		return os.Getwd()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving bridge root: %w", err)
	}
	return abs, nil
}

// Run starts the MCP stdio server loop. It reads JSON-RPC 2.0 requests from
// r and writes responses to w. sandboxRoot is the canonical project root used
// to sandbox file reads for type:file input_types.
func Run(tools []ToolConfig, sandboxRoot string, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024)

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
			handleToolCall(w, req.ID, req.Params, tools, sandboxRoot)
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
		requiredVars, optionalVars := ExtractAllInputVars(t.Config.RequestTemplate)
		properties := make(map[string]any, len(requiredVars)+len(optionalVars))
		required := make([]string, 0, len(requiredVars))
		describeVar := func(v string, isOptional bool) map[string]any {
			prop := map[string]any{"type": "string"}
			if spec, ok := t.Config.InputTypes[v]; ok && spec.Type == "file" {
				maxMB := spec.MaxSizeMB
				if maxMB == 0 {
					maxMB = 10
				}
				enc := spec.Encoding
				if enc == "" {
					enc = "base64"
				}
				prop["description"] = fmt.Sprintf("Relative file path (max %dMB, will be %s-encoded by the bridge)", maxMB, enc)
			}
			if isOptional {
				existing, _ := prop["description"].(string)
				suffix := "Optional; a YAML-declared default is used when omitted."
				if existing != "" {
					prop["description"] = existing + " " + suffix
				} else {
					prop["description"] = suffix
				}
			}
			return prop
		}
		for _, v := range requiredVars {
			properties[v] = describeVar(v, false)
			required = append(required, v)
		}
		for _, v := range optionalVars {
			properties[v] = describeVar(v, true)
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

func handleToolCall(w io.Writer, id json.RawMessage, params json.RawMessage, tools []ToolConfig, sandboxRoot string) {
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

	result, err := executeHTTPTool(tool.Config, call.Arguments, sandboxRoot)
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

func executeHTTPTool(cfg guests.HTTPToolConfig, args map[string]string, sandboxRoot string) (string, error) {
	resolved, err := ResolveInputs(args, cfg.InputTypes, sandboxRoot)
	if err != nil {
		return "", fmt.Errorf("resolving inputs: %w", err)
	}
	body, err := ResolveTemplate(cfg.RequestTemplate, resolved)
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

	if cfg.AsyncPolling != nil && cfg.AsyncPolling.Enabled {
		return executeAsyncPolling(cfg, respBody, args)
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

	msg := fmt.Sprintf("Saved %s (%s, %d bytes) to %s", artifact.Type, mimeType, len(imageBytes), savePath)
	if len(artifact.PostProcess) > 0 {
		ops, err := ResolvePostProcess(artifact.PostProcess, args)
		if err != nil {
			return "", fmt.Errorf("resolving post_process config: %w", err)
		}
		results, state := RunPostProcess(ops, savePath, slog.Default())
		if sp := state["spritesheet_path"]; sp != "" {
			msg = fmt.Sprintf("Saved sprite sheet image/png to %s (from %s)", sp, savePath)
		}
		msg = FormatResults(msg, results)
	}
	return msg, nil
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

// mimeToExt maps MIME types to canonical file extensions.
var mimeToExt = map[string]string{
	"video/mp4":  ".mp4",
	"video/webm": ".webm",
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"audio/mpeg": ".mp3",
	"audio/wav":  ".wav",
}

// validateArtifactFilename ensures the filename has an extension consistent with
// the artifact MIME type. Missing extensions are appended; mismatched extensions
// produce an error.
func validateArtifactFilename(filename, mimeType string) (string, error) {
	expected, known := mimeToExt[mimeType]
	ext := filepath.Ext(filename)

	if ext == "" {
		if !known {
			return filename, nil
		}
		return filename + expected, nil
	}

	if known && !strings.EqualFold(ext, expected) {
		return "", fmt.Errorf("filename extension %q does not match artifact type %s (expected %s)", ext, mimeType, expected)
	}
	return filename, nil
}

// buildAuthHeaders constructs the auth headers for the given tool config.
func buildAuthHeaders(cfg guests.HTTPToolConfig) http.Header {
	h := make(http.Header)
	if cfg.AuthTokenEnv == "" {
		return h
	}
	token := os.Getenv(cfg.AuthTokenEnv)
	if token == "" {
		return h
	}
	switch cfg.AuthScheme {
	case "bearer":
		h.Set("Authorization", "Bearer "+token)
	case "api-key":
		h.Set("x-goog-api-key", token)
	default:
		h.Set("Authorization", "Bearer "+token)
	}
	return h
}

// resolveDownloadAuth returns the headers to use for artifact downloads based on
// the async_polling.download_auth setting.
//   - "inherit" or "" (default): reuse the initial auth headers
//   - "none": no auth headers on download
func resolveDownloadAuth(cfg guests.HTTPToolConfig) http.Header {
	if cfg.AsyncPolling == nil {
		return buildAuthHeaders(cfg)
	}
	switch cfg.AsyncPolling.DownloadAuth {
	case "none":
		return make(http.Header)
	default:
		return buildAuthHeaders(cfg)
	}
}

// deriveBaseURL extracts the API root from a long-running operation URL.
// For "https://host/v1beta/models/m:predictLongRunning" it returns
// "https://host/v1beta" — the portion before the first resource path segment.
// The operation name returned by the API (e.g. "models/m/operations/xyz")
// is appended to this root to form the polling URL.
func deriveBaseURL(rawURL string) string {
	schemeEnd := strings.Index(rawURL, "://")
	if schemeEnd < 0 {
		return rawURL
	}

	// Strip ":method" suffix first (e.g. ":predictLongRunning").
	methodIdx := strings.LastIndex(rawURL, ":")
	stripped := rawURL
	if methodIdx > schemeEnd+3 {
		stripped = rawURL[:methodIdx]
	}

	// Find the API version segment (/v1beta, /v1, /v2, etc.) and trim
	// everything after it so the operation name isn't doubled.
	pathStart := schemeEnd + 3
	slashAfterHost := strings.Index(stripped[pathStart:], "/")
	if slashAfterHost < 0 {
		return stripped
	}
	pathStart += slashAfterHost // now at first "/" of path
	path := stripped[pathStart:]

	// Match /v followed by a digit and optional suffix up to the next slash.
	vIdx := -1
	for i := 0; i < len(path)-2; i++ {
		if path[i] == '/' && path[i+1] == 'v' && i+2 < len(path) && path[i+2] >= '0' && path[i+2] <= '9' {
			vIdx = i
			break
		}
	}
	if vIdx < 0 {
		return stripped
	}

	// Find the end of the version segment (next '/' after /vN...).
	vEnd := strings.Index(path[vIdx+1:], "/")
	if vEnd < 0 {
		return stripped
	}
	return stripped[:pathStart+vIdx+1+vEnd]
}

// executeAsyncPolling handles the long-running operation pattern: extract the
// operation name from the initial response, poll until done, then download and
// save the artifact.
func executeAsyncPolling(cfg guests.HTTPToolConfig, initialResp []byte, args map[string]string) (string, error) {
	polling := cfg.AsyncPolling

	var parsed map[string]any
	if err := json.Unmarshal(initialResp, &parsed); err != nil {
		return "", fmt.Errorf("parsing initial async response: %w", err)
	}

	opNameRaw, err := extractByPath(parsed, polling.OperationNamePath)
	if err != nil {
		return "", fmt.Errorf("extracting operation name: %w", err)
	}
	opName, ok := opNameRaw.(string)
	if !ok {
		return "", fmt.Errorf("operation name is %T, expected string", opNameRaw)
	}

	baseURL := deriveBaseURL(cfg.URL)
	pollURL := baseURL + "/" + opName

	authHeaders := buildAuthHeaders(cfg)
	pollInterval := time.Duration(polling.PollIntervalMS) * time.Millisecond
	if pollInterval <= 0 {
		pollInterval = 10 * time.Second
	}
	maxWait := time.Duration(polling.MaxWaitMS) * time.Millisecond
	if maxWait <= 0 {
		maxWait = 5 * time.Minute
	}

	deadline := time.Now().Add(maxWait)
	client := &http.Client{Timeout: 30 * time.Second}

	var finalResp map[string]any
	for {
		time.Sleep(pollInterval)

		if time.Now().After(deadline) {
			return "", fmt.Errorf("operation timed out after %dms (max: %dms)", polling.MaxWaitMS, polling.MaxWaitMS)
		}

		req, err := http.NewRequest("GET", pollURL, nil)
		if err != nil {
			return "", fmt.Errorf("creating poll request: %w", err)
		}
		for k, vals := range authHeaders {
			for _, v := range vals {
				req.Header.Set(k, v)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("polling failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("reading poll response: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			return "", fmt.Errorf("polling rate-limited (Retry-After: %s): %s", retryAfter, truncate(string(body), 500))
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("polling failed: HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}

		if err := json.Unmarshal(body, &finalResp); err != nil {
			return "", fmt.Errorf("malformed poll response: %w", err)
		}

		doneRaw, err := extractByPath(finalResp, polling.DonePath)
		if err != nil {
			continue
		}
		done, ok := doneRaw.(bool)
		if !ok || !done {
			continue
		}

		break
	}

	resultURIRaw, err := extractByPath(finalResp, polling.ResultPath)
	if err != nil {
		return "", fmt.Errorf("operation completed but result path not found (%s): %s", polling.ResultPath, truncate(fmt.Sprintf("%v", finalResp), 2000))
	}
	resultURI, ok := resultURIRaw.(string)
	if !ok {
		return "", fmt.Errorf("result path value is %T, expected string", resultURIRaw)
	}

	if cfg.ResponseArtifact == nil {
		return fmt.Sprintf("Async operation completed. Result URI: %s", resultURI), nil
	}

	return handleAsyncArtifactDownload(cfg, resultURI, args)
}

// handleAsyncArtifactDownload downloads a binary artifact from the given URI and
// saves it to the configured path.
func handleAsyncArtifactDownload(cfg guests.HTTPToolConfig, artifactURI string, args map[string]string) (string, error) {
	savePath, err := resolveString(cfg.ResponseArtifact.SaveTo, args)
	if err != nil {
		return "", fmt.Errorf("resolving save_to path: %w", err)
	}

	savePath, err = validateArtifactFilename(savePath, cfg.ResponseArtifact.Type)
	if err != nil {
		return "", err
	}

	dlHeaders := resolveDownloadAuth(cfg)

	req, err := http.NewRequest("GET", artifactURI, nil)
	if err != nil {
		return "", fmt.Errorf("creating download request: %w", err)
	}
	for k, vals := range dlHeaders {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("artifact download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("artifact download failed: HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading artifact data: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return "", fmt.Errorf("creating output directory: %w", err)
	}
	if err := os.WriteFile(savePath, data, 0644); err != nil {
		return "", fmt.Errorf("writing artifact: %w", err)
	}

	msg := fmt.Sprintf("Saved %s (%d bytes) to %s", cfg.ResponseArtifact.Type, len(data), savePath)
	if len(cfg.ResponseArtifact.PostProcess) > 0 {
		ppLog := postProcessFileLogger(savePath)
		ppLog.Info("post-process pipeline starting", "ops", len(cfg.ResponseArtifact.PostProcess), "artifact", savePath)
		ops, err := ResolvePostProcess(cfg.ResponseArtifact.PostProcess, args)
		if err != nil {
			return "", fmt.Errorf("resolving post_process config: %w", err)
		}
		results, state := RunPostProcess(ops, savePath, ppLog)
		if sp := state["spritesheet_path"]; sp != "" {
			msg = fmt.Sprintf("Saved sprite sheet image/png to %s (from %s)", sp, savePath)
		}
		ppLog.Info("post-process pipeline finished", "results", FormatResults("", results))
		msg = FormatResults(msg, results)
	}
	return msg, nil
}

// postProcessFileLogger creates an slog.Logger that writes to a .postprocess.log
// file alongside the artifact. Falls back to the default logger on error.
func postProcessFileLogger(artifactPath string) *slog.Logger {
	logPath := artifactPath + ".postprocess.log"
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		slog.Default().Warn("could not create post-process log file", "path", logPath, "err", err)
		return slog.Default()
	}
	return slog.New(slog.NewTextHandler(io.MultiWriter(f, os.Stderr), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
