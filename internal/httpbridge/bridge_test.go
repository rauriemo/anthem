package httpbridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/guests"
)

// parseResponse is a test helper that unmarshals a JSON-RPC response line.
func parseResponse(t *testing.T, data []byte) jsonRPCResponse {
	t.Helper()
	var resp jsonRPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	return resp
}

// resultMap extracts resp.Result as map[string]any, failing the test if it cannot.
func resultMap(t *testing.T, resp jsonRPCResponse) map[string]any {
	t.Helper()
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not map[string]any: %T", resp.Result)
	}
	return m
}

// contentText extracts the first text from an MCP content array in the result.
func contentText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok {
		t.Fatal("content is not []any")
	}
	if len(content) == 0 {
		t.Fatal("content is empty")
	}
	item, ok := content[0].(map[string]any)
	if !ok {
		t.Fatal("content[0] is not map[string]any")
	}
	text, ok := item["text"].(string)
	if !ok {
		t.Fatal("content[0].text is not string")
	}
	return text
}

func TestExtractInputVars(t *testing.T) {
	tests := []struct {
		name string
		tmpl map[string]any
		want []string
	}{
		{
			name: "simple string var",
			tmpl: map[string]any{"text": "${input.prompt}"},
			want: []string{"prompt"},
		},
		{
			name: "nested map",
			tmpl: map[string]any{
				"contents": []any{
					map[string]any{"parts": []any{map[string]any{"text": "${input.prompt}"}}},
				},
			},
			want: []string{"prompt"},
		},
		{
			name: "multiple vars deduplicated",
			tmpl: map[string]any{
				"a": "${input.prompt}",
				"b": "${input.style}",
				"c": "${input.prompt}",
			},
			want: []string{"prompt", "style"},
		},
		{
			name: "no vars",
			tmpl: map[string]any{"static": "value", "num": 42},
			want: []string{},
		},
		{
			name: "nil template",
			tmpl: nil,
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractInputVars(tt.tmpl)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, v := range tt.want {
				if got[i] != v {
					t.Errorf("got[%d] = %q, want %q", i, got[i], v)
				}
			}
		})
	}
}

func TestResolveTemplate(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    map[string]any
		vars    map[string]string
		wantErr bool
		check   func(t *testing.T, result map[string]any)
	}{
		{
			name: "simple substitution",
			tmpl: map[string]any{"text": "${input.prompt}"},
			vars: map[string]string{"prompt": "draw a cat"},
			check: func(t *testing.T, result map[string]any) {
				if result["text"] != "draw a cat" {
					t.Errorf("text = %v", result["text"])
				}
			},
		},
		{
			name: "nested substitution",
			tmpl: map[string]any{
				"outer": map[string]any{
					"inner": "${input.val}",
				},
			},
			vars: map[string]string{"val": "hello"},
			check: func(t *testing.T, result map[string]any) {
				outer, ok := result["outer"].(map[string]any)
				if !ok {
					t.Fatal("outer is not map[string]any")
				}
				if outer["inner"] != "hello" {
					t.Errorf("inner = %v", outer["inner"])
				}
			},
		},
		{
			name: "array substitution",
			tmpl: map[string]any{
				"items": []any{"${input.a}", "static", "${input.b}"},
			},
			vars: map[string]string{"a": "x", "b": "y"},
			check: func(t *testing.T, result map[string]any) {
				items, ok := result["items"].([]any)
				if !ok {
					t.Fatal("items is not []any")
				}
				if items[0] != "x" || items[1] != "static" || items[2] != "y" {
					t.Errorf("items = %v", items)
				}
			},
		},
		{
			name: "non-string values preserved",
			tmpl: map[string]any{
				"text":   "${input.prompt}",
				"number": 42,
				"flag":   true,
			},
			vars: map[string]string{"prompt": "test"},
			check: func(t *testing.T, result map[string]any) {
				if result["number"] != 42 {
					t.Errorf("number = %v", result["number"])
				}
				if result["flag"] != true {
					t.Errorf("flag = %v", result["flag"])
				}
			},
		},
		{
			name:    "missing variable",
			tmpl:    map[string]any{"text": "${input.missing}"},
			vars:    map[string]string{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolveTemplate(tt.tmpl, tt.vars)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}

func TestMCPProtocol_Initialize(t *testing.T) {
	tools := []ToolConfig{{ID: "test", Config: guests.HTTPToolConfig{
		URL: "http://example.com", Method: "POST",
	}}}

	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	result := resultMap(t, resp)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatal("serverInfo is not map[string]any")
	}
	if info["name"] != "anthem-http-bridge" {
		t.Errorf("server name = %v", info["name"])
	}
}

func TestMCPProtocol_ToolsList(t *testing.T) {
	tools := []ToolConfig{{
		ID: "image-gen",
		Config: guests.HTTPToolConfig{
			URL:         "http://example.com/generate",
			Method:      "POST",
			Description: "Generate images",
			RequestTemplate: map[string]any{
				"prompt": "${input.prompt}",
				"style":  "${input.style}",
			},
		},
	}}

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	result := resultMap(t, resp)
	toolList, ok := result["tools"].([]any)
	if !ok {
		t.Fatal("tools is not []any")
	}
	if len(toolList) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(toolList))
	}
	tool, ok := toolList[0].(map[string]any)
	if !ok {
		t.Fatal("tool is not map[string]any")
	}
	if tool["name"] != "http__image-gen__call" {
		t.Errorf("tool name = %v", tool["name"])
	}
	if tool["description"] != "Generate images" {
		t.Errorf("description = %v", tool["description"])
	}
	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok {
		t.Fatal("inputSchema is not map[string]any")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties is not map[string]any")
	}
	if _, ok := props["prompt"]; !ok {
		t.Error("missing prompt property")
	}
	if _, ok := props["style"]; !ok {
		t.Error("missing style property")
	}
}

