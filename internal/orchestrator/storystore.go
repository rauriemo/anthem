package orchestrator

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	ErrStaleRevision     = errors.New("stale revision: content changed since last read")
	ErrSectionIDNotFound = errors.New("section ID not found in file")
	ErrMutuallyExclusive = errors.New("section and after are mutually exclusive")
	ErrPathTraversal     = errors.New("path traversal detected")
	ErrOutsideStoryDir   = errors.New("target file outside story/ directory")
	ErrAfterNotFound     = errors.New("after section not found")
	ErrDuplicateID       = errors.New("duplicate section ID")
	ErrMissingAnchor     = errors.New("content without section anchor")
)

var sectionAnchorRe = regexp.MustCompile(`<!--\s*id:\s*(\S+)\s*-->`)

type Section struct {
	ID      string
	Heading string
	Content string
	Hash    string
}

type StoryContext struct {
	Config   string
	Files    map[string]string
	Sections map[string]map[string]string // filename -> sectionID -> hash
}

type ValidationError struct {
	Level   string // "error", "warning", "info"
	File    string
	Section string
	Message string
}

type sectionMeta struct {
	Status   string `yaml:"status"`
	EditedBy string `yaml:"edited_by"`
	EditedAt string `yaml:"edited_at"`
}

type storyFrontmatter struct {
	Title    string                 `yaml:"title"`
	Status   string                 `yaml:"status"`
	Sections map[string]sectionMeta `yaml:"sections"`
}

type StoryStore struct {
	projectRoot string
	mu          sync.Mutex
}

func NewStoryStore(projectRoot string) *StoryStore {
	return &StoryStore{projectRoot: projectRoot}
}

func (s *StoryStore) storyDir() string {
	return filepath.Join(s.projectRoot, "story")
}

func (s *StoryStore) validatePath(target string) error {
	if strings.Contains(target, "..") || filepath.IsAbs(target) {
		return ErrPathTraversal
	}
	cleaned := filepath.Clean(target)
	if strings.HasPrefix(cleaned, "..") {
		return ErrPathTraversal
	}
	return nil
}

func (s *StoryStore) filePath(target string) string {
	return filepath.Join(s.storyDir(), target)
}

func (s *StoryStore) CheckRevision(edit StoryEdit) error {
	if edit.Section == "" {
		return nil
	}

	if err := s.validatePath(edit.Target); err != nil {
		return err
	}

	data, err := os.ReadFile(s.filePath(edit.Target))
	if err != nil {
		return fmt.Errorf("reading story file: %w", err)
	}

	_, sections := parseSections(string(data))
	for _, sec := range sections {
		if sec.ID == edit.Section {
			if edit.Rev == "" {
				return nil
			}
			if sec.Hash != edit.Rev {
				return ErrStaleRevision
			}
			return nil
		}
	}

	return ErrSectionIDNotFound
}

func (s *StoryStore) Apply(edit StoryEdit, editedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if edit.Section != "" && edit.After != "" {
		return ErrMutuallyExclusive
	}

	if err := s.validatePath(edit.Target); err != nil {
		return err
	}

	path := s.filePath(edit.Target)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading story file: %w", err)
	}

	content := string(data)
	fm, sections := parseSections(content)

	var newSectionID string

	if edit.Section != "" {
		found := false
		for i, sec := range sections {
			if sec.ID == edit.Section {
				sections[i].Content = edit.Content
				newSectionID = edit.Section
				found = true
				break
			}
		}
		if !found {
			return ErrSectionIDNotFound
		}
	} else if edit.After != "" {
		found := false
		for i, sec := range sections {
			if sec.ID == edit.After {
				newSec := Section{Content: edit.Content}
				if m := sectionAnchorRe.FindStringSubmatch(edit.Content); len(m) > 1 {
					newSec.ID = m[1]
					newSectionID = m[1]
				}
				after := make([]Section, 0, len(sections)+1)
				after = append(after, sections[:i+1]...)
				after = append(after, newSec)
				after = append(after, sections[i+1:]...)
				sections = after
				found = true
				break
			}
		}
		if !found {
			return ErrAfterNotFound
		}
	} else {
		newSec := Section{Content: edit.Content}
		if m := sectionAnchorRe.FindStringSubmatch(edit.Content); len(m) > 1 {
			newSec.ID = m[1]
			newSectionID = m[1]
		}
		sections = append(sections, newSec)
	}

	fmData := updateFrontmatter(fm, newSectionID, editedBy)
	result := reassemble(fmData, sections)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(result), 0644); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming story file: %w", err)
	}

	return nil
}

