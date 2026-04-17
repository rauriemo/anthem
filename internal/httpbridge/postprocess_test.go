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

	frameNames := []string{"frame_0001.png", "frame_0002.png", "frame_0003.png"}
	for _, name := range frameNames {
		writePNG(t, filepath.Join(frameDir, name), false)
	}
	transparentSrc := filepath.Join(dir, "transparent_src.png")
	writePNG(t, transparentSrc, true)

	var callCount int
	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			callCount++
			outDir := args[len(args)-1]
			for _, fn := range frameNames {
				_ = copyFile(transparentSrc, filepath.Join(outDir, fn))
			}
			return noopCmd(ctx)
		},
	}

	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run(filepath.Join(dir, "video.mp4"), nil, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}
	if callCount != 1 {
		t.Errorf("expected 1 rembg batch call, got %d", callCount)
	}
	if !strings.Contains(r.Message, "3 frames") {
		t.Errorf("Message = %q, should mention frame count", r.Message)
	}
	if !strings.Contains(r.Message, "batch") {
		t.Errorf("Message = %q, should mention batch mode", r.Message)
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

func TestRemoveBackground_BatchFails(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	writePNG(t, filepath.Join(frameDir, "frame_0001.png"), false)

	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return failCmd(ctx)
		},
	}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run(filepath.Join(dir, "video.mp4"), nil, state, slog.Default())
	if r.Status != PostProcessFailed {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessFailed)
	}
	if !strings.Contains(r.Message, "rembg batch failed") {
		t.Errorf("Message = %q, should mention batch failure", r.Message)
	}
}

func TestRemoveBackground_BatchNoOutput(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	writePNG(t, filepath.Join(frameDir, "frame_0001.png"), false)

	proc := &RemoveBackgroundProcessor{
		LookPath: func(string) (string, error) { return "rembg", nil },
		CmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return noopCmd(ctx)
		},
	}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run(filepath.Join(dir, "video.mp4"), nil, state, slog.Default())
	if r.Status != PostProcessFailed {
		t.Errorf("Status = %q, want %q", r.Status, PostProcessFailed)
	}
	if !strings.Contains(r.Message, "no output") {
		t.Errorf("Message = %q, should mention no output", r.Message)
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
	bbox := contentBoundingBox(img, 0)
	if bbox.Min.X != 3 || bbox.Min.Y != 5 || bbox.Max.X != 17 || bbox.Max.Y != 15 {
		t.Errorf("bbox = %v, want (3,5)-(17,15)", bbox)
	}
}

func TestContentBoundingBox_AllTransparent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	bbox := contentBoundingBox(img, 0)
	if !bbox.Empty() {
		t.Errorf("expected empty bbox for all-transparent image, got %v", bbox)
	}
}

func TestContentBoundingBox_AlphaThreshold(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	// Ghost pixels at (0,0)-(3,3) with very low alpha
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 2})
		}
	}
	// Real content at (5,5)-(10,10)
	for y := 5; y < 10; y++ {
		for x := 5; x < 10; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}

	bbox := contentBoundingBox(img, 0x1000)
	if bbox.Min.X != 5 || bbox.Min.Y != 5 || bbox.Max.X != 10 || bbox.Max.Y != 10 {
		t.Errorf("bbox = %v, want (5,5)-(10,10) — ghost pixels should be excluded", bbox)
	}
}

func TestContentBoundingBox_ZeroThreshold(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 2})
		}
	}
	for y := 5; y < 10; y++ {
		for x := 5; x < 10; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}

	bbox := contentBoundingBox(img, 0)
	if bbox.Min.X != 0 || bbox.Min.Y != 0 || bbox.Max.X != 10 || bbox.Max.Y != 10 {
		t.Errorf("bbox = %v, want (0,0)-(10,10) — zero threshold should include ghost pixels", bbox)
	}
}

