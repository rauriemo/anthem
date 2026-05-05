package httpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"log/slog"

	// xdraw.CatmullRom is a higher-quality resampling kernel than anything in
	// stdlib image/draw (which only offers NearestNeighbor and Src composites
	// via image.Draw). Needed by NormalizeFramesProcessor's scale_factor
	// downscale path so alpha edges stay soft when packing large Veo frames
	// into a Unity-safe sprite sheet. Aliased to avoid clashing with the
	// stdlib `draw` already imported above for the bbox-centering copy.
	xdraw "golang.org/x/image/draw"

	"github.com/rauriemo/anthem/internal/guests"
)

// PostProcessStatus represents the outcome of a post-processing operation.
type PostProcessStatus string

const (
	PostProcessApplied PostProcessStatus = "applied"
	PostProcessSkipped PostProcessStatus = "skipped"
	PostProcessFailed  PostProcessStatus = "failed"
)

// PostProcessResult is the structured outcome of a single post-processing step.
type PostProcessResult struct {
	Op      string
	Status  PostProcessStatus
	Message string
}

// PipelineState carries intermediate data between chained post-process ops.
// Each op may read from and write to this map to coordinate with downstream ops.
type PipelineState map[string]string

// PostProcessor applies a named post-processing operation to an artifact file.
// The state map is shared across all ops in a single RunPostProcess invocation.
type PostProcessor interface {
	Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult
}

var processors = map[string]PostProcessor{
	"chroma_key":              &ChromaKeyProcessor{},
	"birefnet_matte":          &BiRefNetMatteProcessor{},
	"binarize_alpha":          &BinarizeAlphaProcessor{},
	"plate_speck_cleanup":     &PlateSpeckCleanupProcessor{},
	"extract_video_frames":    &ExtractVideoFramesProcessor{},
	"video_matte":             &VideoMatteProcessor{},
	"check_frame_consistency": &CheckFrameConsistencyProcessor{},
	"normalize_frames":        &NormalizeFramesProcessor{},
	"stitch_spritesheet":      &StitchSpritesheetProcessor{},
}

// RunPostProcess executes an ordered list of post-processing operations on the
// artifact at artifactPath. A shared PipelineState is passed through all ops so
// they can communicate intermediate paths. Failures are logged but never prevent
// subsequent operations or the overall artifact save.
func RunPostProcess(ops []guests.PostProcessOp, artifactPath string, log *slog.Logger) ([]PostProcessResult, PipelineState) {
	state := PipelineState{}
	log.Info("RunPostProcess called", "ops", len(ops), "artifact", artifactPath)
	if len(ops) == 0 {
		return nil, state
	}
	var results []PostProcessResult
	for _, op := range ops {
		p, ok := processors[op.Op]
		if !ok {
			r := PostProcessResult{Op: op.Op, Status: PostProcessSkipped, Message: "unknown operation"}
			log.Warn("unknown post_process operation, skipping", "op", op.Op)
			results = append(results, r)
			continue
		}
		r := p.Run(artifactPath, op.Config, state, log)
		if r.Status == PostProcessFailed {
			log.Warn("post_process operation failed, artifact saved without it",
				"op", op.Op, "message", r.Message)
		}
		results = append(results, r)
	}
	return results, state
}

// FormatResults appends human-readable post-process status lines to msg.
func FormatResults(msg string, results []PostProcessResult) string {
	for _, r := range results {
		msg += fmt.Sprintf("\n  -> %s: %s (%s)", r.Op, r.Status, r.Message)
	}
	return msg
}

