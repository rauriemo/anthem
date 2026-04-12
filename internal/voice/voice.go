package voice

import (
	"fmt"
	"os"
	"strings"
)

type Section struct {
	Name    string
	Content string
}

type VoiceConfig struct {
	Raw      string
	Sections []Section
}

func LoadFile(path string) (*VoiceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading voice file: %w", err)
	}
	return Parse(string(data))
}

func Parse(content string) (*VoiceConfig, error) {
	vc := &VoiceConfig{Raw: content}
	vc.Sections = extractSections(content)
	return vc, nil
}

func extractSections(content string) []Section {
	var sections []Section
	var currentName string
	var buf strings.Builder

	finalize := func() {
		if currentName != "" {
			sections = append(sections, Section{
				Name:    currentName,
				Content: strings.TrimSpace(buf.String()),
			})
		}
	}

	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") {
			finalize()
			currentName = strings.TrimPrefix(line, "## ")
			buf.Reset()
			continue
		}
		if currentName != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	finalize()
	return sections
}

func SelfEvolutionInstruction() string {
	return `[You have two evolving files:
1. agents/orchestrator.md -- YOUR character. Identity, Personality, Focus, Coordination. Update sections here to evolve who you are for this project.
2. ~/.anthem/VOICE.md -- what you know about the USER. Communication style, working habits, expertise, preferences. Update sections here when you learn something about how the user works. This knowledge is shared across all projects so new agents don't start blind.
Use update_voice with the appropriate section_name. Agent character sections (Identity, Personality, Your Focus, Coordination) route to orchestrator.md. Everything else routes to VOICE.md.]`
}

// AgentOwnedSections is the allowlist of section names that belong to the
// orchestrator agent's character file (agents/orchestrator.md). Any section
// not in this set routes to the shared user-knowledge file (VOICE.md).
var AgentOwnedSections = map[string]bool{
	"Identity":     true,
	"Personality":  true,
	"Your Focus":   true,
	"Coordination": true,
}

// IsAgentSection returns true if the named section belongs in orchestrator.md.
func IsAgentSection(name string) bool {
	return AgentOwnedSections[name]
}

// UpdateAgentSection reads a frontmatter+body .md file, replaces or appends
// the named ## section, and writes it back. Frontmatter is preserved verbatim.
func UpdateAgentSection(filePath, sectionName, content string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading agent file: %w", err)
	}

	raw := string(data)

	// Split frontmatter from body
	var fmBlock, body string
	if strings.HasPrefix(strings.TrimSpace(raw), "---") {
		trimmed := strings.TrimSpace(raw)
		rest := trimmed[3:]
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			endDelim := 3 + idx + 4 // "---" + rest[:idx] + "\n---"
			fmBlock = trimmed[:endDelim]
			body = trimmed[endDelim:]
			if len(body) > 0 && body[0] == '\n' {
				body = body[1:]
			}
		} else {
			body = raw
		}
	} else {
		body = raw
	}

	vc, _ := Parse(body)

	proposed := &VoiceConfig{
		Sections: []Section{{Name: sectionName, Content: content}},
	}
	merged := Merge(vc, proposed)

	var result strings.Builder
	if fmBlock != "" {
		result.WriteString(fmBlock)
		result.WriteString("\n\n")
	}
	result.WriteString(renderSections(merged.Sections))
	result.WriteByte('\n')

	return os.WriteFile(filePath, []byte(result.String()), 0644)
}
