package plans

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PlanStatus represents the lifecycle stage of a plan.
type PlanStatus string

const (
	StatusDraft    PlanStatus = "draft"
	StatusBuilding PlanStatus = "building"
	StatusDone     PlanStatus = "done"
	// StatusArchived marks a plan that the user has superseded via /newplan.
	// LatestDraft filters these out so a fresh draft is started on the next
	// Plan-mode message.
	StatusArchived PlanStatus = "archived"
)

// Frontmatter is the YAML header of a plan file.
//
// ID is a stable UUIDv4-shaped identifier generated on first Save and preserved
// across Update/SetStatus/rename. It decouples plan identity from the filesystem
// path so UI components (like Prism's display artifacts) can use a durable key.
// CompileGeneration, CompiledAt, and RequiredProfiles track the lifecycle of the
// adjacent <path>.execute.json sidecar produced by Execute mode's compiler.
type Frontmatter struct {
	ID                string     `yaml:"id,omitempty"`
	Project           string     `yaml:"project"`
	Status            PlanStatus `yaml:"status"`
	Title             string     `yaml:"title"`
	Created           time.Time  `yaml:"created"`
	Updated           time.Time  `yaml:"updated"`
	CompileGeneration int        `yaml:"compile_generation,omitempty"`
	CompiledAt        time.Time  `yaml:"compiled_at,omitempty"`
	RequiredProfiles  []string   `yaml:"required_profiles,omitempty"`
}

// Plan is a fully loaded plan with parsed frontmatter and markdown body.
type Plan struct {
	Path        string
	Frontmatter Frontmatter
	Body        string // markdown content after the frontmatter
}

// PlanMeta is a lightweight summary returned by List.
type PlanMeta struct {
	Path    string     `json:"path"`
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Status  PlanStatus `json:"status"`
	Updated time.Time  `json:"updated"`
}

// Store manages plan files under ~/.anthem/plans/{project}/.
type Store struct {
	root string // e.g. /home/user/.anthem/plans
}

// NewStore creates a Store rooted at homeDir/.anthem/plans and ensures the
// directory exists.
func NewStore(homeDir string) (*Store, error) {
	root := filepath.Join(homeDir, ".anthem", "plans")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("plans: create root: %w", err)
	}
	return &Store{root: root}, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func projectDir(projectSlug string) string {
	return slugify(strings.ReplaceAll(projectSlug, "/", "-"))
}

// Save writes a new plan file and returns its path. The new plan gets a fresh
// UUIDv4-shaped ID in its frontmatter.
func (s *Store) Save(projectSlug, title, body string) (string, error) {
	dir := filepath.Join(s.root, projectDir(projectSlug))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("plans: create project dir: %w", err)
	}

	now := time.Now().UTC()
	fm := Frontmatter{
		ID:      newPlanID(),
		Project: projectSlug,
		Status:  StatusDraft,
		Title:   title,
		Created: now,
		Updated: now,
	}

	slug := slugify(title)
	if slug == "" {
		slug = "plan"
	}
	filename := fmt.Sprintf("%s-%s.md", now.Format("20060102-150405"), slug)
	path := filepath.Join(dir, filename)

	if err := writePlan(path, fm, body); err != nil {
		return "", err
	}
	return path, nil
}

// Update overwrites the body of an existing plan and bumps Updated.
func (s *Store) Update(path, body string) error {
	plan, err := s.Load(path)
	if err != nil {
		return err
	}
	plan.Frontmatter.Updated = time.Now().UTC()
	plan.Body = body
	return writePlan(path, plan.Frontmatter, plan.Body)
}

// Load reads a plan file, parsing frontmatter and body. Legacy plans without
// an ID in their frontmatter are backfilled with a fresh UUID and persisted
// back to disk so subsequent loads see the stable identity.
func (s *Store) Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("plans: read: %w", err)
	}
	fm, body, err := parseFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("plans: parse %s: %w", filepath.Base(path), err)
	}
	if fm.ID == "" {
		fm.ID = newPlanID()
		if werr := writePlan(path, fm, body); werr != nil {
			// Non-fatal: surface the in-memory ID even if we couldn't persist.
			// The next successful write will pin it.
			_ = werr
		}
	}
	return &Plan{Path: path, Frontmatter: fm, Body: body}, nil
}

// List returns metadata for all plans in a project, sorted by Updated descending.
func (s *Store) List(projectSlug string) ([]PlanMeta, error) {
	dir := filepath.Join(s.root, projectDir(projectSlug))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("plans: list: %w", err)
	}

	var metas []PlanMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p, loadErr := s.Load(filepath.Join(dir, e.Name()))
		if loadErr != nil {
			continue
		}
		metas = append(metas, PlanMeta{
			Path:    p.Path,
			ID:      p.Frontmatter.ID,
			Title:   p.Frontmatter.Title,
			Status:  p.Frontmatter.Status,
			Updated: p.Frontmatter.Updated,
		})
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].Updated.After(metas[j].Updated)
	})
	return metas, nil
}

// SetStatus updates the status field of a plan and bumps Updated.
func (s *Store) SetStatus(path string, status PlanStatus) error {
	plan, err := s.Load(path)
	if err != nil {
		return err
	}
	plan.Frontmatter.Status = status
	plan.Frontmatter.Updated = time.Now().UTC()
	return writePlan(path, plan.Frontmatter, plan.Body)
}

// LatestDraft returns the most recently updated draft plan for a project, or nil.
func (s *Store) LatestDraft(projectSlug string) (*Plan, error) {
	metas, err := s.List(projectSlug)
	if err != nil {
		return nil, err
	}
	for _, m := range metas {
		if m.Status == StatusDraft {
			return s.Load(m.Path)
		}
	}
	return nil, nil
}

// --- frontmatter helpers ---

// newPlanID generates a UUIDv4-shaped identifier using crypto/rand. No external
// dependency is required.
func newPlanID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read from crypto/rand should never fail on supported platforms;
		// fall back to a timestamp-derived ID so callers never see an empty
		// string rather than crashing the agent.
		ts := time.Now().UTC().UnixNano()
		return fmt.Sprintf("plan-%x", ts)
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func writePlan(path string, fm Frontmatter, body string) error {
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("plans: marshal frontmatter: %w", err)
	}
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(fmBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString("\n")
		buf.WriteString(body)
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

func parseFrontmatter(content string) (Frontmatter, string, error) {
	var fm Frontmatter
	if !strings.HasPrefix(content, "---\n") {
		return fm, content, nil
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end == -1 {
		// Try trailing --- at EOF
		if strings.HasSuffix(rest, "\n---") {
			end = len(rest) - 3
		} else {
			return fm, content, nil
		}
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return fm, "", fmt.Errorf("invalid frontmatter YAML: %w", err)
	}
	body := strings.TrimPrefix(rest[end+4:], "\n")
	return fm, body, nil
}
