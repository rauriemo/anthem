package voice

import "fmt"

// EnforceCore checks that no [CORE] sections were modified between
// the original and updated voice configs. Returns an error if a
// core section was changed.
func EnforceCore(original, updated *VoiceConfig) error {
	origCore := coreSectionMap(original.Sections)
	updCore := coreSectionMap(updated.Sections)

	for name, origContent := range origCore {
		newContent, exists := updCore[name]
		if !exists {
			return fmt.Errorf("core section %q was removed", name)
		}
		if newContent != origContent {
			return fmt.Errorf("core section %q was modified", name)
		}
	}
	return nil
}

func coreSectionMap(sections []Section) map[string]string {
	m := make(map[string]string)
	for _, s := range sections {
		if s.IsCore {
			m[s.Name] = s.Content
		}
	}
	return m
}
