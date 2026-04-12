package guests

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rauriemo/conduit/pkg/mcpconfig"
	"gopkg.in/yaml.v3"
)

type ArtifactTemplate struct {
	Type   string `yaml:"type" json:"type"`
	SaveTo string `yaml:"save_to" json:"save_to"`
}

type HTTPToolConfig struct {
	URL              string            `yaml:"url" json:"url"`
	Method           string            `yaml:"method" json:"method"`
	AuthTokenEnv     string            `yaml:"auth_token_env,omitempty" json:"auth_token_env,omitempty"`
	AuthScheme       string            `yaml:"auth_scheme,omitempty" json:"auth_scheme,omitempty"`
	RequestTemplate  map[string]any    `yaml:"request_template,omitempty" json:"request_template,omitempty"`
	ResponseArtifact *ArtifactTemplate `yaml:"response_artifact,omitempty" json:"response_artifact,omitempty"`
	TimeoutMS        int               `yaml:"timeout_ms,omitempty" json:"timeout_ms,omitempty"`
	Description      string            `yaml:"description,omitempty" json:"description,omitempty"`
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
