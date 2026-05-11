package snapshotinfo

import (
	"fmt"
	"io"
	"os"

	"delve_first_project/internal/snapshot"
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

	bundle, err := snapshot.ReadSnapshotBundle(f)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}

	fmt.Fprintf(out, "format: %s\n", bundle.Metadata.Format)
	fmt.Fprintf(out, "created: %s\n", bundle.Metadata.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(out, "go version: %s\n", bundle.Metadata.GoVersion)
	fmt.Fprintf(out, "pid: %d\n", bundle.Metadata.PID)
	fmt.Fprintf(out, "gc before heap dump: %t\n", bundle.Metadata.GCBeforeHeapDump)
	fmt.Fprintf(out, "%s: present, %d bytes\n", snapshot.HeapDumpFile, len(bundle.HeapDump))
	fmt.Fprintf(out, "%s: present, %d bytes\n", snapshot.GoroutineProfileFile, len(bundle.GoroutineProfile))
	fmt.Fprintf(out, "%s: valid\n", snapshot.MetadataFile)

	return nil
}
