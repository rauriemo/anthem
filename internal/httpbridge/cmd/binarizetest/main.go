package main

import (
	"fmt"
	"image"
	_ "image/png"
	"log/slog"
	"os"

	"github.com/rauriemo/anthem/internal/httpbridge"
)

// binarizetest applies BinarizeAlphaProcessor with Walt's production defaults
// to a single PNG and prints alpha-channel statistics before and after.
// Used to preview the edge-match-with-Miyazaki upgrade for Walt without
// modifying production code.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: binarizetest <png>")
		os.Exit(2)
	}
	path := os.Args[1]

	beforeSoft, beforeOpaque, beforeTransparent := alphaHist(path)
	fmt.Printf("BEFORE: opaque=%d soft(1-254)=%d transparent=%d\n",
		beforeOpaque, beforeSoft, beforeTransparent)

	proc := &httpbridge.BinarizeAlphaProcessor{}
	r := proc.Run(path, map[string]string{
		"alpha_threshold": "4096",
		"alpha_snap":      "32768",
	}, httpbridge.PipelineState{}, slog.Default())
	fmt.Printf("status=%s\nmessage=%s\n", r.Status, r.Message)

	afterSoft, afterOpaque, afterTransparent := alphaHist(path)
	fmt.Printf("AFTER:  opaque=%d soft(1-254)=%d transparent=%d\n",
		afterOpaque, afterSoft, afterTransparent)

	if beforeSoft > 0 {
		pct := float64(afterSoft) / float64(beforeSoft) * 100.0
		fmt.Printf("soft-edge band: %d -> %d (%.1f%% remaining)\n", beforeSoft, afterSoft, pct)
	}
}

func alphaHist(path string) (soft, opaque, transparent int) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening %s: %v\n", path, err)
		os.Exit(1)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decoding %s: %v\n", path, err)
		os.Exit(1)
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			a8 := uint8(a >> 8)
			switch {
			case a8 == 0:
				transparent++
			case a8 == 255:
				opaque++
			default:
				soft++
			}
		}
	}
	return
}
