package httpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"log/slog"

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
	"remove_background":       &RemoveBackgroundProcessor{},
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
//   - Python binary: MATTE_PYTHON env var if set; otherwise lookPath("python3")
//     then lookPath("python"). Explicit env override wins even when neither
//     name resolves on PATH (e.g. a venv interpreter).
//   - Script path: MATTE_SCRIPT env var if set; otherwise derived from
//     ANTHEM_HTTP_BRIDGE_ROOT + "tools/matte/matte.py", falling back to the
//     process cwd if the env var is unset.
//
// Returns a skip-friendly error when either leg is missing so processors can
// emit PostProcessSkipped instead of PostProcessFailed — matting is optional
// tooling and a fresh-clone environment should not fail the pipeline.
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

	script := os.Getenv("MATTE_SCRIPT")
	if script == "" {
		root := os.Getenv("ANTHEM_HTTP_BRIDGE_ROOT")
		if root == "" {
			if wd, err := os.Getwd(); err == nil {
				root = wd
			}
		}
		if root != "" {
			script = filepath.Join(root, "tools", "matte", "matte.py")
		}
	}
	if script == "" {
		return nil, fmt.Errorf("matte sidecar: script path not resolvable (set MATTE_SCRIPT or ANTHEM_HTTP_BRIDGE_ROOT)")
	}
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("matte sidecar: %w", err)
	}
	return &matteSidecar{Python: python, Script: script}, nil
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
// RemoveBackgroundProcessor
// ---------------------------------------------------------------------------

// RemoveBackgroundProcessor removes the background from a PNG image using the
// BiRefNet-backed matting sidecar. The legacy rembg backend and batch-mode
// (state["frame_dir"]) entry point have both been removed — video matting is
// now handled by VideoMatteProcessor, and still-image matting has a single
// clean backend. Both LookPath and CmdRunner are injectable for tests; nil
// values fall back to the stdlib equivalents.
type RemoveBackgroundProcessor struct {
	LookPath  func(file string) (string, error)
	CmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func (p *RemoveBackgroundProcessor) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
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
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessSkipped,
			Message: err.Error(),
		}
	}

	tmpFile, err := os.CreateTemp("", "birefnet-*.png")
	if err != nil {
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("creating temp file: %v", err),
		}
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := cmdRunner(ctx, sidecar.Python, sidecar.Script, "image",
		"--input", artifactPath,
		"--output", tmpPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("birefnet sidecar failed: %v: %s", err, truncateBytes(output, 500)),
		}
	}

	hasAlpha, fullyOpaque := validateAlpha(tmpPath)
	if !hasAlpha {
		log.Warn("birefnet output is not an RGBA image, keeping original", "path", artifactPath)
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessFailed,
			Message: "birefnet output is not an RGBA image",
		}
	}
	if fullyOpaque {
		log.Warn("birefnet output appears fully opaque, background removal may not have been effective",
			"path", artifactPath)
	}

	if err := os.Rename(tmpPath, artifactPath); err != nil {
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("replacing artifact: %v", err),
		}
	}

	msg := "birefnet"
	if fullyOpaque {
		msg += "; warning: output appears fully opaque"
	}
	return PostProcessResult{
		Op:      "remove_background",
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
	cmd := cmdRunner(ctx, sidecar.Python, args...)
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	if err != nil {
		return PostProcessResult{
			Op:      "video_matte",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("sidecar failed: %v: %s", err, truncateBytes([]byte(stderrBuf.String()), 500)),
		}
	}

	var res videoMatteResult
	if jerr := extractSidecarJSON([]byte(stdoutBuf.String()), &res); jerr != nil {
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
	Status           string    `json:"status"`
	Device           string    `json:"device"`
	FramesProcessed  int       `json:"frames_processed"`
	AnchorThreshold  float64   `json:"anchor_threshold"`
	WindowThreshold  float64   `json:"window_threshold"`
	WindowSize       int       `json:"window_size"`
	AnchorScores     []float64 `json:"anchor_scores"`
	WindowScores     []float64 `json:"window_scores"`
	AnchorOutliers   []int     `json:"anchor_outliers"`
	WindowOutliers   []int     `json:"window_outliers"`
	ElapsedMS        int       `json:"elapsed_ms"`
	Error            string    `json:"error,omitempty"`
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
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	if err := cmd.Run(); err != nil {
		return PostProcessResult{
			Op:      "check_frame_consistency",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("sidecar failed: %v: %s", err, truncateBytes([]byte(stderrBuf.String()), 500)),
		}
	}

	var res driftResult
	if jerr := extractSidecarJSON([]byte(stdoutBuf.String()), &res); jerr != nil {
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

	for i, fb := range loaded {
		canvas := image.NewNRGBA(image.Rect(0, 0, canvasW, canvasH))
		srcBounds := fb.img.Bounds()
		offsetX := padding - unionBBox.Min.X + srcBounds.Min.X
		offsetY := padding - unionBBox.Min.Y + srcBounds.Min.Y
		draw.Draw(canvas, image.Rect(offsetX, offsetY, offsetX+srcBounds.Dx(), offsetY+srcBounds.Dy()), fb.img, srcBounds.Min, draw.Src)

		if err := encodePNG(frames[i], canvas); err != nil {
			return PostProcessResult{Op: "normalize_frames", Status: PostProcessFailed, Message: fmt.Sprintf("writing %s: %v", filepath.Base(frames[i]), err)}
		}
	}

	state["normalized_width"] = strconv.Itoa(canvasW)
	state["normalized_height"] = strconv.Itoa(canvasH)

	return PostProcessResult{
		Op:      "normalize_frames",
		Status:  PostProcessApplied,
		Message: fmt.Sprintf("%d frames normalized to %dx%d (padding=%d)", len(frames), canvasW, canvasH, padding),
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