func (s *StoryStore) ReadContext() (StoryContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := StoryContext{
		Files:    make(map[string]string),
		Sections: make(map[string]map[string]string),
	}

	dir := s.storyDir()

	configData, err := os.ReadFile(filepath.Join(dir, "context.yaml"))
	if err == nil {
		ctx.Config = string(configData)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ctx, fmt.Errorf("reading story directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		ctx.Files[entry.Name()] = string(data)

		_, sections := parseSections(string(data))
		hashes := make(map[string]string, len(sections))
		for _, sec := range sections {
			if sec.ID != "" {
				hashes[sec.ID] = sec.Hash
			}
		}
		ctx.Sections[entry.Name()] = hashes
	}

	return ctx, nil
}

func parseSections(markdown string) (frontmatter string, sections []Section) {
	fm, body := splitStoryFrontmatter(markdown)
	if body == "" {
		return fm, nil
	}

	locs := sectionAnchorRe.FindAllStringIndex(body, -1)
	if len(locs) == 0 {
		return fm, nil
	}

	for i, loc := range locs {
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(body)
		}

		chunk := body[loc[0]:end]
		m := sectionAnchorRe.FindStringSubmatch(chunk)
		id := ""
		if len(m) > 1 {
			id = m[1]
		}

		heading := ""
		lines := strings.SplitN(chunk, "\n", 3)
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "##") {
				heading = trimmed
				break
			}
		}

		content := strings.TrimSpace(chunk)
		sec := Section{
			ID:      id,
			Heading: heading,
			Content: content,
			Hash:    computeHash(content),
		}
		sections = append(sections, sec)
	}

	return fm, sections
}

func computeHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h[:4])
}

func splitStoryFrontmatter(content string) (fm string, body string) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return "", trimmed
	}

	rest := trimmed[3:]
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return trimmed, ""
	}

	fmContent := rest[:idx]
	afterClose := rest[idx+4:]
	if len(afterClose) > 0 && afterClose[0] == '\r' {
		afterClose = afterClose[1:]
	}
	if len(afterClose) > 0 && afterClose[0] == '\n' {
		afterClose = afterClose[1:]
	}

	return "---\n" + fmContent + "\n---", strings.TrimSpace(afterClose)
}

func updateFrontmatter(fm string, sectionID string, editedBy string) string {
	if fm == "" || sectionID == "" {
		return fm
	}

	inner := strings.TrimPrefix(fm, "---\n")
	inner = strings.TrimSuffix(inner, "\n---")

	var sfm storyFrontmatter
	if err := yaml.Unmarshal([]byte(inner), &sfm); err != nil {
		return fm
	}

	if sfm.Sections == nil {
		sfm.Sections = make(map[string]sectionMeta)
	}

	sfm.Sections[sectionID] = sectionMeta{
		Status:   "draft",
		EditedBy: editedBy,
		EditedAt: time.Now().UTC().Format(time.RFC3339),
	}

	out, err := yaml.Marshal(&sfm)
	if err != nil {
		return fm
	}

	return "---\n" + strings.TrimSpace(string(out)) + "\n---"
}

func reassemble(fm string, sections []Section) string {
	var sb strings.Builder
	if fm != "" {
		sb.WriteString(fm)
		sb.WriteString("\n\n")
	}
	for i, sec := range sections {
		sb.WriteString(sec.Content)
		if i < len(sections)-1 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