// runSidecarCmd starts a sidecar process, waits for OS-level exit, and returns
// captured stdout/stderr.
//
// Background: exec.Cmd.Run() / Cmd.Wait() block until both the child exits AND
// the stdout/stderr pipes hit EOF. On Windows, Python sidecars that load
// PyTorch + CUDA (e.g. SAM 2 in matte.py) routinely spawn worker processes
// that inherit the parent's stdio handles and outlive the parent. Those
// inherited handles keep our pipes open even after the sidecar process is
// gone, so Cmd.Wait() blocks indefinitely. The hang is intermittent because
// Python's `multiprocessing` "spawn" start method on Windows sometimes leaks
// handles and sometimes doesn't, depending on whether worker pools are
// reused, GPU memory state, etc.
//
// Workaround: redirect stdio to OS-owned temp files (so Go owns no pipes
// itself) and wait via cmd.Process.Wait() rather than cmd.Wait(). The former
// returns as soon as the OS reports the child has exited and is independent
// of any descriptor still being held by orphaned grandchildren. The temp
// files capture everything the sidecar wrote up to its exit point.
//
// The caller-supplied ctx still bounds total wall time. On context expiry
// the process is killed and ctx.Err() is returned.
func runSidecarCmd(ctx context.Context, cmd *exec.Cmd) (stdout, stderr []byte, err error) {
	stdoutFile, ferr := os.CreateTemp("", "sidecar-stdout-*.log")
	if ferr != nil {
		return nil, nil, fmt.Errorf("creating stdout temp: %w", ferr)
	}
	defer func() {
		_ = stdoutFile.Close()
		_ = os.Remove(stdoutFile.Name())
	}()

	stderrFile, ferr := os.CreateTemp("", "sidecar-stderr-*.log")
	if ferr != nil {
		return nil, nil, fmt.Errorf("creating stderr temp: %w", ferr)
	}
	defer func() {
		_ = stderrFile.Close()
		_ = os.Remove(stderrFile.Name())
	}()

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	if serr := cmd.Start(); serr != nil {
		return nil, nil, fmt.Errorf("starting sidecar: %w", serr)
	}

	type waitResult struct {
		state *os.ProcessState
		err   error
	}
	done := make(chan waitResult, 1)
	go func() {
		state, werr := cmd.Process.Wait()
		done <- waitResult{state: state, err: werr}
	}()

	select {
	case res := <-done:
		switch {
		case res.err != nil:
			err = res.err
		case res.state != nil && !res.state.Success():
			err = fmt.Errorf("exit code %d", res.state.ExitCode())
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		err = ctx.Err()
	}

	if _, serr := stdoutFile.Seek(0, 0); serr == nil {
		stdout, _ = io.ReadAll(stdoutFile)
	}
	if _, serr := stderrFile.Seek(0, 0); serr == nil {
		stderr, _ = io.ReadAll(stderrFile)
	}
	return stdout, stderr, err
}

// ---------------------------------------------------------------------------
// Shared matte-sidecar locator
// ---------------------------------------------------------------------------

// matteSidecar captures the resolved python + matte.py paths used by every
// processor that shells out to tools/matte/matte.py. Keeping this in a struct
// (rather than two return values) keeps the caller sites readable when we
// start passing the resolved location into exec.CommandContext.
type matteSidecar struct {
	Python string
	Script string
}

// locateMattePython resolves how to invoke the matting sidecar.
//
// Python binary (first hit wins):
//  1. MATTE_PYTHON env var — explicit override, used even when the named
//     interpreter is not on PATH (e.g. a venv interpreter).
//  2. lookPath("python3")
//  3. lookPath("python")
//
// Script path (first existing file wins; others are recorded in the error
// message if every candidate misses):
//  1. MATTE_SCRIPT env var — absolute path to matte.py.
//  2. ANTHEM_REPO_ROOT + "tools/matte/matte.py" — install-scoped pointer to
//     the anthem source tree. This MUST be a distinct env var from
//     ANTHEM_HTTP_BRIDGE_ROOT, which is the *project* sandbox root set per
//     downstream project by callers like Prism. Re-using the bridge-root
//     var caused a naming collision that silently skipped matting when
//     anthem was spawned with cwd inside a downstream project.
//  3. Walk up from os.Executable() directory checking
//     "<dir>/tools/matte/matte.py" at each level (handles source-tree
//     builds like `go run ./cmd/anthem`; `go install`-ed binaries that live
//     far from the source tree can't resolve via this path and must rely on
//     MATTE_SCRIPT or ANTHEM_REPO_ROOT). Bounded to 8 levels so a
//     misconfigured installation can't cause an unbounded stat storm.
//  4. os.Getwd() + "tools/matte/matte.py" — preserves the original
//     dev-from-repo-root ergonomics for contributors running `go run` from
//     the anthem repo root.
//
// Still skip-friendly: processors emit PostProcessSkipped instead of
// PostProcessFailed when this returns an error — matting is optional tooling
// and a missing sidecar should not break unrelated tool calls.
func locateMattePython(lookPath func(file string) (string, error)) (*matteSidecar, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	python := os.Getenv("MATTE_PYTHON")
	if python == "" {
		for _, candidate := range []string{"python3", "python"} {
			if p, err := lookPath(candidate); err == nil {
				python = p
				break
			}
		}
	}
	if python == "" {
		return nil, fmt.Errorf("matte sidecar: python not found (set MATTE_PYTHON or add python to PATH)")
	}

	type candidate struct {
		path   string
		source string
	}
	var tried []candidate

	tryScript := func(path, source string) (string, bool) {
		if path == "" {
			return "", false
		}
		tried = append(tried, candidate{path: path, source: source})
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
		return "", false
	}

	if override := os.Getenv("MATTE_SCRIPT"); override != "" {
		if script, ok := tryScript(override, "MATTE_SCRIPT"); ok {
			return &matteSidecar{Python: python, Script: script}, nil
		}
	}

	if root := os.Getenv("ANTHEM_REPO_ROOT"); root != "" {
		if script, ok := tryScript(filepath.Join(root, "tools", "matte", "matte.py"), "ANTHEM_REPO_ROOT"); ok {
			return &matteSidecar{Python: python, Script: script}, nil
		}
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for i := 0; i < 8; i++ {
			source := fmt.Sprintf("exe dir (%d up)", i)
			if script, ok := tryScript(filepath.Join(dir, "tools", "matte", "matte.py"), source); ok {
				return &matteSidecar{Python: python, Script: script}, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	if wd, err := os.Getwd(); err == nil {
		if script, ok := tryScript(filepath.Join(wd, "tools", "matte", "matte.py"), "cwd"); ok {
			return &matteSidecar{Python: python, Script: script}, nil
		}
	}

	if len(tried) == 0 {
		return nil, fmt.Errorf("matte sidecar: script path not resolvable (set MATTE_SCRIPT or ANTHEM_REPO_ROOT)")
	}
	paths := make([]string, len(tried))
	for i, c := range tried {
		paths[i] = fmt.Sprintf("%s (from %s)", c.path, c.source)
	}
	return nil, fmt.Errorf("matte sidecar: matte.py not found; tried:\n  %s\nset MATTE_SCRIPT to the absolute matte.py path, or ANTHEM_REPO_ROOT to the anthem repo root (note: NOT ANTHEM_HTTP_BRIDGE_ROOT — that is the per-project sandbox root)", strings.Join(paths, "\n  "))
}

// extractSidecarJSON scans sidecar stdout for the last JSON object and
// unmarshals it into out. The sidecar emits a single JSON status line to
// stdout; keeping this lenient ("take the last {...} line") avoids breaking
// on accidental extra stdout from upstream libraries.
func extractSidecarJSON(stdout []byte, out any) error {
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			return json.Unmarshal([]byte(line), out)
		}
	}
	return fmt.Errorf("no JSON status line in sidecar stdout")
}

// ---------------------------------------------------------------------------
// ChromaKeyProcessor
// ---------------------------------------------------------------------------

// ChromaKeyProcessor mattes a still-image PNG against a uniform magenta
// (#FF00FF) background plate. Pure-Go, no Python sidecar, no model load:
// runs in milliseconds on a few-megapixel sprite. Replaces the legacy
// BiRefNet-sidecar-backed RemoveBackgroundProcessor which paid a 5–15 s
// per-call model load tax and produced visible white halos because Nano
// Banana cannot return real transparent PNGs (it composites the subject
// onto an opaque off-white background, contaminating silhouette RGB).
//
// Algorithm:
//
//  1. For each pixel compute a "magenta excess" score in [0, 1]:
//     score = clamp((min(R,B) - G) / 255, 0, 1)
//     Score is high when red and blue are both bright AND green is
//     suppressed — exactly the signature of a magenta chroma plate. This
//     is the standard channel-difference keyer used by compositing tools
//     like Nuke's IBK, transposed from green-screen to magenta-screen.
//
//  2. Map score → alpha via Hermite smoothstep across (lower, upper):
//     lower = tolerance - softness/2
//     upper = tolerance + softness/2
//     Below `lower` the pixel is treated as opaque subject (alpha=255);
//     above `upper` it's pure plate (alpha=0); the band between is a
//     smooth ramp so silhouette anti-aliasing survives.
//
//  3. Despill on kept pixels: when both R and B are jointly inflated above
//     G (the magenta signature), suppress R and B by that shared excess
//     minus a `despill_offset` deadzone. Pure-red subjects (R high, B low)
//     have no joint inflation and are untouched; magenta-tinted edges
//     (R≈B≈high, G low) get pulled toward the green-equalized neutral.
//     Neutralizes residual magenta cast on subject edges — the actual
//     cause of the "white halo" with the prior matter; the mask was fine,
//     the RGB carried plate contamination that nothing was suppressing.
//
//  4. Optional shadow-kill pass (kill_shadow=true): the score-based keyer
//     above misses *darkened* magenta — generator-painted ground shadows
//     under the subject that come back as e.g. RGB(110,80,110). Their
//     absolute (min(R,B)-G) score sits well below `tolerance` so they
//     survive the primary pass as opaque "shadow blobs" attached to the
//     subject after matting. The shadow-kill pass converts each surviving
//     pixel to HSV and keys it if its hue is in the magenta family
//     (300° ± shadow_hue_tol) AND its saturation exceeds shadow_min_sat.
//     This catches shadows at any brightness because hue is invariant to
//     value. Safe to enable when the subject's palette has no
//     magenta/violet/pink design elements (e.g. RebelTower's earth-tone
//     painterly-pixel preset). DO NOT enable for sprites with intentional
//     magenta in the design (arcane wizards, magical effects, etc.) — it
//     will eat the subject's purple pixels along with the shadow.
//
// Config keys (all optional; values parsed as decimal strings):
//
//	tolerance       float, default "0.30"  — score threshold center
//	softness        float, default "0.10"  — width of the smoothstep band
//	despill_offset  int,   default "8"     — channel-clamp slack
//	kill_shadow     bool,  default "false" — enable HSV magenta-family
//	                                          second pass for darkened plate
//	shadow_hue_tol  float, default "30"    — half-width (degrees) of the
//	                                          magenta hue window centered
//	                                          on 300°; ignored unless
//	                                          kill_shadow=true
//	shadow_min_sat  float, default "0.10"  — minimum HSV saturation a
//	                                          magenta-hued pixel must reach
//	                                          to be classified as shadow;
//	                                          rejects gray pixels whose hue
//	                                          is undefined / noise-driven;
//	                                          ignored unless kill_shadow=true
type ChromaKeyProcessor struct{}

func (p *ChromaKeyProcessor) Run(artifactPath string, cfg map[string]string, _ PipelineState, log *slog.Logger) PostProcessResult {
	tolerance := 0.30
	if s := cfg["tolerance"]; s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 && v <= 1 {
			tolerance = v
		} else {
			log.Warn("chroma_key: invalid tolerance, using default", "raw", s, "default", tolerance)
		}
	}
	softness := 0.10
	if s := cfg["softness"]; s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 && v <= 1 {
			softness = v
		} else {
			log.Warn("chroma_key: invalid softness, using default", "raw", s, "default", softness)
		}
	}
	despillOffset := 8
	if s := cfg["despill_offset"]; s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 && v <= 255 {
			despillOffset = v
		} else {
			log.Warn("chroma_key: invalid despill_offset, using default", "raw", s, "default", despillOffset)
		}
	}
	killShadow := false
	if s := cfg["kill_shadow"]; s != "" {
		if v, err := strconv.ParseBool(s); err == nil {
			killShadow = v
		} else {
			log.Warn("chroma_key: invalid kill_shadow, using default", "raw", s, "default", killShadow)
		}
	}
	shadowHueTol := 30.0
	if s := cfg["shadow_hue_tol"]; s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 && v <= 180 {
			shadowHueTol = v
		} else {
			log.Warn("chroma_key: invalid shadow_hue_tol, using default", "raw", s, "default", shadowHueTol)
		}
	}
	shadowMinSat := 0.10
	if s := cfg["shadow_min_sat"]; s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 && v <= 1 {
			shadowMinSat = v
		} else {
			log.Warn("chroma_key: invalid shadow_min_sat, using default", "raw", s, "default", shadowMinSat)
		}
	}

	src, err := decodePNG(artifactPath)
	if err != nil {
		return PostProcessResult{
			Op:      "chroma_key",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("decoding PNG: %v", err),
		}
	}

	bounds := src.Bounds()
	dst := image.NewNRGBA(bounds)
	totalPx := bounds.Dx() * bounds.Dy()

	lower := tolerance - softness/2
	upper := tolerance + softness/2
	if lower < 0 {
		lower = 0
	}
	if upper > 1 {
		upper = 1
	}
	if upper <= lower {
		// Hard cutoff if softness is zero or thresholds collapsed.
		upper = lower + 1e-6
	}

	var keyed, despilled, shadowKilled int
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r16, g16, b16, _ := src.At(x, y).RGBA()
			r := uint8(r16 >> 8)
			g := uint8(g16 >> 8)
			b := uint8(b16 >> 8)

			minRB := r
			if b < minRB {
				minRB = b
			}
			score := 0.0
			if minRB > g {
				score = float64(minRB-g) / 255.0
			}

			var alpha uint8
			switch {
			case score >= upper:
				alpha = 0
				keyed++
			case score <= lower:
				alpha = 255
			default:
				t := (score - lower) / (upper - lower)
				// Hermite smoothstep: 3t^2 - 2t^3.
				s := t * t * (3 - 2*t)
				a := math.Round((1 - s) * 255)
				alpha = uint8(a)
				if alpha == 0 {
					keyed++
				}
			}

			// Optional shadow-kill: HSV magenta-family second pass over
			// pixels that survived the score-based keyer. Catches darkened
			// plate (R≈B, G<R, low brightness) that the absolute-score
			// keyer treats as subject. Hue is invariant to brightness, so
			// the same hue+sat test catches bright plate, mid shadow, and
			// deep shadow uniformly.
			if killShadow && alpha > 0 {
				hue, sat, _ := rgbToHSV(r, g, b)
				hueDelta := math.Abs(hue - 300.0)
				if hueDelta > 180 {
					hueDelta = 360 - hueDelta
				}
				if hueDelta <= shadowHueTol && sat >= shadowMinSat {
					alpha = 0
					shadowKilled++
				}
			}

			if alpha == 0 {
				dst.SetNRGBA(x, y, color.NRGBA{})
				continue
			}

			rNew := r
			bNew := b
			// Joint magenta excess above G, in raw [0,255] units. Pixels
			// reaching this branch have score ≤ upper, so spill is small
			// by construction. The despill_offset deadzone prevents
			// natural red/blue subject channels (which never carry plate
			// contamination) from being smudged.
			spill := int(minRB) - int(g) - despillOffset
			if spill > 0 {
				rNew = uint8SubClamp(r, spill)
				bNew = uint8SubClamp(b, spill)
				despilled++
			}
			dst.SetNRGBA(x, y, color.NRGBA{R: rNew, G: g, B: bNew, A: alpha})
		}
	}

	if keyed == 0 {
		log.Warn("chroma_key keyed no pixels; magenta plate may be missing",
			"path", artifactPath, "tolerance", tolerance, "softness", softness)
	}
	if keyed == totalPx {
		log.Warn("chroma_key keyed every pixel; output will be fully transparent",
			"path", artifactPath)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(artifactPath), "chroma-*.png")
	if err != nil {
		return PostProcessResult{
			Op:      "chroma_key",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("creating temp file: %v", err),
		}
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	if err := encodePNG(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return PostProcessResult{
			Op:      "chroma_key",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("encoding output PNG: %v", err),
		}
	}

	if err := os.Rename(tmpPath, artifactPath); err != nil {
		os.Remove(tmpPath)
		return PostProcessResult{
			Op:      "chroma_key",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("replacing artifact: %v", err),
		}
	}

	return PostProcessResult{
		Op:     "chroma_key",
		Status: PostProcessApplied,
		Message: fmt.Sprintf("chroma_key: %d/%d px keyed, %d edge despilled, %d shadow killed (tolerance=%.2f softness=%.2f despill_offset=%d kill_shadow=%t hue_tol=%.0f min_sat=%.2f)",
			keyed, totalPx, despilled, shadowKilled, tolerance, softness, despillOffset, killShadow, shadowHueTol, shadowMinSat),
	}
}

