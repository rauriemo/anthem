package voice

// Merge applies non-[CORE] section changes from updated into original.
// [CORE] sections are always preserved from original.
func Merge(original, updated *VoiceConfig) *VoiceConfig {
	origMap := sectionMap(original.Sections)
	updMap := sectionMap(updated.Sections)

	var merged []Section

	// Preserve ordering from original, updating non-core sections
	for _, s := range original.Sections {
		if s.IsCore {
			merged = append(merged, s)
			continue
		}
		if upd, ok := updMap[s.Name]; ok {
			merged = append(merged, upd)
		} else {
			merged = append(merged, s)
		}
	}

	// Append new sections from updated that don't exist in original
	for _, s := range updated.Sections {
		if _, exists := origMap[s.Name]; !exists && !s.IsCore {
			merged = append(merged, s)
		}
	}

	return &VoiceConfig{Sections: merged}
}

func sectionMap(sections []Section) map[string]Section {
	m := make(map[string]Section)
	for _, s := range sections {
		m[s.Name] = s
	}
	return m
}
