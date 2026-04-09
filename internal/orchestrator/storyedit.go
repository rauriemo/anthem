package orchestrator

import (
	"regexp"
	"strings"
)

var storyEditBlockRe = regexp.MustCompile(
	`(?s)` + "```" + `story-edit` +
		`(?:\s+target="([^"]+)")?` +
		`(?:\s+section="([^"]+)")?` +
		`(?:\s+after="([^"]+)")?` +
		`(?:\s+rev="([^"]+)")?` +
		`\s*\n(.*?)\n` + "```",
)

type StoryEdit struct {
	Target  string // file in story/ (default "narrative.md")
	Section string // replace this section (mutually exclusive with After)
	After   string // insert after this section (mutually exclusive with Section)
	Rev     string // content hash for conflict detection (required with Section)
	Content string // new markdown content
}

func extractStoryEdits(response string) (explanation string, edits []StoryEdit, hasEdits bool) {
	matches := storyEditBlockRe.FindAllStringSubmatchIndex(response, -1)
	if len(matches) == 0 {
		return response, nil, false
	}

	edits = make([]StoryEdit, 0, len(matches))
	for _, m := range matches {
		target := ""
		if m[2] >= 0 {
			target = response[m[2]:m[3]]
		}
		if target == "" {
			target = "narrative.md"
		}

		section := ""
		if m[4] >= 0 {
			section = response[m[4]:m[5]]
		}

		after := ""
		if m[6] >= 0 {
			after = response[m[6]:m[7]]
		}

		rev := ""
		if m[8] >= 0 {
			rev = response[m[8]:m[9]]
		}

		content := ""
		if m[10] >= 0 {
			content = response[m[10]:m[11]]
		}

		edits = append(edits, StoryEdit{
			Target:  target,
			Section: section,
			After:   after,
			Rev:     rev,
			Content: strings.TrimSpace(content),
		})
	}

	stripped := storyEditBlockRe.ReplaceAllString(response, "")
	explanation = strings.TrimSpace(stripped)
	return explanation, edits, true
}
