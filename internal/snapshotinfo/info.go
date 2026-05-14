package snapshotinfo

import (
	"fmt"
	"io"
	"os"

	"bubblepprof/internal/snapshot"
)

func Run(out, errOut io.Writer, program string, args []string) int {
	if len(args) != 2 || args[0] != "info" {
		fmt.Fprintf(errOut, "usage: %s snapshot info snapshot.tar\n", program)
		return 2
	}

	if err := Print(out, args[1]); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
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
