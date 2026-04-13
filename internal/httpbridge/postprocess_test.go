package httpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/guests"
)

// writePNG creates a minimal PNG at path with the given color model.
// If transparent is true, edge pixels have alpha=0; otherwise all pixels are
// fully opaque.
func writePNG(t *testing.T, path string, transparent bool) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			if transparent && (x == 0 || y == 0 || x == 19 || y == 19) {
				img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating test PNG: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
}

// writeSizedPNG creates a PNG with specific content region for normalization tests.
// contentRect defines where opaque pixels are drawn; the rest is transparent.
func writeSizedPNG(t *testing.T, path string, w, h int, contentRect image.Rectangle) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := contentRect.Min.Y; y < contentRect.Max.Y; y++ {
		for x := contentRect.Min.X; x < contentRect.Max.X; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating test PNG: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
}

// failCmd returns an exec.Cmd that exits with code 1 on any platform.
func failCmd(ctx context.Context) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", "exit", "1")
	}
	return exec.CommandContext(ctx, "false")
}

// noopCmd returns an exec.Cmd that succeeds silently on any platform.
func noopCmd(ctx context.Context) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", "echo", "ok")
	}
	return exec.CommandContext(ctx, "true")
}

// copyFileCmd creates a platform-appropriate exec.Cmd that copies src to dst.
func copyFileCmd(ctx context.Context, src, dst string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", "copy", "/y", filepath.FromSlash(src), filepath.FromSlash(dst))
	}
	return exec.CommandContext(ctx, "cp", src, dst)
}

// mockCmdRunner returns a CmdRunner that copies src to the command's output
// path (the last argument).
func mockCmdRunner(src string, fail bool) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if fail {
			return failCmd(ctx)
		}
		outPath := args[len(args)-1]
		return copyFileCmd(ctx, src, outPath)
	}
}

// ---------------------------------------------------------------------------
// RemoveBackgroundProcessor -- single-file mode
// ---------------------------------------------------------------------------

func TestRemoveBackground_RembgNotInstalled(t *testing.T) {
	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "", fmt.Errorf("not found") },
	}
	dir := t.TempDir()
	artifact := filepath.Join(dir, "test.png")
	writePNG(t, artifact, false)

	r := proc.Run(artifact, nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessSkipped {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessSkipped)
	}
	if !strings.Contains(r.Message, "not installed") {
		t.Errorf("Message = %q, want mention of 'not installed'", r.Message)
	}
}

func TestRemoveBackground_Success(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "sprite.png")
	writePNG(t, artifact, false)
	transparentSrc := filepath.Join(dir, "transparent_src.png")
	writePNG(t, transparentSrc, true)

	proc := &RemoveBackgroundProcessor{
		LookPath:  func(string) (string, error) { return "rembg", nil },
		CmdRunner: mockCmdRunner(transparentSrc, false),
	}
	r := proc.Run(artifact, map[string]string{"model": "u2net"}, PipelineState{}, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q, want %q; Message: %s", r.Status, PostProcessApplied, r.Message)
	}
	if !strings.Contains(r.Message, "u2net") {
		t.Errorf("Message = %q, should mention model name", r.Message)
	}
}

func TestRemoveBackground_FullyOpaque(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "sprite.png")
	writePNG(t, artifact, false)
	opaqueSrc := filepath.Join(dir, "opaque_src.png")
	writePNG(t, opaqueSrc, false)

	proc := &RemoveBackgroundProcessor{
		LookPath:  func(string) (string, error) { return "rembg", nil },
		CmdRunner: mockCmdRunner(opaqueSrc, false),
	}
	r := proc.Run(artifact, nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q, want %q; Message: %s", r.Status, PostProcessApplied, r.Message)
	}
	if !strings.Contains(r.Message, "fully opaque") {
		t.Errorf("Message = %q, should warn about fully opaque output", r.Message)
	}
}

