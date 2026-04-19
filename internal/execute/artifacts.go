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
	UpdatedAt string `yaml:"updated_at"`
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
//
// Matching model (diff-by-fingerprint, not time-diff):
//
// MarkStepStart snapshots every artifact currently in artifacts.yaml
// keyed by ID, capturing a fingerprint of (updated_at, status, path,
// file mtime, file size). Collect surfaces an entry whenever:
//
//  1. The ID is absent from the snapshot (newly appended), OR
//  2. The fingerprint differs from the snapshot (mutated in place --
//     e.g. a Revise cycle where the agent regenerates the file and
//     updates the same entry instead of adding a new one).
//
// This avoids three failure modes of the original time-based approach:
//
//   - Agents stamp `created_by` with their agent id (e.g. "miyazaki"),
//     never the step id ("s1"), so CreatedBy==stepID always misses.
//   - Agents fabricate `created_at` (planning timestamps, backdated
//     adoptions), so `t.After(marker)` is unreliable.
//   - On Revise, agents overwrite the file and bump the existing entry
//     rather than appending a new one. A pure ID-set diff misses this
//     entirely; the file-mtime component of the fingerprint catches it
//     even when the yaml entry is otherwise unchanged.
//
// The legacy CreatedBy==stepID path is retained as a fallback for test
// harnesses that stamp step ids directly.
type ContextArtifactProvider struct {
	projectRoot string
	feature     string

	mu             sync.Mutex
	collectMarkers map[string]time.Time                       // stepID -> wall-clock fallback marker
	entrySnapshots map[string]map[string]contextSnapshotEntry // stepID -> id -> fingerprint at snapshot time
}

// contextSnapshotEntry is the fingerprint we compare across MarkStepStart
// and Collect. Any field change (or the file being rewritten) flips the
// "changed since snapshot" bit and surfaces the artifact to the gate.
// file_mtime is the most forgery-resistant signal because it reflects
// what the agent actually did on disk, independent of yaml metadata.
type contextSnapshotEntry struct {
	updatedAt string
	status    string
	path      string
	fileMTime int64 // unix nanoseconds; 0 when the file doesn't exist yet
	fileSize  int64 // bytes; 0 when the file doesn't exist yet
}

func NewContextArtifactProvider(projectRoot, feature string) *ContextArtifactProvider {
	return &ContextArtifactProvider{
		projectRoot:    projectRoot,
		feature:        feature,
		collectMarkers: make(map[string]time.Time),
		entrySnapshots: make(map[string]map[string]contextSnapshotEntry),
	}
}

// MarkStepStart snapshots the current artifacts.yaml as a map of
// ID -> fingerprint (updated_at, status, path, file mtime, file size).
// Collect uses this snapshot as the primary filter: entries whose
// fingerprints differ (or whose IDs are absent) belong to this step.
// The wall-clock marker is retained as a fallback for callers that
// bypass MarkStepStart but still stamp trustworthy `created_at` values.
func (p *ContextArtifactProvider) MarkStepStart(stepID string) {
	snap := p.readArtifactFingerprints()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.collectMarkers[stepID] = time.Now()
	p.entrySnapshots[stepID] = snap
}

// readArtifactFingerprints returns the current ID -> fingerprint map
// from artifacts.yaml. Missing file / parse errors return an empty map
// so the first step in a fresh feature still produces a meaningful
// diff.
func (p *ContextArtifactProvider) readArtifactFingerprints() map[string]contextSnapshotEntry {
	out := make(map[string]contextSnapshotEntry)
	path := filepath.Join(p.featureDir(), "artifacts.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var file contextArtifactsFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return out
	}
	for _, e := range file.Artifacts {
		if e.ID == "" {
			continue
		}
		out[e.ID] = p.fingerprintFor(e)
	}
	return out
}

