// singleframetest mattes a single chosen video frame with BiRefNet (still
// mode, no SAM 2 propagation) and emits multiple cleanup variants so a
// human can visually decide which configuration of plate_speck_cleanup
// removes the magenta artifacts inside the silhouette WITHOUT downscale
// + stitch in the way.
//
// Why this tool exists: walttest takes ~10 minutes to produce final
// spritesheets, and after downscale + stitch it's hard to tell whether
// surviving magenta is something cleanup CAN remove at the per-frame
// stage or something only post-stitch cleanup can address. By isolating
// a single frame at full resolution we cut iteration to ~30 seconds and
// the visual signal is unambiguous.
//
// Usage:
//
//	go run ./internal/httpbridge/cmd/singleframetest \
//	    --mp4 path/to/source.mp4 \
//	    --out path/to/output/dir \
//	    [--frame 150]
//
// Outputs (all named "<video-stem>-frame<NNN>.<tag>.png"):
//
//	*.raw-veo.png       -- the chosen frame as Veo rendered it (no matte)
//	*.raw-matte.png     -- after BiRefNet still matte, before binarize/cleanup
//	*.binarize-only.png -- raw-matte + binarize_alpha (sharpens edges)
//	*.test-current-...  -- binarize + plate_speck_cleanup at the current
//	                       Miyazaki defaults and several alternative
//	                       (score_threshold, size_threshold, edge_aware)
//	                       combinations.
//
// Open them side-by-side in an image viewer and pick the configuration
// that best removes the under-arm / between-legs magenta while keeping
// legitimate magenta-family subject features (potion bottles, painted
// highlights) intact.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rauriemo/anthem/internal/httpbridge"
)