func TestRemoveBackground_CmdFails(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "sprite.png")
	writePNG(t, artifact, false)
	origData, _ := os.ReadFile(artifact)

	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return failCmd(ctx)
		},
	}
	r := proc.Run(artifact, nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessFailed {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessFailed)
	}
	afterData, _ := os.ReadFile(artifact)
	if string(afterData) != string(origData) {
		t.Error("original artifact should be preserved when command fails")
	}
}

func TestRemoveBackground_DefaultModel(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "sprite.png")
	writePNG(t, artifact, false)
	transparentSrc := filepath.Join(dir, "transparent_src.png")
	writePNG(t, transparentSrc, true)

	var capturedArgs []string
	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			capturedArgs = args
			return mockCmdRunner(transparentSrc, false)(ctx, name, args...)
		},
	}
	r := proc.Run(artifact, nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	found := false
	for i, arg := range capturedArgs {
		if arg == "-m" && i+1 < len(capturedArgs) && capturedArgs[i+1] == "isnet-anime" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected default model isnet-anime in args: %v", capturedArgs)
	}
}

func TestRemoveBackground_CustomModel(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "sprite.png")
	writePNG(t, artifact, false)
	transparentSrc := filepath.Join(dir, "transparent_src.png")
	writePNG(t, transparentSrc, true)

	var capturedArgs []string
	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			capturedArgs = args
			return mockCmdRunner(transparentSrc, false)(ctx, name, args...)
		},
	}
	r := proc.Run(artifact, map[string]string{"model": "u2net"}, PipelineState{}, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	found := false
	for i, arg := range capturedArgs {
		if arg == "-m" && i+1 < len(capturedArgs) && capturedArgs[i+1] == "u2net" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected custom model u2net in args: %v", capturedArgs)
	}
}

func TestRemoveBackground_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "sprite.png")
	writePNG(t, artifact, false)
	transparentSrc := filepath.Join(dir, "transparent_src.png")
	writePNG(t, transparentSrc, true)

	var tempFilePath string
	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			tempFilePath = args[len(args)-1]
			return mockCmdRunner(transparentSrc, false)(ctx, name, args...)
		},
	}
	r := proc.Run(artifact, nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	if tempFilePath == "" {
		t.Fatal("temp file path not captured")
	}
	if tempFilePath == artifact {
		t.Error("rembg should write to a temp file, not the artifact itself")
	}
}

// ---------------------------------------------------------------------------
// RemoveBackgroundProcessor -- batch mode
// ---------------------------------------------------------------------------

func TestRemoveBackground_BatchMode(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	for i := 1; i <= 3; i++ {
		writePNG(t, filepath.Join(frameDir, fmt.Sprintf("frame_%04d.png", i)), false)
	}
	transparentSrc := filepath.Join(dir, "transparent_src.png")
	writePNG(t, transparentSrc, true)

	var callCount int
	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			callCount++
			return mockCmdRunner(transparentSrc, false)(ctx, name, args...)
		},
	}

	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run(filepath.Join(dir, "video.mp4"), nil, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	if callCount != 3 {
		t.Errorf("expected 3 rembg calls, got %d", callCount)
	}
	if !strings.Contains(r.Message, "3 frames") {
		t.Errorf("Message = %q, should mention frame count", r.Message)
	}
}

func TestRemoveBackground_BatchNoFrames(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "empty_frames")
	_ = os.Mkdir(frameDir, 0755)

	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
	}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run(filepath.Join(dir, "video.mp4"), nil, state, slog.Default())
	if r.Status != PostProcessSkipped {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessSkipped)
	}
}

// ---------------------------------------------------------------------------
// ExtractVideoFramesProcessor
// ---------------------------------------------------------------------------

func TestExtractVideoFrames_FfmpegNotInstalled(t *testing.T) {
	proc := &ExtractVideoFramesProcessor{
		LookPath: func(string) (string, error) { return "", fmt.Errorf("not found") },
	}
	r := proc.Run("video.mp4", nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessSkipped {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessSkipped)
	}
	if !strings.Contains(r.Message, "ffmpeg not installed") {
		t.Errorf("Message = %q, want mention of 'ffmpeg not installed'", r.Message)
	}
}

