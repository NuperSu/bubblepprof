package main

// This file contains formatting helpers from an earlier output style that
// printed per-frame details during traversal (see output.txt). The current
// output is produced by printAnalysisReport in workflow.go. These functions
// are unused but kept for potential future per-frame reporting.

import (
	"fmt"
	"io"

	"github.com/go-delve/delve/pkg/proc"
)

func printGoroutineHeader(w io.Writer, g *proc.G) {
	if g.Unreadable != nil {
		fmt.Fprintf(w, "\n== goroutine %d (unreadable: %v)\n", g.ID, g.Unreadable)
		return
	}
	fmt.Fprintf(w, "\n== goroutine %d status=%d waitReason=%d\n", g.ID, g.Status, g.WaitReason)
}

func printFrameHeader(w io.Writer, idx int, fr proc.Stackframe) {
	desc := formatFrame(fr)
	if fr.Err != nil {
		fmt.Fprintf(w, "  -- frame %d: %s (frame error: %v)\n", idx, desc, fr.Err)
		return
	}
	fmt.Fprintf(w, "  -- frame %d: %s\n", idx, desc)
}

func formatFrame(fr proc.Stackframe) string {
	loc := fr.Call
	if loc.Fn == nil && loc.File == "" && loc.Line == 0 {
		loc = fr.Current
	}

	fn := "<unknown-func>"
	if loc.Fn != nil && loc.Fn.Name != "" {
		fn = loc.Fn.Name
	}

	if loc.File == "" {
		if loc.PC != 0 {
			return fmt.Sprintf("%s (pc=0x%x)", fn, loc.PC)
		}
		return fn
	}

	if loc.Line > 0 {
		return fmt.Sprintf("%s (%s:%d)", fn, loc.File, loc.Line)
	}
	return fmt.Sprintf("%s (%s)", fn, loc.File)
}
