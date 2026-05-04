package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/rauriemo/anthem/internal/httpbridge"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: specktest <png>")
		os.Exit(2)
	}
	proc := &httpbridge.PlateSpeckCleanupProcessor{}
	r := proc.Run(os.Args[1], nil, httpbridge.PipelineState{}, slog.Default())
	fmt.Printf("status=%s op=%s\nmessage=%s\n", r.Status, r.Op, r.Message)
}