func TestExtractVideoFrames_Success(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	_ = os.WriteFile(videoPath, []byte("fake video"), 0644)

	proc := &ExtractVideoFramesProcessor{
		LookPath: func(string) (string, error) { return "ffmpeg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			outPattern := args[len(args)-1]
			outDir := filepath.Dir(outPattern)
			for i := 1; i <= 4; i++ {
				writePNGToPath(filepath.Join(outDir, fmt.Sprintf("frame_%04d.png", i)), 32, 32)
			}
			return noopCmd(ctx)
		},
	}

	state := PipelineState{}
	r := proc.Run(videoPath, map[string]string{"fps": "10"}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	if state["frame_dir"] == "" {
		t.Error("expected frame_dir in pipeline state")
	}
	if state["frame_count"] != "4" {
		t.Errorf("frame_count = %q, want 4", state["frame_count"])
	}
	if state["fps"] != "10" {
		t.Errorf("fps = %q, want 10", state["fps"])
	}
	if !strings.Contains(r.Message, "4 frames") {
		t.Errorf("Message = %q, should mention frame count", r.Message)
	}
}

func TestExtractVideoFrames_DefaultFPS(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	_ = os.WriteFile(videoPath, []byte("fake"), 0644)

	var capturedArgs []string
	proc := &ExtractVideoFramesProcessor{
		LookPath: func(string) (string, error) { return "ffmpeg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			capturedArgs = args
			outPattern := args[len(args)-1]
			outDir := filepath.Dir(outPattern)
			writePNGToPath(filepath.Join(outDir, "frame_0001.png"), 32, 32)
			return noopCmd(ctx)
		},
	}

	state := PipelineState{}
	r := proc.Run(videoPath, nil, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	found := false
	for _, arg := range capturedArgs {
		if arg == "fps=8" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected default fps=8 in args: %v", capturedArgs)
	}
}

func TestExtractVideoFrames_CmdFails(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "test.mp4")
	_ = os.WriteFile(videoPath, []byte("fake"), 0644)

	proc := &ExtractVideoFramesProcessor{
		LookPath: func(string) (string, error) { return "ffmpeg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return failCmd(ctx)
		},
	}

	state := PipelineState{}
	r := proc.Run(videoPath, nil, state, slog.Default())
	if r.Status != PostProcessFailed {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessFailed)
	}
}

// ---------------------------------------------------------------------------
// NormalizeFramesProcessor
// ---------------------------------------------------------------------------

func TestNormalizeFrames_NoFrameDir(t *testing.T) {
	proc := &NormalizeFramesProcessor{}
	r := proc.Run("irrelevant", nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessSkipped {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessSkipped)
	}
}

