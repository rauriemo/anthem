// specksweep characterizes the magenta-family pixel population in a PNG
// across multiple score thresholds, then runs PlateSpeckCleanupProcessor at
// several (score, size) combinations to produce comparison outputs.
//
// Output filenames: <stem>.sweep-s<NNN>-t<NNNN>.png where NNN is the
// score_threshold * 100 (e.g. s010 = 0.10) and NNNN is the size_threshold.
//
// Usage:
//
//	specksweep <png-path>
package main

import (
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rauriemo/anthem/internal/httpbridge"
)

type cleanupRun struct {
	scoreThreshold float64
	sizeThreshold  int
}

var sweeps = []cleanupRun{
	{0.25, 150}, // current production
	{0.20, 300},
	{0.15, 500},
	{0.12, 1000},
	{0.10, 1000},
	{0.08, 2000},
}

var probeThresholds = []float64{0.05, 0.08, 0.10, 0.12, 0.15, 0.20, 0.25, 0.30}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: specksweep <png>")
		os.Exit(2)
	}
	srcPath := os.Args[1]
	dir := filepath.Dir(srcPath)
	stem := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	fmt.Println("==== SCORE-THRESHOLD HISTOGRAM (opaque pixels only) ====")
	fmt.Printf("source: %s\n\n", srcPath)
	hist := opaqueScoreHistogram(srcPath, probeThresholds)
	fmt.Printf("%-18s %-12s\n", "score_threshold", "flagged_px")
	for i, t := range probeThresholds {
		fmt.Printf("%-18.2f %-12d\n", t, hist[i])
	}
	fmt.Println()

	fmt.Println("==== CLEANUP SWEEPS ====")
	fmt.Printf("%-12s %-10s %-12s %-12s %-12s %-12s %s\n",
		"score", "size", "flagged_px", "killed_px", "preserved_px", "remain_px", "output_file")
	for _, run := range sweeps {
		outName := fmt.Sprintf("%s.sweep-s%03d-t%04d.png", stem, int(run.scoreThreshold*100+0.5), run.sizeThreshold)
		outPath := filepath.Join(dir, outName)
		if err := copyFile(srcPath, outPath); err != nil {
			fmt.Fprintf(os.Stderr, "copy failed: %v\n", err)
			os.Exit(1)
		}
		r := (&httpbridge.PlateSpeckCleanupProcessor{}).Run(
			outPath,
			map[string]string{
				"score_threshold": fmt.Sprintf("%.4f", run.scoreThreshold),
				"size_threshold":  fmt.Sprintf("%d", run.sizeThreshold),
				"connectivity":    "8",
			},
			httpbridge.PipelineState{}, log,
		)
		if r.Status != httpbridge.PostProcessApplied {
			fmt.Fprintf(os.Stderr, "  cleanup failed: %s\n", r.Message)
			continue
		}
		flagged, killed, preserved := parseSpeckTotals(r.Message)
		remain := opaqueScoreHistogram(outPath, []float64{run.scoreThreshold})[0]
		fmt.Printf("%-12.2f %-10d %-12d %-12d %-12d %-12d %s\n",
			run.scoreThreshold, run.sizeThreshold, flagged, killed, preserved, remain, outName)
	}
}

func opaqueScoreHistogram(path string, thresholds []float64) []int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", path, err)
		return make([]int, len(thresholds))
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode %s: %v\n", path, err)
		return make([]int, len(thresholds))
	}
	counts := make([]int, len(thresholds))
	b := img.Bounds()
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
			for i, t := range thresholds {
				if score >= t {
					counts[i]++
				}
			}
		}
	}
	return counts
}

// parseSpeckTotals returns (flagged_px, killed_px, preserved_px) from
// PlateSpeckCleanupProcessor's status message:
//
//	"flagged=X in Y components; killed Kc components (Kp px); preserved Pc components (Pp px) ..."
func parseSpeckTotals(msg string) (flagged, killed, preserved int) {
	if i := strings.Index(msg, "flagged="); i >= 0 {
		_, _ = fmt.Sscanf(msg[i+len("flagged="):], "%d", &flagged)
	}
	if i := strings.Index(msg, "killed "); i >= 0 {
		var kc int
		_, _ = fmt.Sscanf(msg[i+len("killed "):], "%d components (%d px)", &kc, &killed)
	}
	if i := strings.Index(msg, "preserved "); i >= 0 {
		var pc int
		_, _ = fmt.Sscanf(msg[i+len("preserved "):], "%d components (%d px)", &pc, &preserved)
	}
	return
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
