// walttest runs Walt's animation post-process chain against a single MP4
// and emits multiple spritesheet variants for A/B comparison, sharing
// matte stages across branches that use the same matter.
//
// Branches emitted today:
//
//   - <stem>.test-baseline.png         (Walt's current chain: video_matte
//     SAM 2 path -> normalize_frames -> stitch_spritesheet, no cleanup)
//   - <stem>.test-miyazaki-chain.png   (SAM 2 matte + per-frame
//     binarize_alpha + per-frame plate_speck_cleanup at 0.25/150 +
//     normalize + stitch -- pre-fix RGB-bleed reference; preserved so
//     the deltas vs `fixed-with-sam2` are visible after the RGB-zero
//     fix lands in PlateSpeckCleanupProcessor)
//   - <stem>.test-strategy2.png        (cleanup applied AFTER stitch
//     against the baseline output, single pass over the whole grid)
//   - <stem>.test-fixed-with-sam2.png  (NEW: SAM 2 matte + binarize_alpha
//     + plate_speck_cleanup at 0.25/150, all per-frame at full
//     resolution, then normalize + stitch -- this is the chain that
//     should pass once PlateSpeckCleanupProcessor zeros RGB on killed
//     pixels)
//   - <stem>.test-fixed-no-sam2.png    (NEW: same chain but consuming a
//     no-SAM-2 matte run instead -- per-frame BiRefNet only via the
//     sidecar's --no-sam2 fallback path. Tests whether SAM 2's temporal
//     coherence is still required once dust is solved upstream)
//
// The matte stages are slow (~3min each on a 192-frame sequence with SAM 2
// on CUDA, ~22min for per-frame BiRefNet without SAM 2). All downstream
// branches run in <30s each.
//
// Usage:
//   walttest <mp4-path>
//
// The driver expects ffmpeg on PATH and the matte sidecar configured per
// CLAUDE.md (MATTE_PYTHON / MATTE_SCRIPT env vars or default discovery).
package main