func TestFloorAlpha(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 0})   // fully transparent
	img.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 2})   // ghost (below threshold)
	img.SetNRGBA(2, 0, color.NRGBA{R: 255, A: 100}) // real semi-transparent
	img.SetNRGBA(3, 0, color.NRGBA{R: 255, A: 255}) // fully opaque

	floorAlpha(img, 0x1000)

	if img.Pix[0*4+3] != 0 {
		t.Errorf("pixel 0: alpha = %d, want 0 (was already transparent)", img.Pix[0*4+3])
	}
	if img.Pix[1*4+3] != 0 {
		t.Errorf("pixel 1: alpha = %d, want 0 (ghost should be floored)", img.Pix[1*4+3])
	}
	if img.Pix[2*4+3] != 100 {
		t.Errorf("pixel 2: alpha = %d, want 100 (real semi-transparent untouched)", img.Pix[2*4+3])
	}
	if img.Pix[3*4+3] != 255 {
		t.Errorf("pixel 3: alpha = %d, want 255 (opaque untouched)", img.Pix[3*4+3])
	}
}

func TestFloorAlpha_ZeroThreshold(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 1})
	img.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 255})

	floorAlpha(img, 0)

	if img.Pix[0*4+3] != 1 {
		t.Errorf("pixel 0: alpha = %d, want 1 (zero threshold should not floor anything)", img.Pix[0*4+3])
	}
	if img.Pix[1*4+3] != 255 {
		t.Errorf("pixel 1: alpha = %d, want 255", img.Pix[1*4+3])
	}
}

// ---------------------------------------------------------------------------
// binarizeAlpha
// ---------------------------------------------------------------------------

func TestBinarizeAlpha(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 5, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 0})   // fully transparent
	img.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 50})  // below 50%
	img.SetNRGBA(2, 0, color.NRGBA{R: 255, A: 128}) // above 50%
	img.SetNRGBA(3, 0, color.NRGBA{R: 255, A: 200}) // well above
	img.SetNRGBA(4, 0, color.NRGBA{R: 255, A: 255}) // fully opaque

	binarizeAlpha(img, 0x8000) // 50% threshold

	cases := []struct {
		px   int
		want uint8
		desc string
	}{
		{0, 0, "transparent stays 0"},
		{1, 0, "A=50 snaps to 0"},
		{2, 255, "A=128 snaps to 255"},
		{3, 255, "A=200 snaps to 255"},
		{4, 255, "A=255 stays 255"},
	}
	for _, tc := range cases {
		got := img.Pix[tc.px*4+3]
		if got != tc.want {
			t.Errorf("pixel %d (%s): alpha = %d, want %d", tc.px, tc.desc, got, tc.want)
		}
	}
}

func TestBinarizeAlpha_PreservesRGB(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 100, G: 150, B: 200, A: 180}) // above threshold
	img.SetNRGBA(1, 0, color.NRGBA{R: 50, G: 60, B: 70, A: 30})     // below threshold

	binarizeAlpha(img, 0x8000)

	c0 := img.NRGBAAt(0, 0)
	if c0.R != 100 || c0.G != 150 || c0.B != 200 {
		t.Errorf("pixel 0 RGB changed: got (%d,%d,%d), want (100,150,200)", c0.R, c0.G, c0.B)
	}
	if c0.A != 255 {
		t.Errorf("pixel 0 alpha = %d, want 255", c0.A)
	}

	c1 := img.NRGBAAt(1, 0)
	if c1.R != 50 || c1.G != 60 || c1.B != 70 {
		t.Errorf("pixel 1 RGB changed: got (%d,%d,%d), want (50,60,70)", c1.R, c1.G, c1.B)
	}
	if c1.A != 0 {
		t.Errorf("pixel 1 alpha = %d, want 0", c1.A)
	}
}

func TestBinarizeAlpha_ZeroThreshold(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 1})
	img.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 128})
	img.SetNRGBA(2, 0, color.NRGBA{R: 255, A: 255})

	binarizeAlpha(img, 0)

	if img.Pix[0*4+3] != 1 {
		t.Errorf("pixel 0: alpha = %d, want 1 (zero threshold is no-op)", img.Pix[0*4+3])
	}
	if img.Pix[1*4+3] != 128 {
		t.Errorf("pixel 1: alpha = %d, want 128 (zero threshold is no-op)", img.Pix[1*4+3])
	}
	if img.Pix[2*4+3] != 255 {
		t.Errorf("pixel 2: alpha = %d, want 255 (zero threshold is no-op)", img.Pix[2*4+3])
	}
}

