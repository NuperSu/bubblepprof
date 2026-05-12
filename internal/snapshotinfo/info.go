package snapshotinfo

import (
	"fmt"
	"io"
	"os"

	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/snapshot"
	"bubblepprof/internal/snapshotparse"
)

func Run(out, errOut io.Writer, program string, args []string) int {
	if len(args) < 1 {
		usage(errOut, program)
		return 2
	}
	switch args[0] {
	case "info":
		if len(args) != 2 {
			usage(errOut, program)
			return 2
		}
		if err := Print(out, args[1]); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	case "parse":
		if len(args) != 2 {
			usage(errOut, program)
			return 2
		}
		if err := PrintParse(out, args[1]); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	default:
		usage(errOut, program)
		return 2
	}
}

func usage(w io.Writer, program string) {
	fmt.Fprintf(w, "usage:\n")
	fmt.Fprintf(w, "  %s snapshot info snapshot.tar\n", program)
	fmt.Fprintf(w, "  %s snapshot parse snapshot.tar\n", program)
}

func Print(out io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	info, err := snapshot.InspectSnapshotBundle(f)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}

	fmt.Fprintf(out, "format: %s\n", info.Metadata.Format)
	fmt.Fprintf(out, "created: %s\n", info.Metadata.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(out, "go version: %s\n", info.Metadata.GoVersion)
	fmt.Fprintf(out, "pid: %d\n", info.Metadata.PID)
	fmt.Fprintf(out, "gc before heap dump: %t\n", info.Metadata.GCBeforeHeapDump)
	fmt.Fprintf(out, "%s: present, %d bytes\n", snapshot.HeapDumpFile, info.HeapDumpSize)
	fmt.Fprintf(out, "%s: present, %d bytes\n", snapshot.GoroutineProfileFile, info.GoroutineProfileSize)
	fmt.Fprintf(out, "%s: valid\n", snapshot.MetadataFile)

	return nil
}

// PrintParse reads a snapshot tar, parses heap.dump into a HeapSnapshot,
// and writes a summary of metadata + parsed heap dump statistics.
func PrintParse(out io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	res, err := snapshotparse.ParseSnapshot(f, heapdump.Options{})
	if err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}

	fmt.Fprintf(out, "snapshot format: %s\n", res.Metadata.Format)
	fmt.Fprintf(out, "created: %s\n", res.Metadata.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(out, "metadata go version: %s\n", res.Metadata.GoVersion)
	fmt.Fprintf(out, "pid: %d\n", res.Metadata.PID)
	fmt.Fprintf(out, "%s size: %d bytes\n", snapshot.HeapDumpFile, res.HeapDumpSize)
	fmt.Fprintf(out, "%s size: %d bytes\n", snapshot.GoroutineProfileFile, res.GoroutineProfileSize)
	fmt.Fprintln(out)
	res.Snapshot.PrintSummary(out)
	return nil
}