import (
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rauriemo/anthem/internal/httpbridge"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: walttest <mp4-path>")
		os.Exit(2)
	}
	mp4Path := os.Args[1]
	if _, err := os.Stat(mp4Path); err != nil {
		fmt.Fprintf(os.Stderr, "mp4 not found: %v\n", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dir := filepath.Dir(mp4Path)
	stem := strings.TrimSuffix(filepath.Base(mp4Path), filepath.Ext(mp4Path))

	fmt.Println("==== PHASE 1a: shared extract + matte WITH SAM 2 (slow, ~3min) ====")
	t0 := time.Now()
	mattedSAM2 := mustMatte(mp4Path, false, log)
	defer os.RemoveAll(mattedSAM2)
	fmt.Printf("SAM 2 matted frames preserved at: %s (elapsed %s)\n\n", mattedSAM2, time.Since(t0).Round(time.Second))

	fmt.Println("==== PHASE 1b: shared extract + matte WITHOUT SAM 2 (slower, ~22min on CPU/CUDA) ====")
	t1 := time.Now()
	mattedNoSAM2 := mustMatte(mp4Path, true, log)
	defer os.RemoveAll(mattedNoSAM2)
	fmt.Printf("no-SAM-2 matted frames preserved at: %s (elapsed %s)\n\n", mattedNoSAM2, time.Since(t1).Round(time.Second))

	branches := []branch{
		{
			tag:               "baseline",
			mattedSrc:         mattedSAM2,
			withBinarize:      false,
			withCleanup:       false,
			perFrameScore:     "0.25",
			perFrameSize:      "150",
			postStitchCleanup: false,
		},
		{
			tag:               "miyazaki-chain",
			mattedSrc:         mattedSAM2,
			withBinarize:      true,
			withCleanup:       true,
			perFrameScore:     "0.25",
			perFrameSize:      "150",
			postStitchCleanup: false,
		},
		{
			tag:               "fixed-with-sam2",
			mattedSrc:         mattedSAM2,
			withBinarize:      true,
			withCleanup:       true,
			perFrameScore:     "0.25",
			perFrameSize:      "150",
			postStitchCleanup: false,
		},
		{
			tag:               "fixed-no-sam2",
			mattedSrc:         mattedNoSAM2,
			withBinarize:      true,
			withCleanup:       true,
			perFrameScore:     "0.25",
			perFrameSize:      "150",
			postStitchCleanup: false,
		},
		{
			// Topology-aware kill: in addition to size <= 150, also kill
			// any component (regardless of size) that's adjacent to an
			// alpha=0 pixel or to the image border. Targets BiRefNet's
			// blob-completion artifacts (unmatted plate patches under
			// arms / between legs) that touch the matted background
			// while preserving fully-interior magenta-family subject
			// features (potion bottles, painted highlights).
			tag:               "edge-aware",
			mattedSrc:         mattedSAM2,
			withBinarize:      true,
			withCleanup:       true,
			perFrameScore:     "0.25",
			perFrameSize:      "150",
			perFrameEdgeAware: true,
			postStitchCleanup: false,
		},
		{
			// Tuned configuration empirically derived from singleframetest
			// on trinket-salesman-idle-loop frame 100:
			//   - score=0.05 catches Veo's desaturated soft-pink plate
			//     (which scores ~0.14, well above 0.05) along with the
			//     halo bleed around it. Lower than 0.05 starts catching
			//     subject anti-aliasing and merging artifacts into
			//     preserved blobs.
			//   - size=600 sits in a clean gap between the largest
			//     unwanted medium artifact (~380 px) and the smallest
			//     legitimate subject feature on this character (the
			//     dangling sash at ~653 px). T=450 and T=600 produce
			//     byte-identical output on frame 100; T=600 takes the
			//     upper end of that gap to maximise headroom against
			//     cross-frame size variation.
			tag:               "tuned-0.05-600",
			mattedSrc:         mattedSAM2,
			withBinarize:      true,
			withCleanup:       true,
			perFrameScore:     "0.05",
			perFrameSize:      "600",
			postStitchCleanup: false,
		},
	}

	for _, b := range branches {
		fmt.Printf("==== PHASE 2: branch %q (matte source: %s) ====\n", b.tag, b.mattedSrc)
		runBranch(b, dir, stem, log)
		fmt.Println()
	}

	fmt.Println("==== PHASE 3: branch \"strategy2\" (cleanup over baseline) ====")
	baselinePath := filepath.Join(dir, stem+".test-baseline.png")
	strategy2Path := filepath.Join(dir, stem+".test-strategy2.png")
	if err := copyFile(baselinePath, strategy2Path); err != nil {
		fmt.Fprintf(os.Stderr, "copy baseline -> strategy2: %v\n", err)
		os.Exit(1)
	}
	beforeS2 := countOpaqueMagenta(strategy2Path)
	r := (&httpbridge.PlateSpeckCleanupProcessor{}).Run(
		strategy2Path,
		map[string]string{"score_threshold": "0.25", "size_threshold": "150", "connectivity": "8"},
		httpbridge.PipelineState{}, log,
	)
	if r.Status != httpbridge.PostProcessApplied {
		fmt.Fprintf(os.Stderr, "strategy2 cleanup: %s -> %s\n", r.Status, r.Message)
		os.Exit(1)
	}
	afterS2 := countOpaqueMagenta(strategy2Path)
	fmt.Printf("strategy2: %s\n", r.Message)
	fmt.Printf("strategy2 opaque-magenta px: %d -> %d (delta %d)\n", beforeS2, afterS2, beforeS2-afterS2)

	fmt.Println()
	fmt.Println("==== SUMMARY ====")
	for _, name := range []string{
		stem + ".test-baseline.png",
		stem + ".test-strategy1.png",
		stem + ".test-strategy2.png",
		stem + ".test-combined.png",
		stem + ".test-perframe-aggressive.png",
		stem + ".test-miyazaki-chain.png",
		stem + ".test-fixed-with-sam2.png",
		stem + ".test-fixed-no-sam2.png",
		stem + ".test-edge-aware.png",
		stem + ".test-tuned-0.05-600.png",
	} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		c := countOpaqueMagenta(p)
		fmt.Printf("  %-55s opaque-magenta=%d\n", name, c)
	}
}

type branch struct {
	tag               string
	mattedSrc         string // dir of pre-matted frames this branch should consume (SAM 2 or no-SAM-2 path)
	withBinarize      bool   // run binarize_alpha per-frame BEFORE plate_speck_cleanup
	withCleanup       bool   // run plate_speck_cleanup per-frame after matte
	perFrameScore     string // score_threshold for per-frame cleanup
	perFrameSize      string // size_threshold for per-frame cleanup
	perFrameEdgeAware bool   // pass edge_aware=true to per-frame cleanup
	postStitchCleanup bool   // also run cleanup after stitch
	postStitchScore   string // score_threshold for post-stitch cleanup
	postStitchSize    string // size_threshold for post-stitch cleanup
}