// uint8SubClamp returns base-delta saturated to [0, 255]. Used by the
// chroma_key despill to suppress R/B channels by the magenta excess.
func uint8SubClamp(base uint8, delta int) uint8 {
	v := int(base) - delta
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// rgbToHSV converts an 8-bit RGB triple to (hue°, saturation, value).
// Hue is in [0, 360); saturation and value are in [0, 1]. Used by the
// chroma_key shadow-kill pass to detect magenta-family pixels regardless
// of brightness. For pure gray (delta == 0) hue is undefined and returned
// as 0; callers must gate hue-based decisions on saturation.
func rgbToHSV(r, g, b uint8) (hue, sat, val float64) {
	rf := float64(r) / 255.0
	gf := float64(g) / 255.0
	bf := float64(b) / 255.0
	maxC := math.Max(rf, math.Max(gf, bf))
	minC := math.Min(rf, math.Min(gf, bf))
	val = maxC
	delta := maxC - minC
	if maxC == 0 {
		return 0, 0, 0
	}
	sat = delta / maxC
	if delta == 0 {
		return 0, sat, val
	}
	switch {
	case rf >= gf && rf >= bf:
		hue = 60 * ((gf - bf) / delta)
	case gf >= bf:
		hue = 60 * ((bf-rf)/delta + 2)
	default:
		hue = 60 * ((rf-gf)/delta + 4)
	}
	if hue < 0 {
		hue += 360
	}
	return hue, sat, val
}

// ---------------------------------------------------------------------------
// BiRefNetMatteProcessor
// ---------------------------------------------------------------------------

// BiRefNetMatteProcessor mattes a still-image PNG by shelling out to the
// matte.py `still` subcommand, which runs BiRefNet semantic segmentation
// (optionally followed by guided-filter edge refinement). Used as the
// recovery path for stills whose source plate is NOT pure magenta —
// "subject-palette bleed" cases where Nano Banana paints the matte in a
// non-magenta hue (e.g. maroon/oxblood for a fire wizard, dusty-rose for
// a pink-themed sprite). ChromaKeyProcessor produces a no-op result on
// those because its score-based keyer keys on the magenta signature; this
// op segments the subject semantically so the source background color is
// irrelevant.
//
// Trade-offs vs. ChromaKeyProcessor (the default still-image matter):
//
//   - Cost: 5–15 seconds per still (Python startup, BiRefNet model load,
//     and inference) vs. milliseconds for the in-Go chroma key. Acceptable
//     for a recovery tool but unsuitable as a default.
//   - Despill: BiRefNet outputs a binary subject mask with no
//     understanding of plate-color contamination. If the source has a
//     magenta cast bleeding into subject edges, that cast survives in
//     the kept pixels (the chroma key's despill pass is what makes
//     magenta-plate stills come back without halos). Mitigation: when
//     the source plate IS magenta, prefer ChromaKeyProcessor; reach for
//     this op only when the plate is non-magenta and chroma key has
//     already failed.
//   - Sidecar dependency: skips with PostProcessSkipped (not failed)
//     when matte.py / Python / model checkpoints aren't installed,
//     matching the same skip-friendly contract as VideoMatteProcessor.
//
// Config keys (all optional):
//
//	refine  bool, default "true"  — guided-filter edge recovery on the mask
//	radius  int,  default "8"     — guided-filter radius
type BiRefNetMatteProcessor struct {
	LookPath  func(file string) (string, error)
	CmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

type birefnetStillResult struct {
	Status    string `json:"status"`
	Mode      string `json:"mode"`
	Device    string `json:"device"`
	Refined   bool   `json:"refined"`
	Radius    int    `json:"radius"`
	ElapsedMS int    `json:"elapsed_ms"`
	Input     string `json:"input"`
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
}

func (p *BiRefNetMatteProcessor) Run(artifactPath string, cfg map[string]string, _ PipelineState, log *slog.Logger) PostProcessResult {
	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	cmdRunner := p.CmdRunner
	if cmdRunner == nil {
		cmdRunner = exec.CommandContext
	}

	sidecar, err := locateMattePython(lookPath)
	if err != nil {
		return PostProcessResult{Op: "birefnet_matte", Status: PostProcessSkipped, Message: err.Error()}
	}

	if _, err := os.Stat(artifactPath); err != nil {
		return PostProcessResult{Op: "birefnet_matte", Status: PostProcessFailed, Message: fmt.Sprintf("artifact not readable: %v", err)}
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(artifactPath), "birefnet-*.png")
	if err != nil {
		return PostProcessResult{Op: "birefnet_matte", Status: PostProcessFailed, Message: fmt.Sprintf("creating temp file: %v", err)}
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	// CreateTemp reserves a unique filename and creates an empty placeholder.
	// Delete the placeholder before invoking the sidecar so the post-call
	// os.Stat is a meaningful "did the sidecar write the output?" check
	// rather than "did the placeholder still exist?".
	_ = os.Remove(tmpPath)
	defer os.Remove(tmpPath)

	args := []string{sidecar.Script, "still",
		"--input", artifactPath,
		"--output", tmpPath,
	}
	if cfg["refine"] != "false" {
		args = append(args, "--refine")
		if radius := cfg["radius"]; radius != "" {
			args = append(args, "--radius", radius)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Info("birefnet_matte start", "artifact", artifactPath)
	startedAt := time.Now()
	cmd := cmdRunner(ctx, sidecar.Python, args...)
	stdoutBytes, stderrBytes, runErr := runSidecarCmd(ctx, cmd)
	log.Info("birefnet_matte sidecar exited", "elapsed", time.Since(startedAt).Round(time.Millisecond), "err", runErr)
	if runErr != nil {
		return PostProcessResult{
			Op:      "birefnet_matte",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("sidecar failed: %v: %s", runErr, truncateBytes(stderrBytes, 500)),
		}
	}

	var res birefnetStillResult
	if jerr := extractSidecarJSON(stdoutBytes, &res); jerr != nil {
		return PostProcessResult{Op: "birefnet_matte", Status: PostProcessFailed, Message: fmt.Sprintf("parsing sidecar stdout: %v", jerr)}
	}
	if res.Status != "ok" {
		errMsg := res.Error
		if errMsg == "" {
			errMsg = "sidecar reported non-ok status"
		}
		return PostProcessResult{Op: "birefnet_matte", Status: PostProcessFailed, Message: errMsg}
	}

	if _, err := os.Stat(tmpPath); err != nil {
		return PostProcessResult{Op: "birefnet_matte", Status: PostProcessFailed, Message: fmt.Sprintf("sidecar produced no output: %v", err)}
	}

	if err := os.Rename(tmpPath, artifactPath); err != nil {
		return PostProcessResult{Op: "birefnet_matte", Status: PostProcessFailed, Message: fmt.Sprintf("replacing artifact: %v", err)}
	}

	return PostProcessResult{
		Op:     "birefnet_matte",
		Status: PostProcessApplied,
		Message: fmt.Sprintf("birefnet still matte applied (%s, refined=%t, radius=%d, %dms)",
			res.Device, res.Refined, res.Radius, res.ElapsedMS),
	}
}

// ---------------------------------------------------------------------------
// BinarizeAlphaProcessor
// ---------------------------------------------------------------------------

// BinarizeAlphaProcessor strips soft-alpha edge halos from a still PNG by
// flooring near-zero alpha pixels to fully transparent and snapping the
// remaining pixels to fully opaque or fully transparent at a configurable
// midpoint. Pure-Go pixel math, no sidecar, runs in milliseconds.
//
// Algorithmically identical to the floorAlpha + binarizeAlpha passes that
// NormalizeFramesProcessor (Walt's pipeline) applies inside its bbox/scale
// loop. Extracted as a standalone op so still-image matters (notably the
// BiRefNetMatteProcessor recovery path) can apply the same halo-killer
// without dragging in NormalizeFramesProcessor's frame-set machinery.
//
// Why it matters: BiRefNet returns a soft probability mask, so silhouette
// edges come back as semi-transparent pixels (alpha 80–200 range) whose RGB
// is a blend of the subject and the original background plate. When those
// semi-transparent edges composite against a light surface they show the
// underlying plate-tinted RGB as a faint halo around the subject. Snapping
// alpha to {0, 255} at the 50% midpoint nukes the soft band entirely:
// edges that were "half plate, half subject" either disappear (alpha → 0)
// or become fully opaque (alpha → 255). The visual cost is slightly more
// aliased silhouette outlines, which at sprite-sheet display scale is
// invisible.
//
// Config keys (all optional):
//
//	alpha_threshold  uint, default "0"      — pixels with alpha in
//	                                          (0, threshold] are floored
//	                                          to 0. Use to kill faint
//	                                          BiRefNet "ghost" residue
//	                                          well below the snap midpoint.
//	                                          Threshold scale is uint32
//	                                          (0–0xFFFF); Walt's pipeline
//	                                          uses 4096 (~6% opacity) as
//	                                          its default floor.
//	alpha_snap       uint, default "0"      — pixels above the threshold
//	                                          become 255; pixels at or
//	                                          below become 0. uint32 scale.
//	                                          Walt's pipeline uses 32768
//	                                          (50% midpoint). A value of 0
//	                                          disables snapping (op acts
//	                                          purely as a floor pass).
//	                                          When snap > 0 it dominates
//	                                          floor: any pixel above
//	                                          alpha_snap is forced to 255
//	                                          regardless of alpha_threshold.
//
// Both defaults are 0 so the op is a no-op out of the box; callers must
// opt in via config. The Miyazaki rematte chain enables both with Walt's
// production values (4096 / 32768) so the recovery path matches Walt's
// halo-killing behavior end-to-end.
type BinarizeAlphaProcessor struct{}

func (p *BinarizeAlphaProcessor) Run(artifactPath string, cfg map[string]string, _ PipelineState, log *slog.Logger) PostProcessResult {
	var alphaThreshold uint32
	if s := cfg["alpha_threshold"]; s != "" {
		if v, err := strconv.ParseUint(s, 10, 32); err == nil && v <= 0xFFFF {
			alphaThreshold = uint32(v)
		} else {
			log.Warn("binarize_alpha: invalid alpha_threshold, using default 0", "raw", s)
		}
	}
	var alphaSnap uint32
	if s := cfg["alpha_snap"]; s != "" {
		if v, err := strconv.ParseUint(s, 10, 32); err == nil && v <= 0xFFFF {
			alphaSnap = uint32(v)
		} else {
			log.Warn("binarize_alpha: invalid alpha_snap, using default 0", "raw", s)
		}
	}

	if alphaThreshold == 0 && alphaSnap == 0 {
		return PostProcessResult{
			Op:      "binarize_alpha",
			Status:  PostProcessSkipped,
			Message: "alpha_threshold=0 and alpha_snap=0; both passes disabled",
		}
	}

	src, err := decodePNG(artifactPath)
	if err != nil {
		return PostProcessResult{
			Op:      "binarize_alpha",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("decoding PNG: %v", err),
		}
	}

	// Force NRGBA so the existing floorAlpha / binarizeAlpha helpers (which
	// operate directly on the .Pix backing slice) can be reused without
	// branching on color model. PNG decode usually returns NRGBA already
	// for alpha-bearing PNGs, but image.Image color-model fallbacks (e.g.
	// paletted PNGs that the encoder upgraded to RGBA64) would skip the
	// helper. Always normalizing avoids the silent skip.
	bounds := src.Bounds()
	nrgba, ok := src.(*image.NRGBA)
	if !ok {
		nrgba = image.NewNRGBA(bounds)
		draw.Draw(nrgba, bounds, src, bounds.Min, draw.Src)
	}

	floored := 0
	snappedOpaque := 0
	snappedTransparent := 0
	pix := nrgba.Pix
	for i := 3; i < len(pix); i += 4 {
		a8 := pix[i]
		if a8 == 0 {
			continue
		}
		a16 := uint32(a8) * 0x101
		if alphaThreshold > 0 && a16 <= alphaThreshold {
			pix[i] = 0
			floored++
			continue
		}
		if alphaSnap > 0 {
			if a16 > alphaSnap {
				pix[i] = 255
				snappedOpaque++
			} else {
				pix[i] = 0
				snappedTransparent++
			}
		}
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(artifactPath), "binarize-*.png")
	if err != nil {
		return PostProcessResult{
			Op:      "binarize_alpha",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("creating temp file: %v", err),
		}
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	if err := encodePNG(tmpPath, nrgba); err != nil {
		os.Remove(tmpPath)
		return PostProcessResult{
			Op:      "binarize_alpha",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("encoding output PNG: %v", err),
		}
	}
	if err := os.Rename(tmpPath, artifactPath); err != nil {
		os.Remove(tmpPath)
		return PostProcessResult{
			Op:      "binarize_alpha",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("replacing artifact: %v", err),
		}
	}

	return PostProcessResult{
		Op:     "binarize_alpha",
		Status: PostProcessApplied,
		Message: fmt.Sprintf("floored=%d snapped_opaque=%d snapped_transparent=%d (alpha_threshold=%d alpha_snap=%d)",
			floored, snappedOpaque, snappedTransparent, alphaThreshold, alphaSnap),
	}
}

// ---------------------------------------------------------------------------
// PlateSpeckCleanupProcessor
// ---------------------------------------------------------------------------

// PlateSpeckCleanupProcessor removes small interior plate-color survivors that
// BiRefNet's "single connected blob" prior leaves behind in concave subject
// pockets (between legs, under shoes, under bag handles, etc.). Pure-Go,
// deterministic, no Python sidecar; runs in milliseconds on a multi-megapixel
// sprite. Designed to chain after `birefnet_matte` + `binarize_alpha`.
//
// Why it exists. BiRefNet treats the subject as one connected segmentation
// region and tends to fill in topologically-enclosed background pockets as
// part of the subject mask. Empirically (2026-05-04 trinket-salesman audit)
// this leaves 50–70 small magenta-family blobs (4–90 px each) in the
// interior of complex sprites, while large intentional purple subject
// features (potion bottles, leather highlights, decorative cloth) survive
// as single cohesive blobs in the 150–500 px range. The discriminator is
// trivial: small blobs are wrong, large blobs are right.
//
// Algorithm:
//
//  1. Score each pixel via the same chroma-key keyer formula as
//     ChromaKeyProcessor: score = max(0, (min(R,B) - G) / 255). Flag any
//     opaque pixel whose score exceeds `score_threshold` (default 0.25,
//     matching the chroma-key default).
//  2. 8-connectivity flood-fill (BFS) to label connected components on
//     the flagged mask.
//  3. For each component whose pixel count is ≤ `size_threshold` (default
//     150), set every pixel's alpha to 0. Larger components are preserved
//     (they're real subject features).
//
// Symmetry rule check (anthem/CLAUDE.md "Color-op symmetry rule"). This op
// only modifies the alpha channel; RGB is never touched. So it does NOT
// introduce a still-vs-animation color drift and is exempt from the
// both-pipelines requirement. Walt's `video_matte` + Lanczos downscale
// chain already mitigates interior pockets in animations via SAM 2's
// temporal consensus and downscale-driven plate-color dilution.
//
// Empirical validation (2026-05-04, trinket-salesman + fire-wizard +
// hot-air-balloon-archer audit on production assets):
//
//   - trinket-salesman: 1,436 flagged px in 68 components. At T=150,
//     65 components killed (535 px) preserved 3 cohesive purple
//     decorations (901 px). Visual review confirmed clean removal of
//     interior holes between legs, under shoes, under bag — and clean
//     preservation of the bag's purple potions.
//   - fire-wizard: 0 flagged px. No-op.
//   - hot-air-balloon-archer: 0 flagged px. No-op.
//
// Conclusion: cheap, deterministic, only does work when needed, and the
// only sprite where it activated was correctly classified. Safe as a
// default chain step.
//
// Config keys (all optional; values parsed as decimal strings):
//
//	score_threshold  float, default "0.25"  — chroma-key keyer threshold;
//	                                          opaque pixels above this are
//	                                          flagged for connectivity
//	                                          analysis. Lower values widen
//	                                          the flagging net to include
//	                                          subtler magenta-family bleed.
//	size_threshold   int,   default "150"   — components with ≤ N pixels
//	                                          have their alpha killed.
//	                                          Tuned on trinket-salesman to
//	                                          preserve 88 px subject
//	                                          features while killing
//	                                          50–70 px interior holes.
//	connectivity     int,   default "8"     — 4 (cardinal) or 8 (cardinal
//	                                          + diagonal) BFS adjacency.
//	                                          8 catches diagonally-touching
//	                                          plate pixels as one component;
//	                                          4 splits them, which inflates
//	                                          the small-component count and
//	                                          can mis-classify a single
//	                                          diagonal hole as several
//	                                          tiny blobs.
//	edge_aware       bool,  default "false" — when true, components are
//	                                          ALSO killed if any of their
//	                                          pixels is adjacent (under the
//	                                          chosen connectivity) to an
//	                                          alpha=0 pixel OR to the image
//	                                          border. This catches large
//	                                          BiRefNet blob-completion
//	                                          artifacts (unmatted plate
//	                                          patches under arms / between
//	                                          legs) that touch the matted
//	                                          background while preserving
//	                                          legitimate magenta-family
//	                                          subject features (potion
//	                                          bottles, painted highlights)
//	                                          that are fully interior to
//	                                          the silhouette. Default off
//	                                          for backward compatibility.
type PlateSpeckCleanupProcessor struct{}

func (p *PlateSpeckCleanupProcessor) Run(artifactPath string, cfg map[string]string, _ PipelineState, log *slog.Logger) PostProcessResult {
	scoreThreshold := 0.25
	if s := cfg["score_threshold"]; s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 && v <= 1 {
			scoreThreshold = v
		} else {
			log.Warn("plate_speck_cleanup: invalid score_threshold, using default", "raw", s, "default", scoreThreshold)
		}
	}
	sizeThreshold := 150
	if s := cfg["size_threshold"]; s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			sizeThreshold = v
		} else {
			log.Warn("plate_speck_cleanup: invalid size_threshold, using default", "raw", s, "default", sizeThreshold)
		}
	}
	connectivity := 8
	if s := cfg["connectivity"]; s != "" {
		if v, err := strconv.Atoi(s); err == nil && (v == 4 || v == 8) {
			connectivity = v
		} else {
			log.Warn("plate_speck_cleanup: invalid connectivity (want 4 or 8), using default", "raw", s, "default", connectivity)
		}
	}
	edgeAware := false
	if s := cfg["edge_aware"]; s != "" {
		switch strings.ToLower(s) {
		case "true", "1", "yes":
			edgeAware = true
		case "false", "0", "no":
			edgeAware = false
		default:
			log.Warn("plate_speck_cleanup: invalid edge_aware (want true/false), using default", "raw", s, "default", edgeAware)
		}
	}

	src, err := decodePNG(artifactPath)
	if err != nil {
		return PostProcessResult{
			Op:      "plate_speck_cleanup",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("decoding PNG: %v", err),
		}
	}

	bounds := src.Bounds()
	nrgba, ok := src.(*image.NRGBA)
	if !ok {
		nrgba = image.NewNRGBA(bounds)
		draw.Draw(nrgba, bounds, src, bounds.Min, draw.Src)
	}

	W, H := bounds.Dx(), bounds.Dy()
	if W == 0 || H == 0 {
		return PostProcessResult{Op: "plate_speck_cleanup", Status: PostProcessSkipped, Message: "empty image"}
	}

	flagged := make([]bool, W*H)
	pix := nrgba.Pix
	stride := nrgba.Stride
	flaggedCount := 0
	for y := 0; y < H; y++ {
		row := y * stride
		flagRow := y * W
		for x := 0; x < W; x++ {
			off := row + x*4
			r, g, b, a := pix[off], pix[off+1], pix[off+2], pix[off+3]
			if a == 0 {
				continue
			}
			minRB := r
			if b < minRB {
				minRB = b
			}
			if minRB <= g {
				continue
			}
			score := float64(minRB-g) / 255.0
			if score > scoreThreshold {
				flagged[flagRow+x] = true
				flaggedCount++
			}
		}
	}

	if flaggedCount == 0 {
		return PostProcessResult{
			Op:      "plate_speck_cleanup",
			Status:  PostProcessApplied,
			Message: fmt.Sprintf("no flagged pixels (score_threshold=%.2f); image already clean", scoreThreshold),
		}
	}

	// 8-connectivity (or 4) BFS labeling. Process flagged pixels in
	// raster order; each unvisited flagged pixel kicks off a flood-fill
	// that records the component's pixel offsets, so the kill pass can
	// rewrite alpha=0 only when the size threshold is met.
	visited := make([]bool, W*H)
	queue := make([]int, 0, 64)
	component := make([]int, 0, 64)
	var dxs, dys []int
	if connectivity == 4 {
		dxs = []int{-1, 1, 0, 0}
		dys = []int{0, 0, -1, 1}
	} else {
		dxs = []int{-1, -1, -1, 0, 0, 1, 1, 1}
		dys = []int{-1, 0, 1, -1, 1, -1, 0, 1}
	}

	componentCount := 0
	killedComponents := 0
	killedPixels := 0
	killedComponentsBySize := 0
	killedComponentsByEdge := 0
	preservedComponents := 0
	preservedPixels := 0

	for startY := 0; startY < H; startY++ {
		flagRow := startY * W
		for startX := 0; startX < W; startX++ {
			startIdx := flagRow + startX
			if !flagged[startIdx] || visited[startIdx] {
				continue
			}
			componentCount++
			component = component[:0]
			queue = append(queue[:0], startIdx)
			visited[startIdx] = true
			// edgeAdjacent is set if any pixel in this component has a
			// neighbor (under the chosen connectivity) that's either
			// alpha=0 or off the image. We track it during BFS so the
			// kill decision below has the answer in O(component) work
			// instead of a second pass.
			edgeAdjacent := false
			for len(queue) > 0 {
				idx := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				component = append(component, idx)
				cy := idx / W
				cx := idx - cy*W
				for k := range dxs {
					nx, ny := cx+dxs[k], cy+dys[k]
					if nx < 0 || nx >= W || ny < 0 || ny >= H {
						// Out-of-bounds neighbor: treat the image
						// border as exterior alpha=0 so a magenta
						// component that runs to the edge of the
						// frame counts as edge-adjacent.
						edgeAdjacent = true
						continue
					}
					nIdx := ny*W + nx
					if flagged[nIdx] && !visited[nIdx] {
						visited[nIdx] = true
						queue = append(queue, nIdx)
						continue
					}
					if !flagged[nIdx] {
						// Non-flagged neighbor: check whether it's a
						// transparent (matted-out) pixel, which would
						// make the component sit on the silhouette
						// edge. We don't add it to the component.
						nOff := ny*stride + nx*4
						if pix[nOff+3] == 0 {
							edgeAdjacent = true
						}
					}
				}
			}

			killBySize := len(component) <= sizeThreshold
			killByEdge := edgeAware && edgeAdjacent && !killBySize
			if killBySize || killByEdge {
				killedComponents++
				killedPixels += len(component)
				if killBySize {
					killedComponentsBySize++
				}
				if killByEdge {
					killedComponentsByEdge++
				}
				for _, idx := range component {
					yy := idx / W
					xx := idx - yy*W
					off := yy*stride + xx*4
					// Zero RGB alongside alpha so downstream Lanczos
					// downscalers (which average RGB across neighbors
					// regardless of alpha) cannot reintroduce magenta
					// contamination from killed pixels.
					pix[off] = 0
					pix[off+1] = 0
					pix[off+2] = 0
					pix[off+3] = 0
				}
			} else {
				preservedComponents++
				preservedPixels += len(component)
			}
		}
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(artifactPath), "speck-*.png")
	if err != nil {
		return PostProcessResult{
			Op:      "plate_speck_cleanup",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("creating temp file: %v", err),
		}
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	if err := encodePNG(tmpPath, nrgba); err != nil {
		os.Remove(tmpPath)
		return PostProcessResult{
			Op:      "plate_speck_cleanup",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("encoding output PNG: %v", err),
		}
	}

	if err := os.Rename(tmpPath, artifactPath); err != nil {
		os.Remove(tmpPath)
		return PostProcessResult{
			Op:      "plate_speck_cleanup",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("replacing artifact: %v", err),
		}
	}

	msg := fmt.Sprintf("flagged=%d in %d components; killed %d components (%d px); preserved %d components (%d px) (score_threshold=%.2f size_threshold=%d connectivity=%d edge_aware=%t)",
		flaggedCount, componentCount, killedComponents, killedPixels, preservedComponents, preservedPixels, scoreThreshold, sizeThreshold, connectivity, edgeAware)
	if edgeAware {
		msg += fmt.Sprintf(" [killed by size=%d, killed by edge=%d]", killedComponentsBySize, killedComponentsByEdge)
	}
	return PostProcessResult{
		Op:      "plate_speck_cleanup",
		Status:  PostProcessApplied,
		Message: msg,
	}
}

// ---------------------------------------------------------------------------
// VideoMatteProcessor
// ---------------------------------------------------------------------------

// VideoMatteProcessor mattes every frame in state["frame_dir"] via the
// BiRefNet (keyframes) + SAM 2 (propagation) + guided-filter sidecar. On SAM 2
// failure the sidecar degrades to per-frame BiRefNet and reports the mode in
// its stdout JSON; the Go side surfaces the mode in the result message.
//
// Config keys (all optional):
//
//	keyframe_every  int, default "8"      — one BiRefNet seed every N frames
//	refine          bool, default "true"  — enable guided-filter edge recovery
//	radius          int, default "8"      — guided-filter radius
//	no_sam2         bool, default "false" — skip SAM 2, per-frame BiRefNet only
type VideoMatteProcessor struct {
	LookPath  func(file string) (string, error)
	CmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

type videoMatteResult struct {
	Status          string `json:"status"`
	Mode            string `json:"mode"`
	Device          string `json:"device"`
	FramesProcessed int    `json:"frames_processed"`
	Keyframes       int    `json:"keyframes"`
	ElapsedMS       int    `json:"elapsed_ms"`
	Error           string `json:"error,omitempty"`
}

func (p *VideoMatteProcessor) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
	frameDir := state["frame_dir"]
	if frameDir == "" {
		return PostProcessResult{Op: "video_matte", Status: PostProcessSkipped, Message: "no frame_dir in pipeline state"}
	}

	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	cmdRunner := p.CmdRunner
	if cmdRunner == nil {
		cmdRunner = exec.CommandContext
	}

	sidecar, err := locateMattePython(lookPath)
	if err != nil {
		return PostProcessResult{Op: "video_matte", Status: PostProcessSkipped, Message: err.Error()}
	}

	frames, err := listPNGs(frameDir)
	if err != nil || len(frames) == 0 {
		return PostProcessResult{Op: "video_matte", Status: PostProcessSkipped, Message: "no frames in frame_dir"}
	}

	outDir, err := os.MkdirTemp("", "video-matte-out-*")
	if err != nil {
		return PostProcessResult{Op: "video_matte", Status: PostProcessFailed, Message: fmt.Sprintf("creating output dir: %v", err)}
	}
	defer os.RemoveAll(outDir)

	args := []string{sidecar.Script, "video",
		"--frames-dir", frameDir,
		"--output-dir", outDir,
	}
	if kfe := cfg["keyframe_every"]; kfe != "" {
		args = append(args, "--keyframe-every", kfe)
	} else {
		args = append(args, "--keyframe-every", "8")
	}
	if cfg["refine"] != "false" {
		args = append(args, "--refine")
	}
	if radius := cfg["radius"]; radius != "" {
		args = append(args, "--radius", radius)
	}
	if cfg["no_sam2"] == "true" {
		args = append(args, "--no-sam2")
	}

	timeout := time.Duration(len(frames))*10*time.Second + 5*time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	log.Info("video_matte start", "frames", len(frames), "timeout", timeout)
	startedAt := time.Now()
	cmd := cmdRunner(ctx, sidecar.Python, args...)
	stdoutBytes, stderrBytes, runErr := runSidecarCmd(ctx, cmd)
	log.Info("video_matte sidecar exited", "elapsed", time.Since(startedAt).Round(time.Millisecond), "err", runErr)
	if runErr != nil {
		return PostProcessResult{
			Op:      "video_matte",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("sidecar failed: %v: %s", runErr, truncateBytes(stderrBytes, 500)),
		}
	}

	var res videoMatteResult
	if jerr := extractSidecarJSON(stdoutBytes, &res); jerr != nil {
		return PostProcessResult{Op: "video_matte", Status: PostProcessFailed, Message: fmt.Sprintf("parsing sidecar stdout: %v", jerr)}
	}
	if res.Status != "ok" {
		errMsg := res.Error
		if errMsg == "" {
			errMsg = "sidecar reported non-ok status"
		}
		return PostProcessResult{Op: "video_matte", Status: PostProcessFailed, Message: errMsg}
	}

	replaced := 0
	for _, framePath := range frames {
		srcPath := filepath.Join(outDir, filepath.Base(framePath))
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		if err := copyFile(srcPath, framePath); err != nil {
			return PostProcessResult{Op: "video_matte", Status: PostProcessFailed, Message: fmt.Sprintf("replacing %s: %v", filepath.Base(framePath), err)}
		}
		replaced++
	}
	if replaced == 0 {
		return PostProcessResult{Op: "video_matte", Status: PostProcessFailed, Message: "sidecar produced no matted frames"}
	}

	msg := fmt.Sprintf("%s; %d/%d frames matted (%s, %d keyframes, %dms)",
		res.Mode, replaced, len(frames), res.Device, res.Keyframes, res.ElapsedMS)
	return PostProcessResult{Op: "video_matte", Status: PostProcessApplied, Message: msg}
}

// ---------------------------------------------------------------------------
// CheckFrameConsistencyProcessor
// ---------------------------------------------------------------------------

// CheckFrameConsistencyProcessor runs a DINOv2-based drift check over the
// matted frames in state["frame_dir"], comparing each frame against both
// frame 0 (anchor) and a rolling temporal window. Advisory only: low-similarity
// frames are reported in the result message and in pipeline state, but never
// cause the overall pipeline to fail.
//
// Config keys (all optional):
//
//	anchor_threshold  float, default "0.85"
//	window_threshold  float, default "0.92"
//	window_size       int,   default "3"
type CheckFrameConsistencyProcessor struct {
	LookPath  func(file string) (string, error)
	CmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

type driftResult struct {
	Status          string    `json:"status"`
	Device          string    `json:"device"`
	FramesProcessed int       `json:"frames_processed"`
	AnchorThreshold float64   `json:"anchor_threshold"`
	WindowThreshold float64   `json:"window_threshold"`
	WindowSize      int       `json:"window_size"`
	AnchorScores    []float64 `json:"anchor_scores"`
	WindowScores    []float64 `json:"window_scores"`
	AnchorOutliers  []int     `json:"anchor_outliers"`
	WindowOutliers  []int     `json:"window_outliers"`
	ElapsedMS       int       `json:"elapsed_ms"`
	Error           string    `json:"error,omitempty"`
}

func (p *CheckFrameConsistencyProcessor) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
	frameDir := state["frame_dir"]
	if frameDir == "" {
		return PostProcessResult{Op: "check_frame_consistency", Status: PostProcessSkipped, Message: "no frame_dir in pipeline state"}
	}

	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	cmdRunner := p.CmdRunner
	if cmdRunner == nil {
		cmdRunner = exec.CommandContext
	}

	sidecar, err := locateMattePython(lookPath)
	if err != nil {
		return PostProcessResult{Op: "check_frame_consistency", Status: PostProcessSkipped, Message: err.Error()}
	}

	args := []string{sidecar.Script, "drift", "--frames-dir", frameDir}
	anchor := cfg["anchor_threshold"]
	if anchor == "" {
		anchor = "0.85"
	}
	window := cfg["window_threshold"]
	if window == "" {
		window = "0.92"
	}
	size := cfg["window_size"]
	if size == "" {
		size = "3"
	}
	args = append(args,
		"--anchor-threshold", anchor,
		"--window-threshold", window,
		"--window-size", size,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := cmdRunner(ctx, sidecar.Python, args...)
	stdoutBytes, stderrBytes, runErr := runSidecarCmd(ctx, cmd)
	if runErr != nil {
		return PostProcessResult{
			Op:      "check_frame_consistency",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("sidecar failed: %v: %s", runErr, truncateBytes(stderrBytes, 500)),
		}
	}

	var res driftResult
	if jerr := extractSidecarJSON(stdoutBytes, &res); jerr != nil {
		return PostProcessResult{Op: "check_frame_consistency", Status: PostProcessFailed, Message: fmt.Sprintf("parsing sidecar stdout: %v", jerr)}
	}
	if res.Status != "ok" {
		errMsg := res.Error
		if errMsg == "" {
			errMsg = "sidecar reported non-ok status"
		}
		return PostProcessResult{Op: "check_frame_consistency", Status: PostProcessFailed, Message: errMsg}
	}

	anchorSet := make(map[int]struct{}, len(res.AnchorOutliers))
	for _, i := range res.AnchorOutliers {
		anchorSet[i] = struct{}{}
	}
	windowSet := make(map[int]struct{}, len(res.WindowOutliers))
	for _, i := range res.WindowOutliers {
		windowSet[i] = struct{}{}
	}
	union := make(map[int]struct{}, len(anchorSet)+len(windowSet))
	for i := range anchorSet {
		union[i] = struct{}{}
	}
	for i := range windowSet {
		union[i] = struct{}{}
	}
	combined := make([]int, 0, len(union))
	for i := range union {
		combined = append(combined, i)
	}
	sort.Ints(combined)

	combinedStrs := make([]string, 0, len(combined))
	for _, i := range combined {
		combinedStrs = append(combinedStrs, strconv.Itoa(i))
	}
	state["drift_frames"] = strings.Join(combinedStrs, ",")

	var msg string
	switch {
	case len(combined) == 0:
		msg = fmt.Sprintf("no drift (%d frames, anchor>=%s, window>=%s, %dms)",
			res.FramesProcessed, anchor, window, res.ElapsedMS)
	default:
		msg = fmt.Sprintf("%d outlier frames [anchor=%d, window=%d] of %d (%dms): %s",
			len(combined), len(anchorSet), len(windowSet), res.FramesProcessed, res.ElapsedMS,
			strings.Join(combinedStrs, ","))
	}
	state["drift_report"] = msg

	return PostProcessResult{Op: "check_frame_consistency", Status: PostProcessApplied, Message: msg}
}

// ---------------------------------------------------------------------------
// ExtractVideoFramesProcessor
// ---------------------------------------------------------------------------

// ExtractVideoFramesProcessor extracts PNG frames from a video using ffmpeg.
type ExtractVideoFramesProcessor struct {
	LookPath  func(file string) (string, error)
	CmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

const defaultExtractFPS = "8"

func (p *ExtractVideoFramesProcessor) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	cmdRunner := p.CmdRunner
	if cmdRunner == nil {
		cmdRunner = exec.CommandContext
	}

	ffmpegPath, err := lookPath("ffmpeg")
	if err != nil {
		return PostProcessResult{Op: "extract_video_frames", Status: PostProcessSkipped, Message: "ffmpeg not installed"}
	}

	fps := cfg["fps"]
	if fps == "" {
		fps = defaultExtractFPS
	}

	tmpDir, err := os.MkdirTemp("", "frames-*")
	if err != nil {
		return PostProcessResult{Op: "extract_video_frames", Status: PostProcessFailed, Message: fmt.Sprintf("creating temp dir: %v", err)}
	}

	pattern := filepath.Join(tmpDir, "frame_%04d.png")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := cmdRunner(ctx, ffmpegPath, "-i", artifactPath, "-vf", "fps="+fps, pattern)
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(tmpDir)
		return PostProcessResult{Op: "extract_video_frames", Status: PostProcessFailed, Message: fmt.Sprintf("ffmpeg failed: %v: %s", err, truncateBytes(output, 200))}
	}

	frames, err := listPNGs(tmpDir)
	if err != nil || len(frames) == 0 {
		os.RemoveAll(tmpDir)
		return PostProcessResult{Op: "extract_video_frames", Status: PostProcessFailed, Message: "ffmpeg produced no frames"}
	}

	state["frame_dir"] = tmpDir
	state["frame_count"] = strconv.Itoa(len(frames))
	state["fps"] = fps

	return PostProcessResult{
		Op:      "extract_video_frames",
		Status:  PostProcessApplied,
		Message: fmt.Sprintf("%d frames at %s fps to %s", len(frames), fps, tmpDir),
	}
}

// ---------------------------------------------------------------------------
// NormalizeFramesProcessor
// ---------------------------------------------------------------------------

// NormalizeFramesProcessor ensures all frames in a directory share identical
// dimensions by computing the union content bounding box across all frames and
// centering each frame on a uniform canvas.
type NormalizeFramesProcessor struct{}

func (p *NormalizeFramesProcessor) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
	frameDir := state["frame_dir"]
	if frameDir == "" {
		return PostProcessResult{Op: "normalize_frames", Status: PostProcessSkipped, Message: "no frame_dir in pipeline state"}
	}

	padding := 4
	if s := cfg["padding"]; s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			padding = v
		}
	}

	alphaThreshold := defaultAlphaThreshold
	if s := cfg["alpha_threshold"]; s != "" {
		if v, err := strconv.ParseUint(s, 10, 32); err == nil {
			alphaThreshold = uint32(v)
		}
	}

	var alphaSnap uint32
	if s := cfg["alpha_snap"]; s != "" {
		if v, err := strconv.ParseUint(s, 10, 32); err == nil {
			alphaSnap = uint32(v)
		}
	}

	// scale_factor: integer divisor applied after centering each frame on the
	// uniform canvas. Used to bring oversized Veo 1080p outputs (1447x3132
	// raw → 11560x25128 stitched at 8 cols) down to Unity-safe texture sizes
	// (≤16384 on any axis). scale_factor=4 is the production default in
	// Walt's creature pipeline (agents/walt.md). Values <1 or non-integer
	// are ignored with a warning. Resampling uses CatmullRom (cubic) which
	// is the best-quality filter shipped in x/image/draw — Lanczos isn't
	// available in Go stdlib, and CatmullRom matches it closely for 2–8×
	// downscales while preserving the soft alpha edges produced by matting.
	scaleFactor := 1
	if s := cfg["scale_factor"]; s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 1 {
			scaleFactor = v
		} else {
			log.Warn("normalize_frames: invalid scale_factor, using 1", "raw", s)
		}
	}

	frames, err := listPNGs(frameDir)
	if err != nil || len(frames) == 0 {
		return PostProcessResult{Op: "normalize_frames", Status: PostProcessSkipped, Message: "no frames to normalize"}
	}

	type frameBBox struct {
		img    image.Image
		bounds image.Rectangle
		bbox   image.Rectangle
	}
	loaded := make([]frameBBox, 0, len(frames))
	var unionBBox image.Rectangle
	first := true

	for _, path := range frames {
		img, err := decodePNG(path)
		if err != nil {
			return PostProcessResult{Op: "normalize_frames", Status: PostProcessFailed, Message: fmt.Sprintf("decoding %s: %v", filepath.Base(path), err)}
		}
		if nrgba, ok := img.(*image.NRGBA); ok {
			floorAlpha(nrgba, alphaThreshold)
			binarizeAlpha(nrgba, alphaSnap)
		}
		bbox := contentBoundingBox(img, alphaThreshold)
		if first {
			unionBBox = bbox
			first = false
		} else {
			unionBBox = unionBBox.Union(bbox)
		}
		loaded = append(loaded, frameBBox{img: img, bounds: img.Bounds(), bbox: bbox})
	}

	if unionBBox.Empty() {
		return PostProcessResult{Op: "normalize_frames", Status: PostProcessSkipped, Message: "all frames are fully transparent"}
	}

	canvasW := unionBBox.Dx() + 2*padding
	canvasH := unionBBox.Dy() + 2*padding

	// Integer-divide dims so every frame lands on the same pixel grid.
	// Rounding mode matches the Python patch-work reference (`//` floor div)
	// so a scale_factor that produced a Unity-safe sheet during manual
	// asset patching produces the same sheet when re-run through Walt.
	outW, outH := canvasW, canvasH
	if scaleFactor > 1 {
		outW = canvasW / scaleFactor
		outH = canvasH / scaleFactor
		if outW < 1 || outH < 1 {
			return PostProcessResult{
				Op:     "normalize_frames",
				Status: PostProcessFailed,
				Message: fmt.Sprintf("scale_factor=%d collapses %dx%d canvas to <1px; refusing",
					scaleFactor, canvasW, canvasH),
			}
		}
	}

	for i, fb := range loaded {
		canvas := image.NewNRGBA(image.Rect(0, 0, canvasW, canvasH))
		srcBounds := fb.img.Bounds()
		offsetX := padding - unionBBox.Min.X + srcBounds.Min.X
		offsetY := padding - unionBBox.Min.Y + srcBounds.Min.Y
		draw.Draw(canvas, image.Rect(offsetX, offsetY, offsetX+srcBounds.Dx(), offsetY+srcBounds.Dy()), fb.img, srcBounds.Min, draw.Src)

		out := canvas
		if scaleFactor > 1 {
			scaled := image.NewNRGBA(image.Rect(0, 0, outW, outH))
			xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), canvas, canvas.Bounds(), xdraw.Over, nil)
			out = scaled
		}

		if err := encodePNG(frames[i], out); err != nil {
			return PostProcessResult{Op: "normalize_frames", Status: PostProcessFailed, Message: fmt.Sprintf("writing %s: %v", filepath.Base(frames[i]), err)}
		}
	}

	state["normalized_width"] = strconv.Itoa(outW)
	state["normalized_height"] = strconv.Itoa(outH)

	msg := fmt.Sprintf("%d frames normalized to %dx%d (padding=%d)", len(frames), outW, outH, padding)
	if scaleFactor > 1 {
		msg = fmt.Sprintf("%d frames normalized to %dx%d (padding=%d, scale_factor=%d, pre-scale=%dx%d, CatmullRom)",
			len(frames), outW, outH, padding, scaleFactor, canvasW, canvasH)
	}
	return PostProcessResult{
		Op:      "normalize_frames",
		Status:  PostProcessApplied,
		Message: msg,
	}
}

