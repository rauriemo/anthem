package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (s *StoryStore) Validate() []ValidationError {
	dir := s.storyDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []ValidationError{{Level: "error", Message: fmt.Sprintf("cannot read story directory: %v", err)}}
	}

	var errs []ValidationError
	allIDs := make(map[string]string) // id -> filename
	charIDs := make(map[string]bool)
	worldIDs := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		_, sections := parseSections(string(data))

		localIDs := make(map[string]bool)
		for _, sec := range sections {
			if sec.ID == "" {
				errs = append(errs, ValidationError{
					Level:   "error",
					File:    entry.Name(),
					Message: "section content without an <!-- id: --> anchor",
				})
				continue
			}

			if localIDs[sec.ID] {
				errs = append(errs, ValidationError{
					Level:   "error",
					File:    entry.Name(),
					Section: sec.ID,
					Message: fmt.Sprintf("duplicate section ID %q within file", sec.ID),
				})
			}
			localIDs[sec.ID] = true

			if prevFile, exists := allIDs[sec.ID]; exists {
				errs = append(errs, ValidationError{
					Level:   "error",
					File:    entry.Name(),
					Section: sec.ID,
					Message: fmt.Sprintf("duplicate section ID %q (also in %s)", sec.ID, prevFile),
				})
			}
			allIDs[sec.ID] = entry.Name()

			if entry.Name() == "characters.md" {
				charIDs[sec.ID] = true
			}
			if entry.Name() == "world.md" {
				worldIDs[sec.ID] = true
			}

			if sec.Heading == "" {
				errs = append(errs, ValidationError{
					Level:   "warning",
					File:    entry.Name(),
					Section: sec.ID,
					Message: "section has anchor but no heading",
				})
			}

			bodyContent := sec.Content
			if idx := strings.Index(bodyContent, "\n"); idx >= 0 {
				bodyContent = bodyContent[idx:]
			}
			if idx := strings.Index(bodyContent, "\n"); idx >= 0 {
				bodyContent = bodyContent[idx:]
			}
			trimmed := strings.TrimSpace(bodyContent)
			if trimmed == "" || trimmed == sec.Heading {
				errs = append(errs, ValidationError{
					Level:   "warning",
					File:    entry.Name(),
					Section: sec.ID,
					Message: "empty section (anchor + heading but no body)",
				})
			}

			wordCount := len(strings.Fields(sec.Content))
			if wordCount > 2000 {
				errs = append(errs, ValidationError{
					Level:   "info",
					File:    entry.Name(),
					Section: sec.ID,
					Message: fmt.Sprintf("section is %d words (consider splitting)", wordCount),
				})
			}

			if !isSnakeCase(sec.ID) {
				errs = append(errs, ValidationError{
					Level:   "info",
					File:    entry.Name(),
					Section: sec.ID,
					Message: fmt.Sprintf("section ID %q is not snake_case", sec.ID),
				})
			}
		}

		totalWords := 0
		for _, sec := range sections {
			totalWords += len(strings.Fields(sec.Content))
		}
		if totalWords > 15000 {
			errs = append(errs, ValidationError{
				Level:   "info",
				File:    entry.Name(),
				Message: fmt.Sprintf("file total is %d words (approaching prompt budget)", totalWords),
			})
		}
	}

	return errs
}

func isSnakeCase(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	_ = utf8.RuneCountInString(s)
	return true
}
