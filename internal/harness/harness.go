package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rauriemo/anthem/internal/config"
)

// mcpJSON matches Claude Code's .mcp.json schema.
type mcpJSON struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// WriteMCPConfig writes a .mcp.json file into wsPath that Claude Code auto-discovers.
// If servers is empty, no file is written.
func WriteMCPConfig(wsPath string, servers map[string]config.MCPServerConfig) error {
	if len(servers) == 0 {
		return nil
	}

	out := mcpJSON{MCPServers: make(map[string]mcpServerEntry, len(servers))}
	for name, s := range servers {
		out.MCPServers[name] = mcpServerEntry{
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
		}
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp config: %w", err)
	}

	return os.WriteFile(filepath.Join(wsPath, ".mcp.json"), data, 0644)
}

// PrepareSkills copies skill directories into the workspace's .claude/skills/ directory
// so Claude Code auto-discovers them via three-level progressive loading.
//
// Skill refs come in two forms:
//   - "anthem://name" — built-in skill, copied from builtinDir/name/ to wsPath/.claude/skills/name/
//   - "./path/to/skill" — project-local, already in the repo workspace, no copy needed
func PrepareSkills(wsPath string, skillRefs []string, builtinDir string, logger *slog.Logger) error {
	if len(skillRefs) == 0 {
		return nil
	}

	skillsTarget := filepath.Join(wsPath, ".claude", "skills")

	needsDir := false
	for _, ref := range skillRefs {
		if strings.HasPrefix(ref, "anthem://") {
			needsDir = true
			break
		}
	}

	if needsDir {
		if err := os.MkdirAll(skillsTarget, 0755); err != nil {
			return fmt.Errorf("create skills dir: %w", err)
		}
	}

	for _, ref := range skillRefs {
		if !strings.HasPrefix(ref, "anthem://") {
			continue
		}
		name := strings.TrimPrefix(ref, "anthem://")
		src := filepath.Join(builtinDir, name)
		dst := filepath.Join(skillsTarget, name)

		if err := copyDir(src, dst); err != nil {
			if logger != nil {
				logger.Warn("failed to copy built-in skill", "skill", ref, "error", err)
			}
			continue
		}
	}

	return nil
}

// ResolveMCPServers merges global baseline servers with profile-specific refs.
func ResolveMCPServers(registry map[string]config.MCPServerConfig, globalRefs []string, profileRefs []string) map[string]config.MCPServerConfig {
	merged := make(map[string]config.MCPServerConfig)

	// Global baseline: all servers in the registry are available to all agents
	// (the registry itself is the global baseline in the current design)
	// If globalRefs is provided, only include those; otherwise include all.
	if len(globalRefs) > 0 {
		for _, name := range globalRefs {
			if s, ok := registry[name]; ok {
				merged[name] = s
			}
		}
	}

	// Profile additions
	for _, name := range profileRefs {
		if s, ok := registry[name]; ok {
			merged[name] = s
		}
	}

	return merged
}

// ResolveSkillRefs merges global baseline skills with profile-specific refs, deduplicating.
func ResolveSkillRefs(globalSkills []string, profileRefs []string) []string {
	seen := make(map[string]bool, len(globalSkills)+len(profileRefs))
	var result []string
	for _, s := range globalSkills {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	for _, s := range profileRefs {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}

	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