// contentBoundingBox returns the tightest rectangle containing all pixels
// whose alpha exceeds alphaThreshold (uint32 scale, 0–0xFFFF).
// A threshold of 0 reproduces the legacy "any non-zero alpha" behavior.
func contentBoundingBox(img image.Image, alphaThreshold uint32) image.Rectangle {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a > alphaThreshold {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}

	if minX > maxX || minY > maxY {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

// defaultAlphaThreshold is ~6% opacity (0x1000 / 0xFFFF). Pixels at or below
// this level are treated as rembg residue noise rather than real content.
const defaultAlphaThreshold uint32 = 0x1000

// floorAlpha sets the alpha channel to 0 for every pixel whose alpha is
// non-zero but at or below the given threshold (uint32 scale). This removes
// near-invisible rembg halos that cause faded-frame artifacts in sprite sheets.
func floorAlpha(img *image.NRGBA, threshold uint32) {
	pix := img.Pix
	for i := 3; i < len(pix); i += 4 {
		a8 := pix[i]
		if a8 == 0 {
			continue
		}
		if uint32(a8)*0x101 <= threshold {
			pix[i] = 0
		}
	}
}

// binarizeAlpha snaps every pixel's alpha to 0 or 255 based on threshold
// (uint32 scale, 0–0xFFFF). Pixels with alpha above the threshold become fully
// opaque; those at or below become fully transparent. A threshold of 0 is a
// no-op, allowing opt-out. This eliminates rembg's soft-edge bleeding where
// limb boundaries retain mid-range partial transparency.
func binarizeAlpha(img *image.NRGBA, threshold uint32) {
	if threshold == 0 {
		return
	}
	pix := img.Pix
	for i := 3; i < len(pix); i += 4 {
		a8 := pix[i]
		if a8 == 0 {
			continue
		}
		if uint32(a8)*0x101 > threshold {
			pix[i] = 255
		} else {
			pix[i] = 0
		}
	}
}

// ---------------------------------------------------------------------------
// StitchSpritesheetProcessor
// ---------------------------------------------------------------------------

// StitchSpritesheetProcessor packs individual frame PNGs into a grid sprite
// sheet and writes a metadata sidecar JSON file.
type StitchSpritesheetProcessor struct{}

// SpritesheetMeta is the JSON sidecar written alongside the sprite sheet.
type SpritesheetMeta struct {
	FrameCount  int    `json:"frame_count"`
	Columns     int    `json:"columns"`
	Rows        int    `json:"rows"`
	FrameWidth  int    `json:"frame_width"`
	FrameHeight int    `json:"frame_height"`
	FPS         int    `json:"fps"`
	SourceVideo string `json:"source_video"`
}

func (p *StitchSpritesheetProcessor) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
	frameDir := state["frame_dir"]
	if frameDir == "" {
		return PostProcessResult{Op: "stitch_spritesheet", Status: PostProcessSkipped, Message: "no frame_dir in pipeline state"}
	}

	defer os.RemoveAll(frameDir)

	columns := 4
	if s := cfg["columns"]; s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			columns = v
		}
	}

	keepVideo := false
	if cfg["keep_video"] == "true" {
		keepVideo = true
	}

	frames, err := listPNGs(frameDir)
	if err != nil || len(frames) == 0 {
		return PostProcessResult{Op: "stitch_spritesheet", Status: PostProcessFailed, Message: "no frames to stitch"}
	}

	images := make([]image.Image, 0, len(frames))
	var frameW, frameH int
	for i, path := range frames {
		img, err := decodePNG(path)
		if err != nil {
			return PostProcessResult{Op: "stitch_spritesheet", Status: PostProcessFailed, Message: fmt.Sprintf("decoding %s: %v", filepath.Base(path), err)}
		}
		b := img.Bounds()
		if i == 0 {
			frameW, frameH = b.Dx(), b.Dy()
		} else if b.Dx() != frameW || b.Dy() != frameH {
			return PostProcessResult{Op: "stitch_spritesheet", Status: PostProcessFailed, Message: fmt.Sprintf("frame size mismatch: %s is %dx%d, expected %dx%d", filepath.Base(path), b.Dx(), b.Dy(), frameW, frameH)}
		}
		images = append(images, img)
	}

	rows := int(math.Ceil(float64(len(images)) / float64(columns)))
	sheetW := columns * frameW
	sheetH := rows * frameH
	sheet := image.NewNRGBA(image.Rect(0, 0, sheetW, sheetH))

	for i, img := range images {
		col := i % columns
		row := i / columns
		x := col * frameW
		y := row * frameH
		draw.Draw(sheet, image.Rect(x, y, x+frameW, y+frameH), img, img.Bounds().Min, draw.Src)
	}

	ext := filepath.Ext(artifactPath)
	stem := strings.TrimSuffix(filepath.Base(artifactPath), ext)
	dir := filepath.Dir(artifactPath)
	sheetPath := filepath.Join(dir, stem+".png")
	metaPath := filepath.Join(dir, stem+".spritesheet.json")

	fps := 8
	if s := state["fps"]; s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			fps = v
		}
	}

	meta := SpritesheetMeta{
		FrameCount:  len(images),
		Columns:     columns,
		Rows:        rows,
		FrameWidth:  frameW,
		FrameHeight: frameH,
		FPS:         fps,
		SourceVideo: filepath.Base(artifactPath),
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
		return PostProcessResult{Op: "stitch_spritesheet", Status: PostProcessFailed, Message: fmt.Sprintf("writing metadata: %v", err)}
	}

	// Remove any stale Unity .meta for the sheet before overwriting the PNG.
	// Unity's internalIDToNameTable is append-only across re-imports, so when
	// a pipeline re-run produces a different frame_count than a prior run,
	// the old .meta's name-table keeps ghost entries that cause
	// AnimationClip keyframes to resolve to null sprites -- i.e. blinking
	// playback. Deleting the .meta forces Unity to import the PNG fresh and
	// the downstream SpriteSheetPostprocessor (com.anthem.spritesheet Unity
	// package) slices cleanly without any ghost rows.
	unityMetaPath := sheetPath + ".meta"
	if _, err := os.Stat(unityMetaPath); err == nil {
		if rmErr := os.Remove(unityMetaPath); rmErr != nil {
			log.Warn("stitch_spritesheet could not remove stale Unity .meta", "path", unityMetaPath, "error", rmErr)
		} else {
			log.Info("stitch_spritesheet removed stale Unity .meta for clean reimport", "path", unityMetaPath)
		}
	}

	if err := encodePNG(sheetPath, sheet); err != nil {
		os.Remove(metaPath)
		return PostProcessResult{Op: "stitch_spritesheet", Status: PostProcessFailed, Message: fmt.Sprintf("writing sprite sheet: %v", err)}
	}

	if !keepVideo {
		os.Remove(artifactPath)
	}

	state["spritesheet_path"] = sheetPath
	state["metadata_path"] = metaPath

	return PostProcessResult{
		Op:      "stitch_spritesheet",
		Status:  PostProcessApplied,
		Message: fmt.Sprintf("%dx%d sheet (%d frames, %d cols x %d rows) -> %s", sheetW, sheetH, len(images), columns, rows, sheetPath),
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// validateAlpha decodes the PNG at path and checks whether the image has an
// alpha-capable color model and whether any sampled pixels are non-opaque.
func validateAlpha(path string) (hasAlpha bool, fullyOpaque bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return false, false
	}

	switch img.ColorModel() {
	case color.NRGBAModel, color.NRGBA64Model, color.RGBAModel, color.RGBA64Model:
		hasAlpha = true
	default:
		return false, false
	}

	bounds := img.Bounds()
	fullyOpaque = true
	step := 10

	for x := bounds.Min.X; x < bounds.Max.X; x += step {
		_, _, _, a := img.At(x, bounds.Min.Y).RGBA()
		if a < 0xFFFF {
			return true, false
		}
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		_, _, _, a := img.At(bounds.Min.X, y).RGBA()
		if a < 0xFFFF {
			return true, false
		}
	}
	for x := bounds.Min.X; x < bounds.Max.X; x += step {
		_, _, _, a := img.At(x, bounds.Max.Y-1).RGBA()
		if a < 0xFFFF {
			return true, false
		}
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		_, _, _, a := img.At(bounds.Max.X-1, y).RGBA()
		if a < 0xFFFF {
			return true, false
		}
	}

	return true, fullyOpaque
}

func decodePNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	return img, err
}

func encodePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := &png.Encoder{CompressionLevel: png.BestSpeed}
	return enc.Encode(f, img)
}

// listPNGs returns sorted .png file paths from a directory.
func listPNGs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func truncateBytes(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