// fingerprintFor computes the snapshot fingerprint for a single entry.
// File stats are read through the project root so relative paths in
// artifacts.yaml resolve to the same disk location as an external
// consumer would see. A missing file yields zero mtime/size; that's
// a valid "absent" fingerprint that will still compare unequal to any
// post-step state where the file exists.
func (p *ContextArtifactProvider) fingerprintFor(e contextArtifactEntry) contextSnapshotEntry {
	fp := contextSnapshotEntry{
		updatedAt: e.UpdatedAt,
		status:    e.Status,
		path:      e.Path,
	}
	if e.Path != "" {
		full := filepath.Join(p.projectRoot, filepath.FromSlash(e.Path))
		if info, err := os.Stat(full); err == nil {
			fp.fileMTime = info.ModTime().UnixNano()
			fp.fileSize = info.Size()
		}
	}
	return fp
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
	snapshot := p.entrySnapshots[stepID]
	p.mu.Unlock()

	var arts []StepArtifact
	seen := make(map[string]bool)
	appendArtifact := func(e contextArtifactEntry) {
		// Deduplicate by ID in case two filters match the same entry
		// (e.g. stepID-as-created_by AND new-since-snapshot).
		if e.ID != "" && seen[e.ID] {
			return
		}
		if e.ID != "" {
			seen[e.ID] = true
		}
		arts = append(arts, StepArtifact{
			StepID:     stepID,
			Path:       e.Path,
			Kind:       e.Type,
			Summary:    e.Desc,
			ArtifactID: NewArtifactID(stepID, e.Path),
		})
	}

	for _, e := range file.Artifacts {
		if e.Status == "rejected" {
			continue
		}
		// Primary filter: fingerprint diff. An entry surfaces when either
		//   * the ID is new since the snapshot (appended during the step), or
		//   * the ID existed but the fingerprint has changed (mutated in
		//     place -- common on Revise when the agent overwrites the file
		//     and re-uses the existing artifacts.yaml entry).
		// The file-mtime component of the fingerprint is the key signal:
		// it catches regenerated binary payloads even when the agent
		// leaves every yaml field unchanged.
		if snapshot != nil && e.ID != "" {
			prev, existed := snapshot[e.ID]
			curr := p.fingerprintFor(e)
			if !existed || prev != curr {
				appendArtifact(e)
				continue
			}
		}
		// Legacy fallback: test harnesses and tooling that stamp the
		// step id in `created_by` still work.
		if e.CreatedBy == stepID {
			appendArtifact(e)
			continue
		}
		// Last-resort fallback for callers that don't MarkStepStart (so
		// snapshot is nil) but do stamp trustworthy created_at values.
		// Only used when there's no snapshot to compare against.
		if snapshot == nil && !marker.IsZero() && e.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil && t.After(marker) {
				appendArtifact(e)
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
		refs[i] = upstreamRef{StepID: a.StepID, Path: a.Path, Kind: a.Kind, Summary: a.Summary}
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

// defaultIgnoredDirBasenames are directory basenames skipped at any depth
// because they are never agent-authored output:
//   - VCS / agent metadata (.git, .context, .anthem)
//   - IDE metadata (.vscode, .vs, .idea, .cursor, .claude)
//   - Language package caches (node_modules)
//
// These are dot-dirs or globally-recognized tooling dirs where a basename
// match is safe even deep in the tree. Unity/runtime caches are matched by
// project-relative path instead (defaultIgnoredTopLevelDirs) to avoid false
// positives on `Assets/ThirdParty/Library` etc.
var defaultIgnoredDirBasenames = map[string]bool{
	".git":         true,
	".context":     true,
	".anthem":      true,
	".vscode":      true,
	".vs":          true,
	".idea":        true,
	".cursor":      true,
	".claude":      true,
	"node_modules": true,
}

// defaultIgnoredTopLevelDirs are directories that only qualify as noise at
// the project root. Unity managed caches (Library, Temp, Logs,
// MemoryCaptures), build output (obj, bin, Build, Builds, dist, target),
// and per-user Unity settings live at the project root; the same names
// nested inside Assets/ could legitimately be agent output and must NOT be
// skipped. Matching is against `filepath.Rel(projectRoot, path)`.
var defaultIgnoredTopLevelDirs = map[string]bool{
	"Library":        true, // Unity managed asset/shader/burst cache
	"Temp":           true, // Unity editor temp
	"Logs":           true, // Unity logs
	"UserSettings":   true, // Unity per-user
	"MemoryCaptures": true, // Unity memory profiler
	"Build":          true, // Unity/engine build output
	"Builds":         true,
	"obj":            true, // .NET intermediate
	"bin":            true, // .NET binary
	"dist":           true, // JS/TS bundler output
	"target":         true, // Rust/Java build output
}

// defaultIgnoredFileBasenames are files rewritten by tooling during an
// agent run that must not count as agent output. `.mcp.json` is rewritten
// by Claude Code / MCP clients when the agent starts; OS metadata files
// (`.DS_Store`, `Thumbs.db`) appear whenever the OS indexes the project.
var defaultIgnoredFileBasenames = map[string]bool{
	".mcp.json": true,
	".DS_Store": true,
	"Thumbs.db": true,
}

// FilesystemArtifactProvider tracks file changes to detect artifacts produced
// by each step. Before a step runs, call MarkStepStart to snapshot the
// current state; Collect then returns files created or modified since.
type FilesystemArtifactProvider struct {
	projectRoot string
	// extraIgnoreDirBasenames augments defaultIgnoredDirBasenames with
	// project-specific dir names (matched at any depth). Populated via
	// AddIgnoreDirs so orchestrator config can tune noise filtering
	// without patching this package.
	extraIgnoreDirBasenames map[string]bool
	// extraIgnoreFileBasenames augments defaultIgnoredFileBasenames.
	extraIgnoreFileBasenames map[string]bool

	mu        sync.Mutex
	snapshots map[string]map[string]time.Time // stepID -> path -> modtime
}

func NewFilesystemArtifactProvider(projectRoot string) *FilesystemArtifactProvider {
	return &FilesystemArtifactProvider{
		projectRoot:              projectRoot,
		extraIgnoreDirBasenames:  make(map[string]bool),
		extraIgnoreFileBasenames: make(map[string]bool),
		snapshots:                make(map[string]map[string]time.Time),
	}
}

// AddIgnoreDirs extends the dir-basename ignore list. Use when a project
// has tool-specific cache directories not in the default list (e.g.
// `.gradle`, `.mvn`). Matching is by basename at any depth.
func (p *FilesystemArtifactProvider) AddIgnoreDirs(names ...string) {
	for _, n := range names {
		p.extraIgnoreDirBasenames[n] = true
	}
}

// AddIgnoreFiles extends the file-basename ignore list.
func (p *FilesystemArtifactProvider) AddIgnoreFiles(names ...string) {
	for _, n := range names {
		p.extraIgnoreFileBasenames[n] = true
	}
}

// shouldSkipDir reports whether a directory encountered during walk should
// be skipped entirely. Used in BOTH MarkStepStart and Collect so the
// snapshot and the diff see the same tree -- otherwise files we
// snapshotted would get reported as "new" on collection (because the
// snapshot path wasn't in the before map), producing phantom artifacts.
func (p *FilesystemArtifactProvider) shouldSkipDir(absPath string) bool {
	base := filepath.Base(absPath)
	if defaultIgnoredDirBasenames[base] || p.extraIgnoreDirBasenames[base] {
		return true
	}
	rel, err := filepath.Rel(p.projectRoot, absPath)
	if err != nil {
		return false
	}
	// rel is guaranteed not to have a leading separator. For a top-level
	// dir it equals the basename; for nested dirs it includes separators.
	if defaultIgnoredTopLevelDirs[rel] {
		return true
	}
	return false
}

// shouldSkipFile reports whether a file should be omitted from snapshots
// and artifact collection.
func (p *FilesystemArtifactProvider) shouldSkipFile(absPath string) bool {
	base := filepath.Base(absPath)
	return defaultIgnoredFileBasenames[base] || p.extraIgnoreFileBasenames[base]
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
			if p.shouldSkipDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if p.shouldSkipFile(path) {
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
			if p.shouldSkipDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if p.shouldSkipFile(path) {
			return nil
		}
		prevMod, existed := before[path]
		if !existed || info.ModTime().After(prevMod) {
			rel, _ := filepath.Rel(p.projectRoot, path)
			arts = append(arts, StepArtifact{
				StepID:     stepID,
				Path:       rel,
				Kind:       kindFromExt(filepath.Ext(path)),
				Summary:    "",
				ArtifactID: NewArtifactID(stepID, rel),
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
		refs[i] = upstreamRef{StepID: a.StepID, Path: a.Path, Kind: a.Kind, Summary: a.Summary}
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
