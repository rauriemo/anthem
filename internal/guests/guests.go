package guests

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rauriemo/conduit/pkg/mcpconfig"
	"gopkg.in/yaml.v3"
)

var templateVarRe = regexp.MustCompile(`\$\{input\.([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// PostProcessOp declares a typed post-processing operation to run on an
// artifact after it is saved to disk. The bridge owns the implementation
// of each named operation; guest YAML declares intent, not mechanism.
type PostProcessOp struct {
	Op     string            `yaml:"op" json:"op"`
	Config map[string]string `yaml:"config,omitempty" json:"config,omitempty"`
}

type ArtifactTemplate struct {
	Type        string          `yaml:"type" json:"type"`
	SaveTo      string          `yaml:"save_to" json:"save_to"`
	PostProcess []PostProcessOp `yaml:"post_process,omitempty" json:"post_process,omitempty"`
}

// knownPostProcessOps is the closed set of operations the bridge implements.
var knownPostProcessOps = map[string]bool{
	"remove_background":    true,
	"extract_video_frames": true,
	"normalize_frames":     true,
	"stitch_spritesheet":   true,
}

// ValidatePostProcess checks that every operation in the list is recognized.
// Returns a slice of errors (one per problem). Callers should log these as
// warnings rather than blocking guest registration, so that a stale guest
// config referencing an op from a newer Anthem version doesn't break loading.
func ValidatePostProcess(ops []PostProcessOp) []error {
	var errs []error
	for i, op := range ops {
		if op.Op == "" {
			errs = append(errs, fmt.Errorf("post_process[%d]: missing op field", i))
		} else if !knownPostProcessOps[op.Op] {
			errs = append(errs, fmt.Errorf("post_process[%d]: unknown op %q", i, op.Op))
		}
	}
	return errs
}

type AsyncPollingConfig struct {
	Enabled           bool   `yaml:"enabled" json:"enabled"`
	PollIntervalMS    int    `yaml:"poll_interval_ms" json:"poll_interval_ms"`
	MaxWaitMS         int    `yaml:"max_wait_ms" json:"max_wait_ms"`
	OperationNamePath string `yaml:"operation_name_path" json:"operation_name_path"`
	DonePath          string `yaml:"done_path" json:"done_path"`
	ResultPath        string `yaml:"result_path" json:"result_path"`
	DownloadAuth      string `yaml:"download_auth,omitempty" json:"download_auth,omitempty"`
}

// InputTypeSpec declares how a template variable should be resolved at tool-call
// time. Type "file" causes the bridge to read the file from disk and encode it;
// "string" (or empty) passes the value through unchanged.
type InputTypeSpec struct {
	Type      string `yaml:"type" json:"type"`
	Encoding  string `yaml:"encoding,omitempty" json:"encoding,omitempty"`
	MaxSizeMB int    `yaml:"max_size_mb,omitempty" json:"max_size_mb,omitempty"`
}

type HTTPToolConfig struct {
	URL              string                      `yaml:"url" json:"url"`
	Method           string                      `yaml:"method" json:"method"`
	AuthTokenEnv     string                      `yaml:"auth_token_env,omitempty" json:"auth_token_env,omitempty"`
	AuthScheme       string                      `yaml:"auth_scheme,omitempty" json:"auth_scheme,omitempty"`
	AsyncPolling     *AsyncPollingConfig         `yaml:"async_polling,omitempty" json:"async_polling,omitempty"`
	InputTypes       map[string]InputTypeSpec    `yaml:"input_types,omitempty" json:"input_types,omitempty"`
	RequestTemplate  map[string]any              `yaml:"request_template,omitempty" json:"request_template,omitempty"`
	ResponseArtifact *ArtifactTemplate           `yaml:"response_artifact,omitempty" json:"response_artifact,omitempty"`
	TimeoutMS        int                         `yaml:"timeout_ms,omitempty" json:"timeout_ms,omitempty"`
	Description      string                      `yaml:"description,omitempty" json:"description,omitempty"`
}

type GuestAgent struct {
	ID                      string                            `json:"id"`
	Name                    string                            `json:"name"`
	Description             string                            `json:"description"`
	Role                    string                            `json:"role,omitempty"`
	Capabilities            []string                          `json:"capabilities,omitempty"`
	Icon                    string                            `json:"icon,omitempty"`
	Model                   string                            `json:"model,omitempty"`
	Quotes                  []string                          `json:"quotes,omitempty"`
	RequirementsFingerprint string                            `json:"requirements_fingerprint"`
	AllowedTools            []string                          `json:"allowed_tools,omitempty"`
	MCPServers              map[string]mcpconfig.MCPServerRef `json:"mcp_servers,omitempty"`
	HTTPTools               map[string]HTTPToolConfig         `json:"http_tools,omitempty"`
	Scope                   string                            `json:"scope"`
	Source                  string                            `json:"source"`
	File                    string                            `json:"file"`
}

type GuestIndex struct {
	Version     int                   `json:"version"`
	GeneratedAt time.Time             `json:"generated_at"`
	Agents      map[string]GuestAgent `json:"agents"`
}

type frontmatter struct {
	Name         string                            `yaml:"name"`
	Description  string                            `yaml:"description"`
	Model        string                            `yaml:"model"`
	Role         string                            `yaml:"role"`
	Capabilities []string                          `yaml:"capabilities"`
	Icon         string                            `yaml:"icon"`
	Quotes       []string                          `yaml:"quotes"`
	Requirements map[string]any                    `yaml:"requirements"`
	AllowedTools []string                          `yaml:"allowed_tools"`
	MCPServers   map[string]mcpconfig.MCPServerRef `yaml:"mcp_servers"`
	HTTPTools    map[string]HTTPToolConfig         `yaml:"http_tools"`
}

const indexFile = ".agents-index.json"

func ScanDirectory(dir string, logger *slog.Logger) (GuestIndex, error) {
	if logger == nil {
		logger = slog.Default()
	}

	index := GuestIndex{
		Version:     1,
		GeneratedAt: time.Now().UTC(),
		Agents:      make(map[string]GuestAgent),
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return index, fmt.Errorf("scanning agents directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if entry.Name() == "orchestrator.md" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("skipping agent file", "path", path, "error", err)
			continue
		}

		agent, err := ParseFrontmatter(data)
		if err != nil {
			logger.Warn("skipping agent file", "path", path, "error", err)
			continue
		}

		slug := strings.TrimSuffix(entry.Name(), ".md")
		agent.ID = slug
		agent.File = entry.Name()
		agent.Scope = "project"
		agent.Source = "local"

		for _, tool := range agent.HTTPTools {
			if tool.ResponseArtifact != nil {
				for _, warn := range ValidatePostProcess(tool.ResponseArtifact.PostProcess) {
					logger.Warn("post_process validation", "agent", slug, "warning", warn)
				}
			}
		}

		index.Agents[slug] = agent
	}

	return index, nil
}

func WriteIndex(dir string, index GuestIndex) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent index: %w", err)
	}

	target := filepath.Join(dir, indexFile)
	tmp := target + ".tmp"

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing temp agent index: %w", err)
	}

	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming agent index: %w", err)
	}

	return nil
}

func LoadIndex(dir string) (GuestIndex, error) {
	var index GuestIndex
	data, err := os.ReadFile(filepath.Join(dir, indexFile))
	if err != nil {
		return index, fmt.Errorf("reading agent index: %w", err)
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return index, fmt.Errorf("parsing agent index: %w", err)
	}
	return index, nil
}

func LoadPersona(dir string, slug string) (string, error) {
	path := filepath.Join(dir, slug+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("loading persona for %q: %w", slug, err)
	}

	_, body, err := splitFrontmatterDelimiters(data)
	if err != nil {
		return "", fmt.Errorf("parsing persona for %q: %w", slug, err)
	}

	return body, nil
}

// LoadOrchestratorPersona loads agents/orchestrator.md body, returning empty
// string if the file doesn't exist. Separate from ScanDirectory so the guest
// index stays clean.
func LoadOrchestratorPersona(dir string) (string, error) {
	body, err := LoadPersona(dir, "orchestrator")
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		// LoadPersona wraps the error; check the underlying cause
		if strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "cannot find") {
			return "", nil
		}
		return "", err
	}
	return body, nil
}

// LoadAgentFrontmatter reads any agent .md file and returns its YAML
// frontmatter as a generic map. Returns an empty map (not an error) if the
// file doesn't exist or has no frontmatter.
func LoadAgentFrontmatter(agentPath string) (map[string]any, error) {
	data, err := os.ReadFile(agentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), nil
		}
		return nil, fmt.Errorf("reading agent file %s: %w", agentPath, err)
	}

	yamlBytes, _, err := splitFrontmatterDelimiters(data)
	if err != nil {
		return make(map[string]any), nil
	}

	var fm map[string]any
	if err := yaml.Unmarshal(yamlBytes, &fm); err != nil {
		return nil, fmt.Errorf("parsing YAML frontmatter in %s: %w", agentPath, err)
	}
	if fm == nil {
		fm = make(map[string]any)
	}
	return fm, nil
}

func ParseFrontmatter(data []byte) (GuestAgent, error) {
	yamlBytes, _, err := splitFrontmatterDelimiters(data)
	if err != nil {
		return GuestAgent{}, err
	}

	var fm frontmatter
	if err := yaml.Unmarshal(yamlBytes, &fm); err != nil {
		return GuestAgent{}, fmt.Errorf("parsing YAML frontmatter: %w", err)
	}

	if fm.Name == "" {
		return GuestAgent{}, fmt.Errorf("missing required field: name")
	}
	if fm.Description == "" {
		return GuestAgent{}, fmt.Errorf("missing required field: description")
	}

	for name, tool := range fm.HTTPTools {
		if tool.AuthScheme != "" && tool.AuthScheme != "bearer" && tool.AuthScheme != "api-key" {
			return GuestAgent{}, fmt.Errorf("http_tools[%q]: auth_scheme must be \"bearer\", \"api-key\", or empty, got %q", name, tool.AuthScheme)
		}
		if err := validateInputTypes(name, tool); err != nil {
			return GuestAgent{}, err
		}
		applyInputTypeDefaults(fm.HTTPTools, name)
	}

	return GuestAgent{
		Name:                    fm.Name,
		Description:             fm.Description,
		Role:                    fm.Role,
		Capabilities:            fm.Capabilities,
		Icon:                    fm.Icon,
		Model:                   fm.Model,
		Quotes:                  fm.Quotes,
		RequirementsFingerprint: ComputeRequirementsFingerprint(fm.Requirements),
		AllowedTools:            fm.AllowedTools,
		MCPServers:              fm.MCPServers,
		HTTPTools:               fm.HTTPTools,
	}, nil
}

func extractTemplateVars(tmpl map[string]any) map[string]bool {
	seen := make(map[string]bool)
	walkTemplateStrings(tmpl, func(s string) {
		for _, match := range templateVarRe.FindAllStringSubmatch(s, -1) {
			seen[match[1]] = true
		}
	})
	return seen
}

func walkTemplateStrings(v any, fn func(string)) {
	switch val := v.(type) {
	case string:
		fn(val)
	case map[string]any:
		for _, child := range val {
			walkTemplateStrings(child, fn)
		}
	case []any:
		for _, child := range val {
			walkTemplateStrings(child, fn)
		}
	}
}

func validateInputTypes(toolName string, tool HTTPToolConfig) error {
	if len(tool.InputTypes) == 0 {
		return nil
	}
	templateVars := extractTemplateVars(tool.RequestTemplate)
	for key, spec := range tool.InputTypes {
		if !templateVars[key] {
			return fmt.Errorf("http_tools[%q].input_types[%q]: key not found in request_template (no ${input.%s} reference)", toolName, key, key)
		}
		switch spec.Type {
		case "", "string", "file":
		default:
			return fmt.Errorf("http_tools[%q].input_types[%q]: unsupported input type %q", toolName, key, spec.Type)
		}
		if spec.Type == "file" {
			switch spec.Encoding {
			case "", "base64", "raw":
			default:
				return fmt.Errorf("http_tools[%q].input_types[%q]: encoding must be \"base64\", \"raw\", or empty, got %q", toolName, key, spec.Encoding)
			}
			if spec.MaxSizeMB < 0 || spec.MaxSizeMB > 50 {
				return fmt.Errorf("http_tools[%q].input_types[%q]: max_size_mb must be between 0 and 50, got %d", toolName, key, spec.MaxSizeMB)
			}
		}
	}
	return nil
}

func applyInputTypeDefaults(tools map[string]HTTPToolConfig, toolName string) {
	tool := tools[toolName]
	for key, spec := range tool.InputTypes {
		if spec.Type == "" {
			spec.Type = "string"
		}
		if spec.Type == "file" {
			if spec.Encoding == "" {
				spec.Encoding = "base64"
			}
			if spec.MaxSizeMB == 0 {
				spec.MaxSizeMB = 10
			}
		}
		tool.InputTypes[key] = spec
	}
	tools[toolName] = tool
}

func ComputeRequirementsFingerprint(requirements map[string]any) string {
	if len(requirements) == 0 {
		return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(nil))
	}

	normalized := normalizeForHash(requirements)
	data, _ := json.Marshal(normalized)
	return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(data))
}

func FilterByAllowlist(index GuestIndex, allowlist []string) GuestIndex {
	if allowlist == nil {
		return index
	}

	filtered := GuestIndex{
		Version:     index.Version,
		GeneratedAt: index.GeneratedAt,
		Agents:      make(map[string]GuestAgent),
	}

	seen := make(map[string]bool)
	for _, slug := range allowlist {
		if seen[slug] {
			continue
		}
		seen[slug] = true
		if agent, ok := index.Agents[slug]; ok {
			filtered.Agents[slug] = agent
		}
	}

	return filtered
}

func splitFrontmatterDelimiters(data []byte) (yamlBytes []byte, body string, err error) {
	content := bytes.TrimSpace(data)
	if len(content) == 0 {
		return nil, "", fmt.Errorf("empty file")
	}

	if !bytes.HasPrefix(content, []byte("---")) {
		return nil, "", fmt.Errorf("missing opening --- delimiter")
	}

	content = content[3:]
	// Skip newline after opening delimiter
	if len(content) > 0 && content[0] == '\r' {
		content = content[1:]
	}
	if len(content) > 0 && content[0] == '\n' {
		content = content[1:]
	}

	idx := bytes.Index(content, []byte("\n---"))
	if idx < 0 {
		// Check for --- at end without trailing newline
		if bytes.HasSuffix(bytes.TrimRight(content, "\r\n \t"), []byte("---")) {
			// Entire content is frontmatter if it ends with ---
			trimmed := bytes.TrimRight(content, "\r\n \t")
			yamlPart := trimmed[:len(trimmed)-3]
			return bytes.TrimSpace(yamlPart), "", nil
		}
		return nil, "", fmt.Errorf("missing closing --- delimiter")
	}

	yamlPart := content[:idx]
	rest := content[idx+4:] // skip \n---

	// Skip optional newline after closing delimiter
	if len(rest) > 0 && rest[0] == '\r' {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	return bytes.TrimSpace(yamlPart), strings.TrimSpace(string(rest)), nil
}

func normalizeForHash(v any) any {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		ordered := make([][2]any, len(keys))
		for i, k := range keys {
			ordered[i] = [2]any{k, normalizeForHash(val[k])}
		}
		return ordered
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = normalizeForHash(item)
		}
		return result
	default:
		return v
	}
}