func main() {
	mp4 := flag.String("mp4", "", "path to source MP4 (only used to derive output stem when --reuse-matte is set; ignored when --still is set)")
	frameIdx := flag.Int("frame", 150, "frame index to test (1-based, matches ExtractVideoFramesProcessor's frame_NNNN.png naming)")
	outDir := flag.String("out", "", "output directory for variant PNGs")
	frameDirFlag := flag.String("frame-dir", "", "reuse already-extracted frames from this directory (skips extract step)")
	reuseMatte := flag.String("reuse-matte", "", "reuse a pre-matted PNG (skips extract AND matte; fastest iteration); takes the path to a previously-saved raw-matte.png")
	stillPath := flag.String("still", "", "test cleanup directly on a pre-cleaned still PNG (skips extract, matte, and stem derivation from --mp4); used to validate Miyazaki-style still-image cleanup tunings against existing characters without re-generating them")
	stillCompare := flag.Bool("still-compare", false, "in --still mode, run only two variants: current Miyazaki default (0.25/150) and proposed tuned (0.05/600) for direct A/B comparison")
	sweep := flag.Bool("sweep", false, "run a focused size sweep at score=0.10 (intermediate sizes between 150 and 1000) instead of the default broad variants")
	scoreSweep := flag.Bool("score-sweep", false, "run a focused score sweep at fixed --sweep-size (default 450); shows how widening the chroma keyer changes which subject features get caught")
	sweepSize := flag.String("sweep-size", "450", "size threshold to hold fixed during --score-sweep")
	sweepScore := flag.String("sweep-score", "0.05", "score threshold to hold fixed during --sweep (size sweep)")
	flag.Parse()

	if *outDir == "" || (*mp4 == "" && *stillPath == "") {
		fmt.Fprintln(os.Stderr, "usage: singleframetest --out <dir> { --mp4 <path> [--frame N] [--frame-dir <existing>] [--reuse-matte <png>] [--sweep] [--score-sweep] | --still <png> [--still-compare] }")
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	state := httpbridge.PipelineState{}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir out: %v\n", err)
		os.Exit(1)
	}

	var stem string
	if *stillPath != "" {
		stem = strings.TrimSuffix(filepath.Base(*stillPath), filepath.Ext(*stillPath))
	} else {
		stem = strings.TrimSuffix(filepath.Base(*mp4), filepath.Ext(*mp4))
		stem = fmt.Sprintf("%s-frame%03d", stem, *frameIdx)
	}

	rawMatte := filepath.Join(*outDir, stem+".raw-matte.png")

	if *stillPath != "" {
		fmt.Printf("==== using pre-cleaned still at: %s ====\n", *stillPath)
		if _, err := os.Stat(*stillPath); err != nil {
			fmt.Fprintf(os.Stderr, "still not readable: %v\n", err)
			os.Exit(1)
		}
		if *stillPath != rawMatte {
			mustCopy(*stillPath, rawMatte)
		}
	} else if *reuseMatte != "" {
		fmt.Printf("==== reusing pre-matted PNG at: %s ====\n", *reuseMatte)
		if _, err := os.Stat(*reuseMatte); err != nil {
			fmt.Fprintf(os.Stderr, "matte not readable: %v\n", err)
			os.Exit(1)
		}
		if *reuseMatte != rawMatte {
			mustCopy(*reuseMatte, rawMatte)
		}
	} else {
		var frameDir string
		if *frameDirFlag != "" {
			frameDir = *frameDirFlag
			fmt.Printf("==== reusing extracted frames at: %s ====\n", frameDir)
		} else {
			fmt.Println("==== extracting frames ====")
			if r := (&httpbridge.ExtractVideoFramesProcessor{}).Run(*mp4, map[string]string{"fps": "24"}, state, log); r.Status != httpbridge.PostProcessApplied {
				fmt.Fprintf(os.Stderr, "extract failed: %s\n", r.Message)
				os.Exit(1)
			}
			frameDir = state["frame_dir"]
			fmt.Printf("  frames at: %s\n", frameDir)
			fmt.Printf("  (preserved for re-runs; pass --frame-dir %s to skip extract next time)\n", frameDir)
		}
		srcFrame := filepath.Join(frameDir, fmt.Sprintf("frame_%04d.png", *frameIdx))
		if _, err := os.Stat(srcFrame); err != nil {
			fmt.Fprintf(os.Stderr, "frame %d not found at %s: %v\n", *frameIdx, srcFrame, err)
			os.Exit(1)
		}

		rawVeo := filepath.Join(*outDir, stem+".raw-veo.png")
		mustCopy(srcFrame, rawVeo)
		fmt.Printf("  raw veo frame -> %s\n", rawVeo)

		mustCopy(srcFrame, rawMatte)
		fmt.Println("==== matting frame with BiRefNet still ====")
		if r := (&httpbridge.BiRefNetMatteProcessor{}).Run(rawMatte, map[string]string{"refine": "true"}, state, log); r.Status != httpbridge.PostProcessApplied {
			fmt.Fprintf(os.Stderr, "matte failed: %s\n", r.Message)
			os.Exit(1)
		}
		fmt.Printf("  raw matte -> %s (open this to see what BiRefNet kept as foreground)\n", rawMatte)
	}

	type variant struct {
		tag      string
		binarize bool
		score    string
		size     string
		edge     bool
	}
	var variants []variant
	if *scoreSweep {
		// Hold size fixed (typically just below the smallest legitimate
		// subject feature size found by --sweep) and bisect score downward
		// to find the loosest keyer that still doesn't over-flag subject
		// features. Lower scores catch softer pinks (Veo's desaturated
		// plate, Lanczos halos) but risk grabbing painted purple subject
		// highlights.
		variants = []variant{
			{tag: "scoresweep-0.00-" + *sweepSize, binarize: true, score: "0.00", size: *sweepSize},
			{tag: "scoresweep-0.01-" + *sweepSize, binarize: true, score: "0.01", size: *sweepSize},
			{tag: "scoresweep-0.02-" + *sweepSize, binarize: true, score: "0.02", size: *sweepSize},
			{tag: "scoresweep-0.03-" + *sweepSize, binarize: true, score: "0.03", size: *sweepSize},
			{tag: "scoresweep-0.05-" + *sweepSize, binarize: true, score: "0.05", size: *sweepSize},
		}
	} else 	if *sweep {
		// Focused sweep: hold score fixed at --sweep-score (default 0.05,
		// which is where we landed for trinket-salesman), bisect size to
		// find the largest threshold that still preserves legitimate
		// subject features. Use to check whether the chosen T is a) doing
		// anything (vs T=0/lower) and b) safely below the smallest
		// legit subject feature.
		variants = []variant{
			{tag: "sweep-" + *sweepScore + "-50", binarize: true, score: *sweepScore, size: "50"},
			{tag: "sweep-" + *sweepScore + "-150", binarize: true, score: *sweepScore, size: "150"},
			{tag: "sweep-" + *sweepScore + "-300", binarize: true, score: *sweepScore, size: "300"},
			{tag: "sweep-" + *sweepScore + "-450", binarize: true, score: *sweepScore, size: "450"},
			{tag: "sweep-" + *sweepScore + "-600", binarize: true, score: *sweepScore, size: "600"},
			{tag: "sweep-" + *sweepScore + "-750", binarize: true, score: *sweepScore, size: "750"},
			{tag: "sweep-" + *sweepScore + "-900", binarize: true, score: *sweepScore, size: "900"},
			{tag: "sweep-" + *sweepScore + "-1500", binarize: true, score: *sweepScore, size: "1500"},
		}
	} else if *stillCompare {
		// --still-compare: minimal A/B for validating that the proposed
		// tuned (0.05/600) settings derived from the trinket-salesman
		// animation are safe to apply to Miyazaki's still-image pipeline
		// on already-cleaned characters. Both variants run a redundant
		// binarize first (no-op on already-binarized stills) so the
		// comparison stays apples-to-apples.
		variants = []variant{
			{tag: "miyazaki-current-0.25-150", binarize: true, score: "0.25", size: "150"},
			{tag: "miyazaki-tuned-0.05-600", binarize: true, score: "0.05", size: "600"},
		}
	} else {
		variants = []variant{
			{tag: "binarize-only", binarize: true},
			{tag: "test-current-0.25-150", binarize: true, score: "0.25", size: "150"},
			{tag: "test-wider-0.10-150", binarize: true, score: "0.10", size: "150"},
			{tag: "test-bigger-0.25-1000", binarize: true, score: "0.25", size: "1000"},
			{tag: "test-bigger-0.25-5000", binarize: true, score: "0.25", size: "5000"},
			{tag: "test-wider-bigger-0.10-1000", binarize: true, score: "0.10", size: "1000"},
			{tag: "test-edge-aware-0.25-150", binarize: true, score: "0.25", size: "150", edge: true},
		}
	}

	fmt.Println("==== running cleanup variants ====")
	for _, v := range variants {
		out := filepath.Join(*outDir, stem+"."+v.tag+".png")
		mustCopy(rawMatte, out)

		if v.binarize {
			r := (&httpbridge.BinarizeAlphaProcessor{}).Run(out, map[string]string{"alpha_threshold": "4096", "alpha_snap": "32768"}, state, log)
			if r.Status != httpbridge.PostProcessApplied {
				fmt.Fprintf(os.Stderr, "  binarize %s FAILED: %s\n", v.tag, r.Message)
				continue
			}
		}
		if v.score != "" {
			cfg := map[string]string{
				"score_threshold": v.score,
				"size_threshold":  v.size,
				"connectivity":    "8",
			}
			if v.edge {
				cfg["edge_aware"] = "true"
			}
			r := (&httpbridge.PlateSpeckCleanupProcessor{}).Run(out, cfg, state, log)
			if r.Status != httpbridge.PostProcessApplied {
				fmt.Fprintf(os.Stderr, "  cleanup %s FAILED: %s\n", v.tag, r.Message)
				continue
			}
			fmt.Printf("  %s -> %s\n      %s\n", v.tag, out, r.Message)
		} else {
			fmt.Printf("  %s -> %s (binarize only, no plate_speck_cleanup)\n", v.tag, out)
		}
	}

	fmt.Println("\n==== DONE ====")
	fmt.Printf("Open %s and compare side-by-side.\n", *outDir)
}

func mustCopy(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		panic(fmt.Errorf("open src: %w", err))
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		panic(fmt.Errorf("create dst: %w", err))
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		panic(fmt.Errorf("copy: %w", err))
	}
}