func TestFloorThenBinarize_Ordering(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 3})   // below floor threshold
	img.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 100}) // above floor, below binarize 50%
	img.SetNRGBA(2, 0, color.NRGBA{R: 255, A: 200}) // above both

	floorAlpha(img, defaultAlphaThreshold)
	binarizeAlpha(img, 0x8000)

	if img.Pix[0*4+3] != 0 {
		t.Errorf("pixel 0: alpha = %d, want 0 (floored to 0, binarize should not resurrect)", img.Pix[0*4+3])
	}
	if img.Pix[1*4+3] != 0 {
		t.Errorf("pixel 1: alpha = %d, want 0 (above floor but below binarize snap)", img.Pix[1*4+3])
	}
	if img.Pix[2*4+3] != 255 {
		t.Errorf("pixel 2: alpha = %d, want 255 (above both thresholds)", img.Pix[2*4+3])
	}
}

// writeGhostPNG creates a PNG with opaque content at contentRect and ghost
// pixels (low alpha) at ghostRect. Everything else is fully transparent.
func writeGhostPNG(t *testing.T, path string, w, h int, contentRect, ghostRect image.Rectangle) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := contentRect.Min.Y; y < contentRect.Max.Y; y++ {
		for x := contentRect.Min.X; x < contentRect.Max.X; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	for y := ghostRect.Min.Y; y < ghostRect.Max.Y; y++ {
		for x := ghostRect.Min.X; x < ghostRect.Max.X; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 200, B: 200, A: 3})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating ghost PNG: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encoding ghost PNG: %v", err)
	}
}

func TestNormalizeFrames_AlphaFloor(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	// Frame 1: opaque content at (5,5)-(10,10), ghost halo at (0,0)-(15,15)
	writeGhostPNG(t, filepath.Join(frameDir, "frame_0001.png"), 20, 20,
		image.Rect(5, 5, 10, 10), image.Rect(0, 0, 15, 15))
	// Frame 2: opaque content at (8,8)-(14,14), ghost halo at (3,3)-(18,18)
	writeGhostPNG(t, filepath.Join(frameDir, "frame_0002.png"), 20, 20,
		image.Rect(8, 8, 14, 14), image.Rect(3, 3, 18, 18))

	proc := &NormalizeFramesProcessor{}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run("irrelevant", map[string]string{"padding": "2"}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// Union of opaque content is (5,5)-(14,14) = 9x9, + 2px padding = 13x13
	if state["normalized_width"] != "13" {
		t.Errorf("normalized_width = %q, want 13 (ghost halo should not inflate bbox)", state["normalized_width"])
	}
	if state["normalized_height"] != "13" {
		t.Errorf("normalized_height = %q, want 13 (ghost halo should not inflate bbox)", state["normalized_height"])
	}

	// Verify ghost pixels were zeroed in output
	img1, err := decodePNG(filepath.Join(frameDir, "frame_0001.png"))
	if err != nil {
		t.Fatal(err)
	}
	nrgba1, ok := img1.(*image.NRGBA)
	if !ok {
		t.Fatal("expected NRGBA output")
	}
	for y := nrgba1.Bounds().Min.Y; y < nrgba1.Bounds().Max.Y; y++ {
		for x := nrgba1.Bounds().Min.X; x < nrgba1.Bounds().Max.X; x++ {
			c := nrgba1.NRGBAAt(x, y)
			if c.A > 0 && c.A < 10 {
				t.Errorf("ghost pixel survived at (%d,%d): A=%d", x, y, c.A)
			}
		}
	}
}

func TestNormalizeFrames_AlphaThresholdConfig(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	// Frame with opaque content at center and ghost pixels in a wide halo
	writeGhostPNG(t, filepath.Join(frameDir, "frame_0001.png"), 20, 20,
		image.Rect(5, 5, 10, 10), image.Rect(0, 0, 18, 18))

	proc := &NormalizeFramesProcessor{}
	state := PipelineState{"frame_dir": frameDir}
	// alpha_threshold=0 means old behavior: ghost pixels count as content
	r := proc.Run("irrelevant", map[string]string{"padding": "0", "alpha_threshold": "0"}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// With threshold 0, bbox includes ghost pixels at (0,0)-(18,18) = 18x18 canvas
	if state["normalized_width"] != "18" {
		t.Errorf("normalized_width = %q, want 18 (zero threshold should include ghost pixels)", state["normalized_width"])
	}
	if state["normalized_height"] != "18" {
		t.Errorf("normalized_height = %q, want 18 (zero threshold should include ghost pixels)", state["normalized_height"])
	}
}

// writeSoftEdgePNG creates a PNG with opaque content at contentRect and
// mid-range semi-transparent pixels (simulating rembg soft-edge bleeding)
// at softRect. Everything else is fully transparent.
func writeSoftEdgePNG(t *testing.T, path string, w, h int, contentRect, softRect image.Rectangle, softAlpha uint8) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := contentRect.Min.Y; y < contentRect.Max.Y; y++ {
		for x := contentRect.Min.X; x < contentRect.Max.X; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	for y := softRect.Min.Y; y < softRect.Max.Y; y++ {
		for x := softRect.Min.X; x < softRect.Max.X; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 100, B: 50, A: softAlpha})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating soft-edge PNG: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encoding soft-edge PNG: %v", err)
	}
}

