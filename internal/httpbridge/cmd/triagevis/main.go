// triagevis produces a triage overlay PNG visualizing what
// PlateSpeckCleanupProcessor would kill vs preserve at given (score_threshold,
// size_threshold). The overlay uses the EXACT same scoring formula and
// 8-connectivity component logic as the production op so the visualization
// is faithful.
//
// Color encoding (rendered on top of a dimmed grayscale of the original):
//   RED tint     = pixels in components <= size_threshold (would be KILLED)
//   GREEN tint   = pixels in components >  size_threshold (would be PRESERVED)
//   White boxes  = bounding boxes around the N largest preserved components,
//                  labeled "#i(N px)" so you can tell what's being kept.
//
// Output: <input-stem>.triage-s<NNN>-t<NNNN>.png next to the input PNG.
//
// Usage:
//   triagevis <png> <score_threshold> <size_threshold>
//
//   triagevis foo.png 0.10 1000
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: triagevis <png> <score_threshold> <size_threshold>")
		os.Exit(2)
	}
	src := os.Args[1]
	score, err := strconv.ParseFloat(os.Args[2], 64)
	if err != nil || score < 0 || score > 1 {
		fmt.Fprintln(os.Stderr, "score_threshold must be in [0,1]")
		os.Exit(2)
	}
	size, err := strconv.Atoi(os.Args[3])
	if err != nil || size < 1 {
		fmt.Fprintln(os.Stderr, "size_threshold must be >= 1")
		os.Exit(2)
	}

	f, err := os.Open(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", src, err)
		os.Exit(1)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode %s: %v\n", src, err)
		os.Exit(1)
	}

	bounds := img.Bounds()
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		nrgba = image.NewNRGBA(bounds)
		draw.Draw(nrgba, bounds, img, bounds.Min, draw.Src)
	}

	W, H := bounds.Dx(), bounds.Dy()
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
			s := float64(minRB-g) / 255.0
			if s > score {
				flagged[flagRow+x] = true
				flaggedCount++
			}
		}
	}

	if flaggedCount == 0 {
		fmt.Println("no flagged pixels at this score threshold; nothing to triage")
		return
	}

	labels, components := labelComponents(flagged, W, H)

	canvas := image.NewNRGBA(bounds)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			off := y*stride + x*4
			r, g, b, a := pix[off], pix[off+1], pix[off+2], pix[off+3]
			gray := uint8((float64(r) + float64(g) + float64(b)) / 3.0 * 0.5)
			canvas.SetNRGBA(x, y, color.NRGBA{gray, gray, gray, a})
		}
	}

	killedComponents, preservedComponents := 0, 0
	killedPx, preservedPx := 0, 0
	for _, c := range components {
		if c.size <= size {
			killedComponents++
			killedPx += c.size
		} else {
			preservedComponents++
			preservedPx += c.size
		}
	}

	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			lab := labels[y*W+x]
			if lab == 0 {
				continue
			}
			c := components[lab-1]
			var tint color.NRGBA
			if c.size <= size {
				tint = color.NRGBA{255, 60, 60, 255}
			} else {
				tint = color.NRGBA{60, 255, 80, 255}
			}
			canvas.SetNRGBA(x, y, tint)
		}
	}

	preservedSorted := make([]*comp, 0, preservedComponents)
	for _, c := range components {
		if c.size > size {
			preservedSorted = append(preservedSorted, c)
		}
	}
	sort.Slice(preservedSorted, func(i, j int) bool {
		return preservedSorted[i].size > preservedSorted[j].size
	})

	white := color.NRGBA{255, 255, 255, 255}
	for i, c := range preservedSorted {
		drawRect(canvas, c.minX, c.minY, c.maxX, c.maxY, white)
		label := fmt.Sprintf("#%d(%d)", i+1, c.size)
		drawLabel(canvas, c.minX, c.minY-14, label, white)
	}

	dir := filepath.Dir(src)
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	scorePart := int(score*100 + 0.5)
	outName := fmt.Sprintf("%s.triage-s%03d-t%04d.png", stem, scorePart, size)
	outPath := filepath.Join(dir, outName)

	out, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", outPath, err)
		os.Exit(1)
	}
	defer out.Close()
	if err := png.Encode(out, canvas); err != nil {
		fmt.Fprintf(os.Stderr, "encode %s: %v\n", outPath, err)
		os.Exit(1)
	}

	fmt.Printf("triage(score=%.2f, size=%d):\n", score, size)
	fmt.Printf("  flagged pixels:        %d\n", flaggedCount)
	fmt.Printf("  total components:      %d\n", len(components))
	fmt.Printf("  KILLED (red):          %d components, %d px\n", killedComponents, killedPx)
	fmt.Printf("  PRESERVED (green):     %d components, %d px\n", preservedComponents, preservedPx)
	if preservedComponents > 0 {
		fmt.Printf("  preserved sizes:       ")
		shown := preservedSorted
		if len(shown) > 20 {
			shown = shown[:20]
		}
		for i, c := range shown {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%d", c.size)
		}
		if len(preservedSorted) > 20 {
			fmt.Printf(" ...(+%d more)", len(preservedSorted)-20)
		}
		fmt.Println()
	}
	fmt.Printf("  output:                %s\n", outName)
}

type comp struct {
	size                       int
	minX, minY, maxX, maxY     int
}

// labelComponents performs an iterative 8-connectivity BFS to label every
// flagged pixel with a 1-based component id and returns the label array
// plus a slice of component descriptors (indexed by label-1).
func labelComponents(flagged []bool, W, H int) ([]int, []*comp) {
	labels := make([]int, W*H)
	var components []*comp
	queue := make([]int, 0, 1024)

	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			i := y*W + x
			if !flagged[i] || labels[i] != 0 {
				continue
			}
			id := len(components) + 1
			c := &comp{size: 0, minX: x, minY: y, maxX: x, maxY: y}
			components = append(components, c)
			queue = queue[:0]
			queue = append(queue, i)
			labels[i] = id
			for head := 0; head < len(queue); head++ {
				p := queue[head]
				py, px := p/W, p%W
				c.size++
				if px < c.minX {
					c.minX = px
				}
				if px > c.maxX {
					c.maxX = px
				}
				if py < c.minY {
					c.minY = py
				}
				if py > c.maxY {
					c.maxY = py
				}
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						nx, ny := px+dx, py+dy
						if nx < 0 || nx >= W || ny < 0 || ny >= H {
							continue
						}
						ni := ny*W + nx
						if !flagged[ni] || labels[ni] != 0 {
							continue
						}
						labels[ni] = id
						queue = append(queue, ni)
					}
				}
			}
		}
	}
	return labels, components
}

func drawRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for x := x0; x <= x1; x++ {
		setIfIn(img, x, y0, c)
		setIfIn(img, x, y1, c)
	}
	for y := y0; y <= y1; y++ {
		setIfIn(img, x0, y, c)
		setIfIn(img, x1, y, c)
	}
}

func setIfIn(img *image.NRGBA, x, y int, c color.NRGBA) {
	b := img.Bounds()
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	img.SetNRGBA(x, y, c)
}

func drawLabel(img *image.NRGBA, x, y int, label string, c color.NRGBA) {
	face := basicfont.Face7x13
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y+13),
	}
	d.DrawString(label)
}
