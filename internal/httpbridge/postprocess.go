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
	"remove_background":   &RemoveBackgroundProcessor{},
	"extract_video_frames": &ExtractVideoFramesProcessor{},
	"normalize_frames":     &NormalizeFramesProcessor{},
	"stitch_spritesheet":   &StitchSpritesheetProcessor{},
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
// RemoveBackgroundProcessor
// ---------------------------------------------------------------------------

// RemoveBackgroundProcessor removes the background from a PNG image using rembg.
// Supports batch mode: if state["frame_dir"] is set, processes every PNG in that
// directory instead of the single artifactPath. Both LookPath and CmdRunner are
// injectable for testing; nil values fall back to the stdlib equivalents.
type RemoveBackgroundProcessor struct {
	LookPath  func(file string) (string, error)
	CmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

const defaultRembgModel = "isnet-anime"

func (p *RemoveBackgroundProcessor) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	cmdRunner := p.CmdRunner
	if cmdRunner == nil {
		cmdRunner = exec.CommandContext
	}

	rembgPath, err := lookPath("rembg")
	if err != nil {
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessSkipped,
			Message: "rembg not installed",
		}
	}

	model := cfg["model"]
	if model == "" {
		model = defaultRembgModel
	}

	if frameDir := state["frame_dir"]; frameDir != "" {
		return p.runBatch(frameDir, rembgPath, model, cmdRunner, log)
	}
	return p.runSingle(artifactPath, rembgPath, model, cmdRunner, log)
}

func (p *RemoveBackgroundProcessor) runSingle(artifactPath, rembgPath, model string, cmdRunner func(context.Context, string, ...string) *exec.Cmd, log *slog.Logger) PostProcessResult {
	tmpFile, err := os.CreateTemp("", "rembg-*.png")
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := cmdRunner(ctx, rembgPath, "i", "-m", model, artifactPath, tmpPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("rembg failed: %v: %s", err, truncateBytes(output, 200)),
		}
	}

	hasAlpha, fullyOpaque := validateAlpha(tmpPath)
	if !hasAlpha {
		log.Warn("rembg output is not an RGBA image, keeping original", "path", artifactPath)
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessFailed,
			Message: "rembg output is not an RGBA image",
		}
	}
	if fullyOpaque {
		log.Warn("rembg output appears fully opaque, background removal may not have been effective",
			"path", artifactPath, "model", model)
	}

	if err := os.Rename(tmpPath, artifactPath); err != nil {
		return PostProcessResult{
			Op:      "remove_background",
			Status:  PostProcessFailed,
			Message: fmt.Sprintf("replacing artifact: %v", err),
		}
	}

	msg := model
	if fullyOpaque {
		msg += "; warning: output appears fully opaque"
	}
	return PostProcessResult{
		Op:      "remove_background",
		Status:  PostProcessApplied,
		Message: msg,
	}
}

func (p *RemoveBackgroundProcessor) runBatch(frameDir, rembgPath, model string, cmdRunner func(context.Context, string, ...string) *exec.Cmd, log *slog.Logger) PostProcessResult {
	frames, err := listPNGs(frameDir)
	if err != nil {
		return PostProcessResult{Op: "remove_background", Status: PostProcessFailed, Message: fmt.Sprintf("listing frames: %v", err)}
	}
	if len(frames) == 0 {
		return PostProcessResult{Op: "remove_background", Status: PostProcessSkipped, Message: "no frames in frame_dir"}
	}

	for _, framePath := range frames {
		tmpFile, err := os.CreateTemp(frameDir, "rembg-*.png")
		if err != nil {
			return PostProcessResult{Op: "remove_background", Status: PostProcessFailed, Message: fmt.Sprintf("creating temp file: %v", err)}
		}
		tmpPath := tmpFile.Name()
		tmpFile.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := cmdRunner(ctx, rembgPath, "i", "-m", model, framePath, tmpPath)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			os.Remove(tmpPath)
			return PostProcessResult{Op: "remove_background", Status: PostProcessFailed, Message: fmt.Sprintf("rembg failed on %s: %v: %s", filepath.Base(framePath), err, truncateBytes(output, 200))}
		}
		if err := os.Rename(tmpPath, framePath); err != nil {
			os.Remove(tmpPath)
			return PostProcessResult{Op: "remove_background", Status: PostProcessFailed, Message: fmt.Sprintf("replacing frame: %v", err)}
		}
	}

	return PostProcessResult{
		Op:      "remove_background",
		Status:  PostProcessApplied,
		Message: fmt.Sprintf("%s; %d frames processed", model, len(frames)),
	}
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

const defaultNormPadding = "4"

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
		bbox := contentBoundingBox(img)
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

// contentBoundingBox returns the tightest rectangle containing all non-fully-transparent pixels.
func contentBoundingBox(img image.Image) image.Rectangle {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a > 0 {
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

// ---------------------------------------------------------------------------
// StitchSpritesheetProcessor
// ---------------------------------------------------------------------------

// StitchSpritesheetProcessor packs individual frame PNGs into a grid sprite
// sheet and writes a metadata sidecar JSON file.
type StitchSpritesheetProcessor struct{}

const defaultStitchColumns = "4"

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

	if err := encodePNG(sheetPath, sheet); err != nil {
		return PostProcessResult{Op: "stitch_spritesheet", Status: PostProcessFailed, Message: fmt.Sprintf("writing sprite sheet: %v", err)}
	}

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

	if !keepVideo {
		os.Remove(artifactPath)
	}
	os.RemoveAll(frameDir)

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
	return png.Encode(f, img)
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

func truncateBytes(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
