package voice

import (
	"fmt"
	"os"
	"strings"
)

type Section struct {
	Name    string
	Content string
	IsCore  bool
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
	var current *Section

	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") {
			if current != nil {
				current.Content = strings.TrimSpace(current.Content)
				sections = append(sections, *current)
			}
			name := strings.TrimPrefix(line, "## ")
			current = &Section{
				Name:   name,
				IsCore: isCoreSectionHeader(name),
			}
			continue
		}
		if current != nil {
			current.Content += line + "\n"
		}
	}
	if current != nil {
		current.Content = strings.TrimSpace(current.Content)
		sections = append(sections, *current)
	}
	return sections
}

func isCoreSectionHeader(name string) bool {
	return strings.Contains(name, "[CORE]")
}

// SelfEvolutionInstruction returns the instruction injected between
// VOICE.md content and the task prompt. It references the workspace copy.
func SelfEvolutionInstruction() string {
	return `[The personality above is your voice file. You may update non-[CORE] sections of
.anthem/VOICE.md in your workspace if you discover persistent patterns about the user's
preferences or working style. Do not modify sections marked [CORE].]`
}