func TestMCPProtocol_ToolCall_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
			return
		}
		if body["query"] != "hello world" {
			t.Errorf("unexpected body: %v", body)
		}
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"result": "ok"}`)
	}))
	defer srv.Close()

	tools := []ToolConfig{{
		ID: "test-api",
		Config: guests.HTTPToolConfig{
			URL:    srv.URL,
			Method: "POST",
			RequestTemplate: map[string]any{
				"query": "${input.prompt}",
			},
		},
	}}

	call := map[string]any{
		"name":      "http__test-api__call",
		"arguments": map[string]string{"prompt": "hello world"},
	}
	callJSON, _ := json.Marshal(call)
	input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, callJSON) + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	result := resultMap(t, resp)
	text := contentText(t, result)
	if !strings.Contains(text, "ok") {
		t.Errorf("unexpected result text: %s", text)
	}
}

func TestMCPProtocol_ToolCall_AuthBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"ok":true}`)
	}))
	defer srv.Close()

	t.Setenv("TEST_BEARER_KEY", "secret-token-123")

	tools := []ToolConfig{{
		ID: "auth-test",
		Config: guests.HTTPToolConfig{
			URL:             srv.URL,
			Method:          "POST",
			AuthTokenEnv:    "TEST_BEARER_KEY",
			AuthScheme:      "bearer",
			RequestTemplate: map[string]any{"q": "${input.q}"},
		},
	}}

	call := map[string]any{"name": "http__auth-test__call", "arguments": map[string]string{"q": "x"}}
	callJSON, _ := json.Marshal(call)
	input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, callJSON) + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer secret-token-123" {
		t.Errorf("auth header = %q, want %q", gotAuth, "Bearer secret-token-123")
	}
}

func TestMCPProtocol_ToolCall_AuthAPIKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		fmt.Fprintf(w, `{"ok":true}`)
	}))
	defer srv.Close()

	t.Setenv("TEST_API_KEY", "gemini-key-456")

	tools := []ToolConfig{{
		ID: "apikey-test",
		Config: guests.HTTPToolConfig{
			URL:             srv.URL,
			Method:          "POST",
			AuthTokenEnv:    "TEST_API_KEY",
			AuthScheme:      "api-key",
			RequestTemplate: map[string]any{"q": "${input.q}"},
		},
	}}

	call := map[string]any{"name": "http__apikey-test__call", "arguments": map[string]string{"q": "x"}}
	callJSON, _ := json.Marshal(call)
	input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, callJSON) + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	if gotKey != "gemini-key-456" {
		t.Errorf("api key header = %q, want %q", gotKey, "gemini-key-456")
	}
}