func TestNormalizeFrames_AlphaSnap(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	// Frames with mid-range alpha pixels (A=100) simulating rembg soft-edge
	writeSoftEdgePNG(t, filepath.Join(frameDir, "frame_0001.png"), 20, 20,
		image.Rect(5, 5, 10, 10), image.Rect(3, 3, 12, 12), 100)
	writeSoftEdgePNG(t, filepath.Join(frameDir, "frame_0002.png"), 20, 20,
		image.Rect(6, 6, 11, 11), image.Rect(4, 4, 13, 13), 120)

	proc := &NormalizeFramesProcessor{}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run("irrelevant", map[string]string{
		"padding":    "0",
		"alpha_snap": "32768",
	}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	for _, name := range []string{"frame_0001.png", "frame_0002.png"} {
		img, err := decodePNG(filepath.Join(frameDir, name))
		if err != nil {
			t.Fatal(err)
		}
		nrgba, ok := img.(*image.NRGBA)
		if !ok {
			t.Fatalf("%s: expected NRGBA output", name)
		}
		for y := nrgba.Bounds().Min.Y; y < nrgba.Bounds().Max.Y; y++ {
			for x := nrgba.Bounds().Min.X; x < nrgba.Bounds().Max.X; x++ {
				a := nrgba.NRGBAAt(x, y).A
				if a != 0 && a != 255 {
					t.Errorf("%s: partial alpha %d at (%d,%d), want 0 or 255", name, a, x, y)
				}
			}
		}
	}
}

func TestNormalizeFrames_AlphaSnapBBox(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	// Opaque content at (5,5)-(10,10) = 5x5, soft-edge halo at (2,2)-(13,13)
	writeSoftEdgePNG(t, filepath.Join(frameDir, "frame_0001.png"), 20, 20,
		image.Rect(5, 5, 10, 10), image.Rect(2, 2, 13, 13), 100)

	proc := &NormalizeFramesProcessor{}
	state := PipelineState{"frame_dir": frameDir}
	r := proc.Run("irrelevant", map[string]string{
		"padding":    "2",
		"alpha_snap": "32768",
	}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// Opaque content is 5x5 + 2px padding each side = 9x9
	if state["normalized_width"] != "9" {
		t.Errorf("normalized_width = %q, want 9 (soft-edge halo should not inflate bbox)", state["normalized_width"])
	}
	if state["normalized_height"] != "9" {
		t.Errorf("normalized_height = %q, want 9 (soft-edge halo should not inflate bbox)", state["normalized_height"])
	}
}

// ---------------------------------------------------------------------------
// StitchSpritesheet -- write ordering and resilience
// ---------------------------------------------------------------------------

func TestStitchSpritesheet_JSONWrittenBeforePNG(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	writePNGToPath(filepath.Join(frameDir, "frame_0001.png"), 16, 16)

	videoPath := filepath.Join(dir, "anim.mp4")
	_ = os.WriteFile(videoPath, []byte("fake"), 0644)

	metaPath := filepath.Join(dir, "anim.spritesheet.json")
	sheetPath := filepath.Join(dir, "anim.png")

	state := PipelineState{"frame_dir": frameDir, "fps": "8"}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(videoPath, nil, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	// Both files must exist after a successful run.
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("metadata sidecar not created: %v", err)
	}
	if _, err := os.Stat(sheetPath); err != nil {
		t.Fatalf("sprite sheet PNG not created: %v", err)
	}

	// Verify the metadata timestamps to confirm JSON was written first.
	metaInfo, _ := os.Stat(metaPath)
	sheetInfo, _ := os.Stat(sheetPath)
	if sheetInfo.ModTime().Before(metaInfo.ModTime()) {
		t.Error("sprite sheet PNG was modified before metadata JSON — write order is wrong")
	}
}

func TestStitchSpritesheet_FrameDirCleanedOnFailure(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	writePNGToPath(filepath.Join(frameDir, "frame_0001.png"), 32, 32)
	writePNGToPath(filepath.Join(frameDir, "frame_0002.png"), 64, 64)

	state := PipelineState{"frame_dir": frameDir}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(filepath.Join(dir, "test.mp4"), nil, state, slog.Default())
	if r.Status != PostProcessFailed {
		t.Fatalf("Status = %q, want %q", r.Status, PostProcessFailed)
	}

	if _, err := os.Stat(frameDir); !os.IsNotExist(err) {
		t.Error("frame dir should be cleaned up even when stitch fails")
	}
}

func TestStitchSpritesheet_OrphanedJSONCleanup(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)
	writePNGToPath(filepath.Join(frameDir, "frame_0001.png"), 16, 16)

	videoPath := filepath.Join(dir, "anim.mp4")
	_ = os.WriteFile(videoPath, []byte("fake"), 0644)

	// Make the output directory read-only so the PNG write fails after
	// the metadata JSON has already been written.
	outputDir := filepath.Join(dir, "output")
	_ = os.Mkdir(outputDir, 0755)
	fakeVideo := filepath.Join(outputDir, "anim.mp4")
	_ = os.WriteFile(fakeVideo, []byte("fake"), 0644)

	metaPath := filepath.Join(outputDir, "anim.spritesheet.json")
	sheetPath := filepath.Join(outputDir, "anim.png")

	// Pre-create the sheet path as a directory to make encodePNG fail
	// (can't create a file where a directory exists).
	_ = os.Mkdir(sheetPath, 0755)

	state := PipelineState{"frame_dir": frameDir, "fps": "8"}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(fakeVideo, nil, state, slog.Default())
	if r.Status != PostProcessFailed {
		t.Fatalf("Status = %q, want %q", r.Status, PostProcessFailed)
	}

	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Error("orphaned metadata JSON should be removed when PNG write fails")
	}
}

