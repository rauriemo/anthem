package execute

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ArtifactProvider abstracts how step outputs are collected and how upstream
// artifacts are injected as context for the next step.
type ArtifactProvider interface {
	Collect(stepID string) ([]StepArtifact, error)
	Inject(stepID string, upstream []StepArtifact) error
}

// --- ContextArtifactProvider (reads/writes .context/features/{feature}/) ---

type contextArtifactsFile struct {
	SchemaVersion string                 `yaml:"schema_version"`
	Artifacts     []contextArtifactEntry `yaml:"artifacts"`
}

type contextArtifactEntry struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Path      string `yaml:"path"`
	CreatedBy string `yaml:"created_by"`
	CreatedAt string `yaml:"created_at"`
	Status    string `yaml:"status"`
	Desc      string `yaml:"description"`
}

type upstreamManifest struct {
	StepID    string        `yaml:"step_id"`
	Upstream  []upstreamRef `yaml:"upstream"`
	CreatedAt string        `yaml:"created_at"`
}

type upstreamRef struct {
	StepID  string `yaml:"step_id"`
	Path    string `yaml:"path"`
	Kind    string `yaml:"kind"`
	Summary string `yaml:"summary"`
}

// ContextArtifactProvider reads artifacts from the feature-context artifacts.yaml
// and writes upstream manifests so the next agent in the chain can see them.
type ContextArtifactProvider struct {
	projectRoot string
	feature     string

	mu             sync.Mutex
	collectMarkers map[string]time.Time // stepID -> timestamp before step ran
}

func NewContextArtifactProvider(projectRoot, feature string) *ContextArtifactProvider {
	return &ContextArtifactProvider{
		projectRoot:    projectRoot,
		feature:        feature,
		collectMarkers: make(map[string]time.Time),
	}
}

// MarkStepStart records the current time so Collect can filter artifacts
// created after this point for the given step.
func (p *ContextArtifactProvider) MarkStepStart(stepID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.collectMarkers[stepID] = time.Now()
}

func (p *ContextArtifactProvider) featureDir() string {
	return filepath.Join(p.projectRoot, ".context", "features", p.feature)
}

func (p *ContextArtifactProvider) Collect(stepID string) ([]StepArtifact, error) {
	path := filepath.Join(p.featureDir(), "artifacts.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading artifacts.yaml: %w", err)
	}

	var file contextArtifactsFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing artifacts.yaml: %w", err)
	}

	p.mu.Lock()
	marker := p.collectMarkers[stepID]
	p.mu.Unlock()

	var arts []StepArtifact
	for _, e := range file.Artifacts {
		if e.Status == "rejected" {
			continue
		}
		if e.CreatedBy == stepID {
			arts = append(arts, StepArtifact{
				StepID:  stepID,
				Path:    e.Path,
				Kind:    e.Type,
				Summary: e.Desc,
			})
			continue
		}
		if !marker.IsZero() && e.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil && t.After(marker) {
				arts = append(arts, StepArtifact{
					StepID:  stepID,
					Path:    e.Path,
					Kind:    e.Type,
					Summary: e.Desc,
				})
			}
		}
	}
	return arts, nil
}

func (p *ContextArtifactProvider) Inject(stepID string, upstream []StepArtifact) error {
	if len(upstream) == 0 {
		return nil
	}
	dir := p.featureDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating feature dir: %w", err)
	}

	refs := make([]upstreamRef, len(upstream))
	for i, a := range upstream {
		refs[i] = upstreamRef(a)
	}
	manifest := upstreamManifest{
		StepID:    stepID,
		Upstream:  refs,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling upstream manifest: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("step-%s-upstream.yaml", stepID))
	return os.WriteFile(path, data, 0644)
}

// --- FilesystemArtifactProvider (scans working directory for changes) ---

// FilesystemArtifactProvider tracks file changes to detect artifacts produced
// by each step. Before a step runs, call MarkStepStart to snapshot the
// current state; Collect then returns files created or modified since.
type FilesystemArtifactProvider struct {
	projectRoot string

	mu        sync.Mutex
	snapshots map[string]map[string]time.Time // stepID -> path -> modtime
}

func NewFilesystemArtifactProvider(projectRoot string) *FilesystemArtifactProvider {
	return &FilesystemArtifactProvider{
		projectRoot: projectRoot,
		snapshots:   make(map[string]map[string]time.Time),
	}
}

// MarkStepStart snapshots modification times for files in the project root so
// Collect can diff against them after the step completes.
func (p *FilesystemArtifactProvider) MarkStepStart(stepID string) {
	snap := make(map[string]time.Time)
	_ = filepath.Walk(p.projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "node_modules" || base == ".context" {
				return filepath.SkipDir
			}
			return nil
		}
		snap[path] = info.ModTime()
		return nil
	})

	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshots[stepID] = snap
}

func (p *FilesystemArtifactProvider) Collect(stepID string) ([]StepArtifact, error) {
	p.mu.Lock()
	before := p.snapshots[stepID]
	p.mu.Unlock()

	if before == nil {
		return nil, nil
	}

	var arts []StepArtifact
	_ = filepath.Walk(p.projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "node_modules" || base == ".context" {
				return filepath.SkipDir
			}
			return nil
		}
		prevMod, existed := before[path]
		if !existed || info.ModTime().After(prevMod) {
			rel, _ := filepath.Rel(p.projectRoot, path)
			arts = append(arts, StepArtifact{
				StepID:  stepID,
				Path:    rel,
				Kind:    kindFromExt(filepath.Ext(path)),
				Summary: "",
			})
		}
		return nil
	})
	return arts, nil
}

func (p *FilesystemArtifactProvider) Inject(stepID string, upstream []StepArtifact) error {
	if len(upstream) == 0 {
		return nil
	}
	dir := filepath.Join(p.projectRoot, ".context", "execution")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating execution context dir: %w", err)
	}

	refs := make([]upstreamRef, len(upstream))
	for i, a := range upstream {
		refs[i] = upstreamRef(a)
	}
	manifest := upstreamManifest{
		StepID:    stepID,
		Upstream:  refs,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling upstream manifest: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("step-%s-upstream.yaml", stepID))
	return os.WriteFile(path, data, 0644)
}

func kindFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".webp", ".svg", ".bmp":
		return "image"
	case ".mp4", ".webm", ".gif":
		return "animation"
	case ".unity", ".scene", ".prefab":
		return "scene"
	default:
		return "file"
	}
}