func TestMCPProtocol_ToolCall_ArtifactSave(t *testing.T) {
	imageData := []byte("fake-png-data")
	b64Image := base64.StdEncoding.EncodeToString(imageData)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiResp := map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]any{{
						"inlineData": map[string]any{
							"mimeType": "image/png",
							"data":     b64Image,
						},
					}},
				},
			}},
		}
		if err := json.NewEncoder(w).Encode(apiResp); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	defer srv.Close()

	outDir := t.TempDir()
	saveTo := filepath.Join(outDir, "generated", "${input.filename}")

	tools := []ToolConfig{{
		ID: "img-gen",
		Config: guests.HTTPToolConfig{
			URL:             srv.URL,
			Method:          "POST",
			RequestTemplate: map[string]any{"prompt": "${input.prompt}"},
			ResponseArtifact: &guests.ArtifactTemplate{
				Type:   "image/png",
				SaveTo: saveTo,
			},
		},
	}}

	call := map[string]any{
		"name":      "http__img-gen__call",
		"arguments": map[string]string{"prompt": "a cat", "filename": "cat.png"},
	}
	callJSON, _ := json.Marshal(call)
	input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, callJSON) + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	expectedPath := filepath.Join(outDir, "generated", "cat.png")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("artifact not written: %v", err)
	}
	if string(data) != "fake-png-data" {
		t.Errorf("artifact content = %q, want %q", string(data), "fake-png-data")
	}

	resp := parseResponse(t, out.Bytes())
	result := resultMap(t, resp)
	text := contentText(t, result)
	if !strings.Contains(text, "cat.png") {
		t.Errorf("result text should mention file: %s", text)
	}
}

func TestMCPProtocol_UnknownMethod(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"unknown/method","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Run(nil, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}

func TestMCPProtocol_ToolCall_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":"forbidden"}`)
	}))
	defer srv.Close()

	tools := []ToolConfig{{
		ID: "err-test",
		Config: guests.HTTPToolConfig{
			URL:             srv.URL,
			Method:          "POST",
			RequestTemplate: map[string]any{"q": "${input.q}"},
		},
	}}

	call := map[string]any{"name": "http__err-test__call", "arguments": map[string]string{"q": "x"}}
	callJSON, _ := json.Marshal(call)
	input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, callJSON) + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	result := resultMap(t, resp)
	if result["isError"] != true {
		t.Error("expected isError=true for HTTP 403")
	}
	text := contentText(t, result)
	if !strings.Contains(text, "403") {
		t.Errorf("error text should mention status code: %s", text)
	}
}

func TestMCPProtocol_ToolCall_UnknownTool(t *testing.T) {
	tools := []ToolConfig{{
		ID: "real-tool",
		Config: guests.HTTPToolConfig{
			URL: "http://example.com", Method: "POST",
			RequestTemplate: map[string]any{"q": "${input.q}"},
		},
	}}

	call := map[string]any{"name": "http__nonexistent__call", "arguments": map[string]string{"q": "x"}}
	callJSON, _ := json.Marshal(call)
	input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, callJSON) + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
}

