package capture

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"time"

	"bubblepprof/internal/snapshot"
)

type RuntimeHeapDumpWriter struct{}

func (RuntimeHeapDumpWriter) WriteHeapDump(fd uintptr) error {
	debug.WriteHeapDump(fd)
	return nil
}

type RuntimeGoroutineProfileWriter struct{}

func (RuntimeGoroutineProfileWriter) WriteGoroutineProfile(w io.Writer) error {
	p := pprof.Lookup("goroutine")
	if p == nil {
		return fmt.Errorf("goroutine profile not found")
	}
	return p.WriteTo(w, 0)
}

type RuntimeMetadataProvider struct{}

func (RuntimeMetadataProvider) Metadata(gcBeforeHeapDump bool) snapshot.SnapshotMetadata {
	return snapshot.SnapshotMetadata{
		Format:               snapshot.FormatV1,
		CreatedAt:            time.Now().UTC(),
		GoVersion:            runtime.Version(),
		PID:                  os.Getpid(),
		HeapDumpFile:         snapshot.HeapDumpFile,
		GoroutineProfileFile: snapshot.GoroutineProfileFile,
		GCBeforeHeapDump:     gcBeforeHeapDump,
	}
}