func runBranch(b branch, dir, stem string, log *slog.Logger) {
	frameDir, err := copyDir(b.mattedSrc, "walttest-"+b.tag+"-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "copy matted frames: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(frameDir)

	if b.withBinarize {
		frames, err := listPNGsSorted(frameDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list binarize frames: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  applying per-frame binarize_alpha (alpha_threshold=4096, alpha_snap=32768) to %d pre-downscale frames...\n", len(frames))
		bin := &httpbridge.BinarizeAlphaProcessor{}
		for _, frame := range frames {
			r := bin.Run(frame,
				map[string]string{"alpha_threshold": "4096", "alpha_snap": "32768"},
				httpbridge.PipelineState{}, log,
			)
			if r.Status != httpbridge.PostProcessApplied {
				fmt.Fprintf(os.Stderr, "  binarize frame %s: %s -> %s\n", filepath.Base(frame), r.Status, r.Message)
				os.Exit(1)
			}
		}
		fmt.Printf("  binarize_alpha applied to %d frames\n", len(frames))
	}

	if b.withCleanup {
		frames, err := listPNGsSorted(frameDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list cleanup frames: %v\n", err)
			os.Exit(1)
		}
		edgeAwareStr := "false"
		if b.perFrameEdgeAware {
			edgeAwareStr = "true"
		}
		fmt.Printf("  applying per-frame plate_speck_cleanup (score=%s, size=%s, edge_aware=%s) to %d pre-downscale frames...\n",
			b.perFrameScore, b.perFrameSize, edgeAwareStr, len(frames))
		killedComponentsTotal, killedPxTotal := 0, 0
		preservedComponentsTotal, preservedPxTotal := 0, 0
		speck := &httpbridge.PlateSpeckCleanupProcessor{}
		for _, frame := range frames {
			r := speck.Run(frame,
				map[string]string{
					"score_threshold": b.perFrameScore,
					"size_threshold":  b.perFrameSize,
					"connectivity":    "8",
					"edge_aware":      edgeAwareStr,
				},
				httpbridge.PipelineState{}, log,
			)
			if r.Status != httpbridge.PostProcessApplied {
				fmt.Fprintf(os.Stderr, "  frame %s: %s -> %s\n", filepath.Base(frame), r.Status, r.Message)
				os.Exit(1)
			}
			kc, kp, pc, pp := parseSpeckTotals(r.Message)
			killedComponentsTotal += kc
			killedPxTotal += kp
			preservedComponentsTotal += pc
			preservedPxTotal += pp
		}
		fmt.Printf("  per-frame cleanup totals: killed %d components (%d px); preserved %d components (%d px)\n",
			killedComponentsTotal, killedPxTotal, preservedComponentsTotal, preservedPxTotal)
	}

	branchMP4 := filepath.Join(dir, stem+".test-"+b.tag+".mp4")
	if err := copyFile(filepath.Join(dir, stem+".mp4"), branchMP4); err != nil {
		fmt.Fprintf(os.Stderr, "copy mp4 for branch: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(branchMP4)

	state := httpbridge.PipelineState{
		"frame_dir":   frameDir,
		"frame_count": fmt.Sprintf("%d", countFiles(frameDir)),
		"fps":         "24",
	}

	must("normalize_frames", (&httpbridge.NormalizeFramesProcessor{}).Run(
		branchMP4,
		map[string]string{"padding": "4", "alpha_threshold": "4096", "alpha_snap": "32768", "scale_factor": "4"},
		state, log,
	))

	must("stitch_spritesheet", (&httpbridge.StitchSpritesheetProcessor{}).Run(
		branchMP4,
		map[string]string{"columns": "8", "keep_video": "true"},
		state, log,
	))

	sheetPath := state["spritesheet_path"]
	beforePostStitch := countOpaqueMagenta(sheetPath)

	if b.postStitchCleanup {
		fmt.Printf("  applying post-stitch plate_speck_cleanup (score=%s, size=%s) to spritesheet...\n",
			b.postStitchScore, b.postStitchSize)
		r := (&httpbridge.PlateSpeckCleanupProcessor{}).Run(
			sheetPath,
			map[string]string{
				"score_threshold": b.postStitchScore,
				"size_threshold":  b.postStitchSize,
				"connectivity":    "8",
			},
			httpbridge.PipelineState{}, log,
		)
		if r.Status != httpbridge.PostProcessApplied {
			fmt.Fprintf(os.Stderr, "  post-stitch cleanup: %s -> %s\n", r.Status, r.Message)
			os.Exit(1)
		}
		fmt.Printf("  post-stitch cleanup: %s\n", r.Message)
	}

	afterPostStitch := countOpaqueMagenta(sheetPath)
	fmt.Printf("  branch %s spritesheet -> %s (opaque-magenta: pre-poststitch=%d, final=%d)\n",
		b.tag, sheetPath, beforePostStitch, afterPostStitch)
}

// mustMatte runs extract_video_frames + video_matte and returns a path to a
// keep-around copy of the matted frames (caller is responsible for cleanup).
// When noSAM2 is true, the sidecar's --no-sam2 flag is passed and matting
// degrades to per-frame BiRefNet (slower, no temporal coherence). The keep
// directory name embeds the matter mode for traceability.
func mustMatte(mp4Path string, noSAM2 bool, log *slog.Logger) string {
	state := httpbridge.PipelineState{}
	must("extract_video_frames", (&httpbridge.ExtractVideoFramesProcessor{}).Run(
		mp4Path, map[string]string{"fps": "24"}, state, log,
	))
	cfg := map[string]string{"keyframe_every": "8", "refine": "true"}
	mode := "sam2"
	if noSAM2 {
		cfg["no_sam2"] = "true"
		mode = "no-sam2"
	}
	must("video_matte["+mode+"]", (&httpbridge.VideoMatteProcessor{}).Run(
		mp4Path, cfg, state, log,
	))
	frameDir := state["frame_dir"]
	keep, err := copyDir(frameDir, "walttest-matted-"+mode+"-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "preserving matted frames: %v\n", err)
		os.Exit(1)
	}
	os.RemoveAll(frameDir) // we don't need the original anymore; branches copy from `keep`
	return keep
}

func must(name string, r httpbridge.PostProcessResult) {
	if r.Status == httpbridge.PostProcessApplied || r.Status == httpbridge.PostProcessSkipped {
		fmt.Printf("  %s: %s -- %s\n", name, r.Status, r.Message)
		return
	}
	fmt.Fprintf(os.Stderr, "  %s FAILED: %s\n", name, r.Message)
	os.Exit(1)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, tmpPattern string) (string, error) {
	dst, err := os.MkdirTemp("", tmpPattern)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return "", err
		}
	}
	return dst, nil
}

func listPNGsSorted(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var pngs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			pngs = append(pngs, filepath.Join(dir, e.Name()))
		}
	}
	return pngs, nil
}