func TestMCPProtocol_MalformedJSON(t *testing.T) {
	input := "this is not json\n"
	var out bytes.Buffer
	if err := Run(nil, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	if resp.Error == nil {
		t.Fatal("expected parse error")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("error code = %d, want -32700", resp.Error.Code)
	}
}

func TestMCPProtocol_NotificationsInitialized(t *testing.T) {
	input := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	var out bytes.Buffer
	if err := Run(nil, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Errorf("expected no output for notification, got %q", out.String())
	}
}

func TestMCPProtocol_ToolsList_FilenameInjection(t *testing.T) {
	tools := []ToolConfig{{
		ID: "img",
		Config: guests.HTTPToolConfig{
			URL:             "http://example.com",
			Method:          "POST",
			RequestTemplate: map[string]any{"prompt": "${input.prompt}"},
			ResponseArtifact: &guests.ArtifactTemplate{
				Type:   "image/png",
				SaveTo: "/out/${input.filename}",
			},
		},
	}}

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, out.Bytes())
	result := resultMap(t, resp)
	toolList, ok := result["tools"].([]any)
	if !ok {
		t.Fatal("tools is not []any")
	}
	tool, ok := toolList[0].(map[string]any)
	if !ok {
		t.Fatal("tool is not map[string]any")
	}
	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok {
		t.Fatal("inputSchema is not map[string]any")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties is not map[string]any")
	}
	if _, ok := props["filename"]; !ok {
		t.Error("filename should be auto-injected into schema from response_artifact.save_to")
	}
	if _, ok := props["prompt"]; !ok {
		t.Error("prompt should be in schema from request_template")
	}
	req, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("required is not []any")
	}
	if len(req) != 2 {
		t.Errorf("expected 2 required fields, got %d", len(req))
	}
}

func TestMCPProtocol_ToolCall_DefaultAuthScheme(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"ok":true}`)
	}))
	defer srv.Close()

	t.Setenv("TEST_DEFAULT_KEY", "default-token")

	tools := []ToolConfig{{
		ID: "default-auth",
		Config: guests.HTTPToolConfig{
			URL:             srv.URL,
			Method:          "POST",
			AuthTokenEnv:    "TEST_DEFAULT_KEY",
			AuthScheme:      "",
			RequestTemplate: map[string]any{"q": "${input.q}"},
		},
	}}

	call := map[string]any{"name": "http__default-auth__call", "arguments": map[string]string{"q": "x"}}
	callJSON, _ := json.Marshal(call)
	input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, callJSON) + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer default-token" {
		t.Errorf("default auth should be Bearer, got %q", gotAuth)
	}
}

func TestMCPProtocol_MultipleRequests(t *testing.T) {
	tools := []ToolConfig{{ID: "t", Config: guests.HTTPToolConfig{
		URL: "http://example.com", Method: "POST",
	}}}

	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"

	var out bytes.Buffer
	if err := Run(tools, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses, got %d: %q", len(lines), out.String())
	}

	resp1 := parseResponse(t, []byte(lines[0]))
	result1 := resultMap(t, resp1)
	if result1["protocolVersion"] != "2024-11-05" {
		t.Errorf("first response should be initialize result")
	}

	resp2 := parseResponse(t, []byte(lines[1]))
	result2 := resultMap(t, resp2)
	if _, ok := result2["tools"]; !ok {
		t.Errorf("second response should be tools/list result")
	}
}

func TestLoadConfigFromEnv_InvalidBase64(t *testing.T) {
	t.Setenv("ANTHEM_HTTP_BRIDGE_CONFIG", "not-valid-base64!!!")
	_, err := LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if !strings.Contains(err.Error(), "decoding") {
		t.Errorf("error should mention decoding: %v", err)
	}
}

func TestLoadConfigFromEnv_InvalidJSON(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("{invalid json"))
	t.Setenv("ANTHEM_HTTP_BRIDGE_CONFIG", encoded)
	_, err := LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parsing") {
		t.Errorf("error should mention parsing: %v", err)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	cfg := map[string]guests.HTTPToolConfig{
		"test-tool": {
			URL:    "http://example.com",
			Method: "POST",
		},
	}
	data, _ := json.Marshal(cfg)
	encoded := base64.StdEncoding.EncodeToString(data)
	t.Setenv("ANTHEM_HTTP_BRIDGE_CONFIG", encoded)

	tools, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].ID != "test-tool" {
		t.Errorf("tool ID = %q, want %q", tools[0].ID, "test-tool")
	}
	if tools[0].Config.URL != "http://example.com" {
		t.Errorf("URL = %q", tools[0].Config.URL)
	}
}

func TestLoadConfigFromEnv_NotSet(t *testing.T) {
	t.Setenv("ANTHEM_HTTP_BRIDGE_CONFIG", "")
	_, err := LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected error when env not set")
	}
}