func TestNormalizeFrames_UniformCanvas(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	// Frame 1: content at (2,2)-(8,8) within 20x20
	writeSizedPNG(t, filepath.Join(frameDir, "frame_0001.png"), 20, 20, image.Rect(2, 2, 8, 8))
	// Frame 2: content at (10,10)-(18,18) within 20x20
	writeSizedPNG(t, filepath.Join(frameDir, "frame_0002.png"), 20, 20, image.Rect(10, 10, 18, 18))

	proc := &NormalizeFramesProcessor{}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run("irrelevant", map[string]string{"padding": "2"}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// Union bbox should be (2,2)-(18,18) = 16x16 content, + 2px padding = 20x20
	if state["normalized_width"] != "20" {
		t.Errorf("normalized_width = %q, want 20", state["normalized_width"])
	}
	if state["normalized_height"] != "20" {
		t.Errorf("normalized_height = %q, want 20", state["normalized_height"])
	}

	// Verify both frames have the same dimensions
	img1, err := decodePNG(filepath.Join(frameDir, "frame_0001.png"))
	if err != nil {
		t.Fatal(err)
	}
	img2, err := decodePNG(filepath.Join(frameDir, "frame_0002.png"))
	if err != nil {
		t.Fatal(err)
	}
	if img1.Bounds() != img2.Bounds() {
		t.Errorf("frames should have identical bounds: %v vs %v", img1.Bounds(), img2.Bounds())
	}
}

func TestNormalizeFrames_AllTransparent(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	// Frame with no opaque pixels
	writeSizedPNG(t, filepath.Join(frameDir, "frame_0001.png"), 20, 20, image.Rect(0, 0, 0, 0))

	proc := &NormalizeFramesProcessor{}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run("irrelevant", nil, state, slog.Default())
	if r.Status != PostProcessSkipped {
		t.Errorf("Status = %q, want %q for all-transparent frames", r.Status, PostProcessSkipped)
	}
}

func TestNormalizeFrames_DefaultPadding(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	// Frame with content at (5,5)-(15,15) = 10x10 content
	writeSizedPNG(t, filepath.Join(frameDir, "frame_0001.png"), 20, 20, image.Rect(5, 5, 15, 15))

	proc := &NormalizeFramesProcessor{}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run("irrelevant", nil, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// 10x10 content + 4px default padding = 18x18
	if state["normalized_width"] != "18" {
		t.Errorf("normalized_width = %q, want 18", state["normalized_width"])
	}
	if state["normalized_height"] != "18" {
		t.Errorf("normalized_height = %q, want 18", state["normalized_height"])
	}
}

// ---------------------------------------------------------------------------
// StitchSpritesheetProcessor
// ---------------------------------------------------------------------------

func TestStitchSpritesheet_NoFrameDir(t *testing.T) {
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run("irrelevant", nil, PipelineState{}, slog.Default())
	if r.Status != PostProcessSkipped {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessSkipped)
	}
}

func TestStitchSpritesheet_Success(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	for i := 1; i <= 6; i++ {
		writePNGToPath(filepath.Join(frameDir, fmt.Sprintf("frame_%04d.png", i)), 32, 32)
	}

	videoPath := filepath.Join(dir, "goblin-walk.mp4")
	_ = os.WriteFile(videoPath, []byte("fake video"), 0644)

	state := PipelineState{"frame_dir": frameDir, "fps": "10"}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(videoPath, map[string]string{"columns": "3"}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// Verify sprite sheet exists
	sheetPath := filepath.Join(dir, "goblin-walk.png")
	if _, err := os.Stat(sheetPath); err != nil {
		t.Fatalf("sprite sheet not created: %v", err)
	}

	// Verify sheet dimensions: 3 cols x 2 rows of 32x32 = 96x64
	img, err := decodePNG(sheetPath)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != 96 || b.Dy() != 64 {
		t.Errorf("sheet dimensions = %dx%d, want 96x64", b.Dx(), b.Dy())
	}

	// Verify metadata sidecar
	metaPath := filepath.Join(dir, "goblin-walk.spritesheet.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("metadata sidecar not created: %v", err)
	}
	var meta SpritesheetMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("invalid metadata JSON: %v", err)
	}
	if meta.FrameCount != 6 {
		t.Errorf("meta.FrameCount = %d, want 6", meta.FrameCount)
	}
	if meta.Columns != 3 {
		t.Errorf("meta.Columns = %d, want 3", meta.Columns)
	}
	if meta.Rows != 2 {
		t.Errorf("meta.Rows = %d, want 2", meta.Rows)
	}
	if meta.FrameWidth != 32 || meta.FrameHeight != 32 {
		t.Errorf("meta frame size = %dx%d, want 32x32", meta.FrameWidth, meta.FrameHeight)
	}
	if meta.FPS != 10 {
		t.Errorf("meta.FPS = %d, want 10", meta.FPS)
	}
	if meta.SourceVideo != "goblin-walk.mp4" {
		t.Errorf("meta.SourceVideo = %q, want goblin-walk.mp4", meta.SourceVideo)
	}

	// Verify MP4 deleted (keep_video defaults to false)
	if _, err := os.Stat(videoPath); !os.IsNotExist(err) {
		t.Error("MP4 should be deleted when keep_video is not set")
	}

	// Verify frame dir cleaned up
	if _, err := os.Stat(frameDir); !os.IsNotExist(err) {
		t.Error("frame dir should be cleaned up after stitching")
	}

	// Verify pipeline state
	if state["spritesheet_path"] != sheetPath {
		t.Errorf("state[spritesheet_path] = %q, want %q", state["spritesheet_path"], sheetPath)
	}
	if state["metadata_path"] != metaPath {
		t.Errorf("state[metadata_path] = %q, want %q", state["metadata_path"], metaPath)
	}
}

func TestStitchSpritesheet_KeepVideo(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	writePNGToPath(filepath.Join(frameDir, "frame_0001.png"), 16, 16)

	videoPath := filepath.Join(dir, "anim.mp4")
	_ = os.WriteFile(videoPath, []byte("fake"), 0644)

	state := PipelineState{"frame_dir": frameDir}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(videoPath, map[string]string{"keep_video": "true"}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Error("MP4 should be kept when keep_video=true")
	}
}

func TestStitchSpritesheet_FrameSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	writePNGToPath(filepath.Join(frameDir, "frame_0001.png"), 32, 32)
	writePNGToPath(filepath.Join(frameDir, "frame_0002.png"), 64, 64)

	state := PipelineState{"frame_dir": frameDir}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(filepath.Join(dir, "test.mp4"), nil, state, slog.Default())
	if r.Status != PostProcessFailed {
		t.Errorf("Status = %q, want %q for size mismatch", r.Status, PostProcessFailed)
	}
	if !strings.Contains(r.Message, "mismatch") {
		t.Errorf("Message = %q, should mention mismatch", r.Message)
	}
}

