package harness

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/conduit/pkg/mcpbridge"
	"github.com/rauriemo/conduit/pkg/mcpconfig"
)

// WriteMCPConfig writes a .mcp.json file into wsPath that Claude Code auto-discovers.
// Delegates to conduit's mcpbridge package. Applies backward-compat migration for
// servers that have a Command but no explicit Type (defaults to stdio).
func WriteMCPConfig(wsPath string, servers map[string]mcpconfig.MCPServerRef) error {
	if len(servers) == 0 {
		return nil
	}

	migrated := make(map[string]mcpconfig.MCPServerRef, len(servers))
	for name, ref := range servers {
		if ref.Type == "" && ref.Command != "" {
			ref.Type = mcpconfig.TransportStdio
		}
		migrated[name] = ref
	}
	return mcpbridge.WriteMCPConfig(wsPath, migrated)
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
		dst := filepath.Join(skillsTarget, name)

		// Try embedded skills first (shipped with binary), then filesystem fallback
		if found, err := WriteEmbeddedSkill(name, dst); found {
			if err != nil && logger != nil {
				logger.Warn("failed to extract embedded skill", "skill", ref, "error", err)
			}
			continue
		}

		src := filepath.Join(builtinDir, name)
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
func ResolveMCPServers(registry map[string]mcpconfig.MCPServerRef, globalRefs []string, profileRefs []string) map[string]mcpconfig.MCPServerRef {
	merged := make(map[string]mcpconfig.MCPServerRef)

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

// MergeGuestServers merges global config servers with per-guest inline server
// declarations from frontmatter. Guest servers take precedence on name collision.
func MergeGuestServers(global map[string]mcpconfig.MCPServerRef, guest map[string]mcpconfig.MCPServerRef) map[string]mcpconfig.MCPServerRef {
	if len(global) == 0 && len(guest) == 0 {
		return nil
	}
	merged := make(map[string]mcpconfig.MCPServerRef, len(global)+len(guest))
	for k, v := range global {
		merged[k] = v
	}
	for k, v := range guest {
		merged[k] = v
	}
	return merged
}

// HTTPToolsToMCPServers converts a map of http_tools configs into synthetic MCP server
// refs that point to `anthem http-bridge`. Each tool gets its own bridge process with
// the config passed via ANTHEM_HTTP_BRIDGE_CONFIG env var (base64-encoded JSON).
// projectRoot is set as ANTHEM_HTTP_BRIDGE_ROOT so the bridge can sandbox file reads.
func HTTPToolsToMCPServers(httpTools map[string]guests.HTTPToolConfig, projectRoot string) map[string]mcpconfig.MCPServerRef {
	if len(httpTools) == 0 {
		return nil
	}

	anthemBin, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(anthemBin); err == nil {
		anthemBin = resolved
	}

	result := make(map[string]mcpconfig.MCPServerRef, len(httpTools))
	for id, cfg := range httpTools {
		toolMap := map[string]guests.HTTPToolConfig{id: cfg}
		data, err := json.Marshal(toolMap)
		if err != nil {
			continue
		}
		encoded := base64.StdEncoding.EncodeToString(data)

		env := map[string]string{
			"ANTHEM_HTTP_BRIDGE_CONFIG": encoded,
		}
		if projectRoot != "" {
			env["ANTHEM_HTTP_BRIDGE_ROOT"] = projectRoot
		}
		if cfg.AuthTokenEnv != "" {
			env[cfg.AuthTokenEnv] = os.Getenv(cfg.AuthTokenEnv)
		}

		serverKey := fmt.Sprintf("http__%s", id)
		result[serverKey] = mcpconfig.MCPServerRef{
			Type:    mcpconfig.TransportStdio,
			Command: anthemBin,
			Args:    []string{"http-bridge"},
			Env:     env,
		}
	}
	return result
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
