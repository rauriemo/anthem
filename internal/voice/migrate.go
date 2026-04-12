package voice

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// MigrateVoiceToOrchestrator checks whether agents/orchestrator.md needs to be
// created from Identity/Personality sections still in ~/.anthem/VOICE.md.
//
// Rules:
//   - If agents/orchestrator.md already exists → no-op. If VOICE.md still has
//     agent sections, log a warning but do NOT auto-prune.
//   - If VOICE.md has no Identity/Personality → nothing to migrate.
//   - Otherwise: extract agent sections into a new orchestrator.md, remove them
//     from VOICE.md, and write both files.
func MigrateVoiceToOrchestrator(voicePath, agentsDir, projectName string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	orchPath := filepath.Join(agentsDir, "orchestrator.md")

	orchExists := false
	if _, err := os.Stat(orchPath); err == nil {
		orchExists = true
	}

	// Load VOICE.md
	vc, err := LoadFile(voicePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.Debug("migration: VOICE.md not found, skipping")
			return nil
		}
		return fmt.Errorf("migration: loading VOICE.md: %w", err)
	}

	// Check for agent-owned sections in VOICE.md
	var agentSections []Section
	var userSections []Section
	for _, s := range vc.Sections {
		if IsAgentSection(s.Name) {
			agentSections = append(agentSections, s)
		} else {
			userSections = append(userSections, s)
		}
	}

	if orchExists {
		if len(agentSections) > 0 {
			names := make([]string, len(agentSections))
			for i, s := range agentSections {
				names[i] = s.Name
			}
			logger.Warn("VOICE.md contains personality sections but orchestrator.md already exists; orchestrator.md takes precedence, stale sections in VOICE.md are ignored",
				"stale_sections", strings.Join(names, ", "))
		}
		return nil
	}

	if len(agentSections) == 0 {
		logger.Debug("migration: no agent sections in VOICE.md, skipping")
		return nil
	}

	// Create agents/ directory if needed
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("migration: creating agents dir: %w", err)
	}

	// Build orchestrator.md with minimal frontmatter + extracted sections
	displayName := projectName
	if displayName == "" {
		displayName = "Project"
	}

	var orch strings.Builder
	fmt.Fprintf(&orch, "---\nname: %s\ndescription: Project orchestrator and pair programmer\nrole: orchestrator\n---\n\n", displayName)
	for i, s := range agentSections {
		if i > 0 {
			orch.WriteString("\n\n")
		}
		fmt.Fprintf(&orch, "## %s\n%s", s.Name, s.Content)
	}
	orch.WriteByte('\n')

	if err := os.WriteFile(orchPath, []byte(orch.String()), 0644); err != nil {
		return fmt.Errorf("migration: writing orchestrator.md: %w", err)
	}

	// Rewrite VOICE.md without agent sections
	cleaned := renderSections(userSections)
	if cleaned != "" {
		cleaned += "\n"
	}
	if err := os.WriteFile(voicePath, []byte(cleaned), 0644); err != nil {
		return fmt.Errorf("migration: rewriting VOICE.md: %w", err)
	}

	migrated := make([]string, len(agentSections))
	for i, s := range agentSections {
		migrated[i] = s.Name
	}
	logger.Info("migrated voice sections to orchestrator.md",
		"sections", strings.Join(migrated, ", "),
		"orchestrator", orchPath)

	return nil
}