func TestStitchSpritesheet_DefaultColumns(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	for i := 1; i <= 5; i++ {
		writePNGToPath(filepath.Join(frameDir, fmt.Sprintf("frame_%04d.png", i)), 16, 16)
	}

	videoPath := filepath.Join(dir, "anim.mp4")
	_ = os.WriteFile(videoPath, []byte("fake"), 0644)

	state := PipelineState{"frame_dir": frameDir}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(videoPath, nil, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// 5 frames with default 4 columns = 4 cols x 2 rows = 64x32
	img, err := decodePNG(filepath.Join(dir, "anim.png"))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != 64 || b.Dy() != 32 {
		t.Errorf("sheet dimensions = %dx%d, want 64x32", b.Dx(), b.Dy())
	}
}

// ---------------------------------------------------------------------------
// RunPostProcess -- pipeline state passing
// ---------------------------------------------------------------------------

func TestRunPostProcess_PipelineStatePassing(t *testing.T) {
	ops := []guests.PostProcessOp{
		{Op: "extract_video_frames", Config: map[string]string{"fps": "8"}},
		{Op: "unknown_op_skipped"},
	}

	origProcessors := processors
	defer func() { processors = origProcessors }()

	var receivedState PipelineState
	processors = map[string]PostProcessor{
		"extract_video_frames": &testStateWriter{key: "frame_dir", value: "/tmp/frames"},
	}

	results, state := RunPostProcess(ops, "test.mp4", slog.Default())
	_ = receivedState
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Status != PostProcessApplied {
		t.Errorf("first op Status = %q, want applied", results[0].Status)
	}
	if results[1].Status != PostProcessSkipped {
		t.Errorf("second op Status = %q, want skipped", results[1].Status)
	}
	if state["frame_dir"] != "/tmp/frames" {
		t.Errorf("returned state[frame_dir] = %q, want %q", state["frame_dir"], "/tmp/frames")
	}
}

type testStateWriter struct {
	key, value string
}

func (w *testStateWriter) Run(artifactPath string, cfg map[string]string, state PipelineState, log *slog.Logger) PostProcessResult {
	state[w.key] = w.value
	return PostProcessResult{Op: "test_state_writer", Status: PostProcessApplied, Message: "wrote " + w.key}
}

func TestRunPostProcess_UnknownOp(t *testing.T) {
	ops := []guests.PostProcessOp{{Op: "nonexistent_op"}}
	results, _ := RunPostProcess(ops, "irrelevant.png", slog.Default())
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != PostProcessSkipped {
		t.Errorf("Status = %q, want %q", results[0].Status, PostProcessSkipped)
	}
}

func TestRunPostProcess_MultipleOps(t *testing.T) {
	ops := []guests.PostProcessOp{
		{Op: "unknown_first"},
		{Op: "unknown_second"},
	}
	results, _ := RunPostProcess(ops, "irrelevant.png", slog.Default())
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Op != "unknown_first" {
		t.Errorf("results[0].Op = %q", results[0].Op)
	}
	if results[1].Op != "unknown_second" {
		t.Errorf("results[1].Op = %q", results[1].Op)
	}
}

func TestRunPostProcess_EmptyOps(t *testing.T) {
	results, state := RunPostProcess(nil, "irrelevant.png", slog.Default())
	if results != nil {
		t.Errorf("expected nil results for empty ops, got %v", results)
	}
	if len(state) != 0 {
		t.Errorf("expected empty state for nil ops, got %v", state)
	}
	results, state = RunPostProcess([]guests.PostProcessOp{}, "irrelevant.png", slog.Default())
	if results != nil {
		t.Errorf("expected nil results for empty slice, got %v", results)
	}
	if len(state) != 0 {
		t.Errorf("expected empty state for empty slice, got %v", state)
	}
}

func TestFormatResults(t *testing.T) {
	results := []PostProcessResult{
		{Op: "remove_background", Status: PostProcessApplied, Message: "isnet-anime"},
		{Op: "unknown", Status: PostProcessSkipped, Message: "unknown operation"},
	}
	msg := FormatResults("Saved image/png to file.png", results)
	if !strings.Contains(msg, "remove_background: applied (isnet-anime)") {
		t.Errorf("formatted message missing applied line: %s", msg)
	}
	if !strings.Contains(msg, "unknown: skipped (unknown operation)") {
		t.Errorf("formatted message missing skipped line: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// validateAlpha
// ---------------------------------------------------------------------------

func TestValidateAlpha_TransparentPNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transparent.png")
	writePNG(t, path, true)
	hasAlpha, fullyOpaque := validateAlpha(path)
	if !hasAlpha {
		t.Error("expected hasAlpha=true")
	}
	if fullyOpaque {
		t.Error("expected fullyOpaque=false")
	}
}

func TestValidateAlpha_OpaquePNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opaque.png")
	writePNG(t, path, false)
	hasAlpha, fullyOpaque := validateAlpha(path)
	if !hasAlpha {
		t.Error("expected hasAlpha=true")
	}
	if !fullyOpaque {
		t.Error("expected fullyOpaque=true")
	}
}

func TestValidateAlpha_NonexistentFile(t *testing.T) {
	hasAlpha, _ := validateAlpha("/nonexistent/file.png")
	if hasAlpha {
		t.Error("expected hasAlpha=false")
	}
}

// ---------------------------------------------------------------------------
// contentBoundingBox
// ---------------------------------------------------------------------------

func TestContentBoundingBox(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for y := 5; y < 15; y++ {
		for x := 3; x < 17; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	bbox := contentBoundingBox(img)
	if bbox.Min.X != 3 || bbox.Min.Y != 5 || bbox.Max.X != 17 || bbox.Max.Y != 15 {
		t.Errorf("bbox = %v, want (3,5)-(17,15)", bbox)
	}
}

func TestContentBoundingBox_AllTransparent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	bbox := contentBoundingBox(img)
	if !bbox.Empty() {
		t.Errorf("expected empty bbox for all-transparent image, got %v", bbox)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writePNGToPath(path string, w, h int) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	f, _ := os.Create(path)
	defer f.Close()
	_ = png.Encode(f, img)
}
