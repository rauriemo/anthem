package voice

import "strings"

// Merge applies section changes from updated into original.
// Preserves ordering from original, updates matching sections, appends new ones.
// Raw is reconstructed from the merged sections.
func Merge(original, updated *VoiceConfig) *VoiceConfig {
	if original == nil && updated == nil {
		return &VoiceConfig{}
	}
	if original == nil {
		return updated
	}
	if updated == nil {
		return original
	}

	origMap := sectionMap(original.Sections)
	updMap := sectionMap(updated.Sections)

	var merged []Section

	for _, s := range original.Sections {
		if upd, ok := updMap[s.Name]; ok {
			merged = append(merged, upd)
		} else {
			merged = append(merged, s)
		}
	}

	for _, s := range updated.Sections {
		if _, exists := origMap[s.Name]; !exists {
			merged = append(merged, s)
		}
	}

	return &VoiceConfig{
		Raw:      renderSections(merged),
		Sections: merged,
	}
}

func renderSections(sections []Section) string {
	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## ")
		b.WriteString(s.Name)
		b.WriteByte('\n')
		b.WriteString(s.Content)
	}
	return b.String()
}

func sectionMap(sections []Section) map[string]Section {
	m := make(map[string]Section)
	for _, s := range sections {
		m[s.Name] = s
	}
	return m
}