func TestStitchSpritesheet_BestSpeedCompression(t *testing.T) {
	dir := t.TempDir()
	frameDir := filepath.Join(dir, "frames")
	_ = os.Mkdir(frameDir, 0755)

	for i := 1; i <= 8; i++ {
		writePNGToPath(filepath.Join(frameDir, fmt.Sprintf("frame_%04d.png", i)), 64, 64)
	}

	videoPath := filepath.Join(dir, "test-anim.mp4")
	_ = os.WriteFile(videoPath, []byte("fake"), 0644)

	state := PipelineState{"frame_dir": frameDir, "fps": "12"}
	proc := &StitchSpritesheetProcessor{}
	r := proc.Run(videoPath, map[string]string{"columns": "4"}, state, slog.Default())
	if r.Status != PostProcessApplied {
		t.Fatalf("Status = %q; Message: %s", r.Status, r.Message)
	}

	sheetPath := filepath.Join(dir, "test-anim.png")
	img, err := decodePNG(sheetPath)
	if err != nil {
		t.Fatalf("decoding sprite sheet: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 256 || b.Dy() != 128 {
		t.Errorf("sheet dimensions = %dx%d, want 256x128", b.Dx(), b.Dy())
	}

	metaPath := filepath.Join(dir, "test-anim.spritesheet.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta SpritesheetMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("invalid metadata JSON: %v", err)
	}
	if meta.FrameCount != 8 || meta.Columns != 4 || meta.Rows != 2 {
		t.Errorf("meta = %+v, want 8 frames / 4 cols / 2 rows", meta)
	}
	if meta.FPS != 12 {
		t.Errorf("meta.FPS = %d, want 12", meta.FPS)
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