func countFiles(dir string) int {
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// parseSpeckTotals extracts (killedComponents, killedPx, preservedComponents,
// preservedPx) from PlateSpeckCleanupProcessor's status message:
//   "flagged=X in Y components; killed Kc components (Kp px); preserved Pc components (Pp px) ..."
func parseSpeckTotals(msg string) (kc, kp, pc, pp int) {
	if i := strings.Index(msg, "killed "); i >= 0 {
		s := msg[i+len("killed "):]
		fmt.Sscanf(s, "%d components (%d px)", &kc, &kp)
	}
	if i := strings.Index(msg, "preserved "); i >= 0 {
		s := msg[i+len("preserved "):]
		fmt.Sscanf(s, "%d components (%d px)", &pc, &pp)
	}
	return
}

// countOpaqueMagenta counts pixels in the PNG with alpha=255 and a
// chroma_key score >= 0.25 (the same threshold PlateSpeckCleanupProcessor
// uses to flag plate-color survivors).
func countOpaqueMagenta(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return -1
	}
	b := img.Bounds()
	count := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := img.At(x, y).RGBA()
			if a>>8 != 255 {
				continue
			}
			r8, g8, b8 := float64(r>>8), float64(g>>8), float64(bb>>8)
			minRB := r8
			if b8 < minRB {
				minRB = b8
			}
			score := (minRB - g8) / 255.0
			if score >= 0.25 {
				count++
			}
		}
	}
	return count
}
