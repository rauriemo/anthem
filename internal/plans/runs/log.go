package runs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RunsDirSuffix is appended to a plan markdown path to get the directory
// holding its run logs. Using a suffix (rather than a sibling dir) keeps
// all per-plan state (markdown + compiled sidecar + runs) addressable
// from the single plan path, which is how the rest of the codebase
// already names compiled sidecars (<plan>.execute.json).
const RunsDirSuffix = ".runs"

// filenameTimeLayout is the timestamp encoding used in run filenames.
// Chosen to be filesystem-safe on Windows (no colons) and sortable
// lexicographically, so a plain directory listing yields runs in start
// order without a secondary sort.
const filenameTimeLayout = "20060102T150405.000Z"

// filenameRE matches the canonical run filename. Kept strict so stray
// files dropped into a .runs/ directory (e.g. editor swap files, OS
// metadata) are skipped by ListActive instead of crashing the replay.
var filenameRE = regexp.MustCompile(`^run_([0-9T\.Z]+)_gen(\d+)\.jsonl$`)

// RunsDir returns the directory holding run logs for the given plan
// markdown path.
func RunsDir(planPath string) string {
	return planPath + RunsDirSuffix
}

// NewRunLogPath allocates a fresh run log filename for the given plan
// at the given compile generation. The timestamp is included in the
// filename so multiple runs of the same plan at the same compile gen
// (e.g. rerun after abort) remain distinguishable on disk.
//
// The returned path is relative to RunsDir(planPath); the caller is
// expected to pass it straight into Append which creates the directory
// as needed on first write.
func NewRunLogPath(planPath string, compileGen int, startedAt time.Time) string {
	stamp := startedAt.UTC().Format(filenameTimeLayout)
	return filepath.Join(RunsDir(planPath), fmt.Sprintf("run_%s_gen%d.jsonl", stamp, compileGen))
}

// Append writes an event to the given run log, creating the file (and
// its parent directory) if necessary. The write opens the file with
// O_APPEND so concurrent writers never clobber each other; combined
// with single-line JSON this gives atomic append semantics on POSIX
// and the NTFS write cache.
func Append(runPath string, event Event) error {
	if runPath == "" {
		return errors.New("runs: empty run log path")
	}
	if err := os.MkdirAll(filepath.Dir(runPath), 0o755); err != nil {
		return fmt.Errorf("runs: create run dir: %w", err)
	}
	line, err := event.marshal()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(runPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("runs: open run log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("runs: append event: %w", err)
	}
	return nil
}

// Replay returns every event in a run log in file order. A truncated
// final line (from a partial write) is silently dropped rather than
// aborting the replay; the next Append will overwrite that truncated
// slot. This matches the "append-only, crash-safe" discipline where a
// partial event is equivalent to no event.
func Replay(runPath string) ([]Event, error) {
	f, err := os.Open(runPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	// Gate payloads with embedded ReviewSpec + artifacts can push a
	// single line above the 64 KiB default scanner buffer. Raise the
	// ceiling to 8 MiB so worst-case gates still replay; a log line
	// larger than that is pathological and we'd rather surface it.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// Tolerate a trailing partial line only: if we can't
			// decode further events after this one, assume the
			// writer was interrupted mid-line and stop cleanly.
			if isLastLineTruncation(f, scanner) {
				break
			}
			return events, fmt.Errorf("runs: decode event in %s: %w", filepath.Base(runPath), err)
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return events, fmt.Errorf("runs: read run log: %w", err)
	}
	return events, nil
}

// isLastLineTruncation is a best-effort probe that returns true when
// the scanner's current error is the last readable chunk of the file,
// meaning the bad line we just saw sits at EOF (classic truncation).
// Used to distinguish genuine log corruption from a tolerable partial
// tail after a crash.
func isLastLineTruncation(f *os.File, scanner *bufio.Scanner) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return false
	}
	// Scanner buffers ahead, so a trailing slack of one buffer width
	// still counts as "at EOF" for our purposes.
	return info.Size()-pos <= int64(len(scanner.Bytes())+2)
}

// ActiveRun describes a non-terminal run found under a project root.
// The fields are the bits callers typically need to reconstruct a
// PlanRunner or emit a plan_snapshot event without reopening the log.
type ActiveRun struct {
	RunPath           string    // absolute path to the .jsonl
	PlanPath          string    // plan markdown this run belongs to
	PlanID            string    // plan frontmatter ID recorded in run_started
	CompileGeneration int       // compile gen recorded in run_started and filename
	StartedAt         time.Time // timestamp of run_started (not filename)
	ProjectSlug       string    // project slug recorded in run_started
	TailType          EventType // last event type -- used for triage
}

