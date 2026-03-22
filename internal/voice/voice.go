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
	return `[The personality above is your voice file. You may update ~/.anthem/VOICE.md if you discover persistent patterns about the user preferences, communication style, or working habits.]`
}