// ListActive walks every .runs/ directory beneath projectRoot and
// returns the runs whose tail event is neither run_completed nor
// run_aborted. Empty/corrupt files are skipped rather than aborting
// the scan so a single bad log file doesn't starve the rest of the
// system at startup.
//
// projectRoot is expected to be a directory like
// ~/.anthem/plans/{project-slug}/; scanning the entire ~/.anthem/plans
// tree is a separate concern handled by the orchestrator iterating
// each project directory.
func ListActive(projectRoot string) ([]ActiveRun, error) {
	entries, err := os.ReadDir(projectRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runs: read project dir: %w", err)
	}

	var active []ActiveRun
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), RunsDirSuffix) {
			continue
		}
		runsDir := filepath.Join(projectRoot, e.Name())
		planPath := strings.TrimSuffix(runsDir, RunsDirSuffix)
		runFiles, err := os.ReadDir(runsDir)
		if err != nil {
			continue
		}
		for _, rf := range runFiles {
			if rf.IsDir() || !filenameRE.MatchString(rf.Name()) {
				continue
			}
			runPath := filepath.Join(runsDir, rf.Name())
			events, err := Replay(runPath)
			if err != nil || len(events) == 0 {
				continue
			}
			tail := events[len(events)-1]
			if tail.IsTerminal() {
				continue
			}
			head := events[0]
			gen := parseGenFromFilename(rf.Name())
			if head.Type == EventRunStarted && head.CompileGeneration != 0 {
				gen = head.CompileGeneration
			}
			active = append(active, ActiveRun{
				RunPath:           runPath,
				PlanPath:          planPath,
				PlanID:            head.PlanID,
				CompileGeneration: gen,
				StartedAt:         head.Timestamp,
				ProjectSlug:       head.ProjectSlug,
				TailType:          tail.Type,
			})
		}
	}

	// Stable order by start time so orchestrator emission is deterministic.
	sort.Slice(active, func(i, j int) bool {
		return active[i].StartedAt.Before(active[j].StartedAt)
	})
	return active, nil
}

// parseGenFromFilename extracts the compile generation embedded in the
// filename. Returns 0 when the filename is not in the expected format
// (the event body is the authoritative source anyway).
func parseGenFromFilename(name string) int {
	m := filenameRE.FindStringSubmatch(name)
	if len(m) != 3 {
		return 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0
	}
	return n
}

// DefaultHistoryLimit is the per-plan cap applied when ListAll is
// called without an explicit limit. At ~10 KB per reduced snapshot
// this keeps the first-hydrate wire payload well under a megabyte per
// plan even at pathological usage.
//
// Files older than the cap stay on disk untouched; only the fetch is
// capped. A future `anthem runs prune` subcommand can delete them in
// bulk when operators want to reclaim space.
const DefaultHistoryLimit = 50

// RunEntry is the disk-paired projection ListAll returns: a reduced
// Snapshot plus the file paths that produced it. Consumers that only
// need wire-level data can ignore the paths; startup triage and future
// prune commands need them.
type RunEntry struct {
	RunPath  string   // absolute path to the .jsonl
	PlanPath string   // plan markdown this run belongs to
	Snapshot Snapshot // replay-reduced view of the run log
}

// ListAll walks every .runs/ directory beneath projectRoot, replays
// each log into a Snapshot, and returns the combined list newest-first
// by Snapshot.StartedAt. Unlike ListActive, terminal (completed/
// aborted) runs are included -- this is the hydration feed that
// populates Prism's scrollable Execute history.
//
// limit caps the number of runs returned PER PLAN DIRECTORY (i.e. per
// .plan.md.runs/), not per project. A project with three plans each
// holding 120 log files returns up to 3*limit entries; a project with
// one plan and 120 log files returns up to limit entries. Pass <=0 or
// DefaultHistoryLimit to accept the default cap. Pass math.MaxInt (or
// any very large number) to effectively disable the cap.
//
// Files older than the per-plan cap remain on disk untouched; they
// are simply skipped during the scan. This keeps the wire payload
// bounded while preserving forensic access on the filesystem.
//
// Corrupt or empty log files are skipped rather than aborting the
// walk, matching ListActive's discipline: a single bad file must
// never starve the rest of the history.
func ListAll(projectRoot string, limit int) ([]RunEntry, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}

	entries, err := os.ReadDir(projectRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runs: read project dir: %w", err)
	}

	var all []RunEntry
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), RunsDirSuffix) {
			continue
		}
		runsDir := filepath.Join(projectRoot, e.Name())
		planPath := strings.TrimSuffix(runsDir, RunsDirSuffix)

		perPlan, err := listPlanRuns(runsDir, planPath)
		if err != nil {
			continue
		}
		// Newest first per plan directory.
		sort.Slice(perPlan, func(i, j int) bool {
			return perPlan[i].Snapshot.StartedAt.After(perPlan[j].Snapshot.StartedAt)
		})
		if len(perPlan) > limit {
			perPlan = perPlan[:limit]
		}
		all = append(all, perPlan...)
	}

	// Global ordering so multi-plan projects present a single
	// newest-first timeline to the UI; the per-plan cap is already
	// applied above.
	sort.Slice(all, func(i, j int) bool {
		return all[i].Snapshot.StartedAt.After(all[j].Snapshot.StartedAt)
	})
	return all, nil
}

// listPlanRuns replays every run log under a single .runs/ directory
// into a RunEntry. Returned entries are in no particular order; the
// caller is expected to sort.
func listPlanRuns(runsDir, planPath string) ([]RunEntry, error) {
	runFiles, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, err
	}
	var out []RunEntry
	for _, rf := range runFiles {
		if rf.IsDir() || !filenameRE.MatchString(rf.Name()) {
			continue
		}
		runPath := filepath.Join(runsDir, rf.Name())
		snap, err := ReplayToSnapshot(runPath)
		if err != nil {
			// Corrupt / empty log -- skip, don't abort the walk.
			continue
		}
		out = append(out, RunEntry{
			RunPath:  runPath,
			PlanPath: planPath,
			Snapshot: snap,
		})
	}
	return out, nil
}
