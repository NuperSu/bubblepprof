package capture

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"time"

	"bubblepprof/internal/bubblelabels"
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

// RuntimeGoroutineStacksWriter emits debug=2 goroutine stacks via
// pprof.Lookup("goroutine"). It is used as the default
// GoroutineStacksWriter when no explicit one is configured.
type RuntimeGoroutineStacksWriter struct{}

func (RuntimeGoroutineStacksWriter) WriteGoroutineStacks(w io.Writer) error {
	p := pprof.Lookup("goroutine")
	if p == nil {
		return fmt.Errorf("goroutine profile not found")
	}
	return p.WriteTo(w, 2)
}

// RegistryLabelManifestProvider snapshots a bubblelabels.Registry into a
// labels.json payload. When the registry has no entries it returns nil
// bytes so the capture path omits the labels.json entry from the tar.
type RegistryLabelManifestProvider struct {
	Registry *bubblelabels.Registry
	Source   string
}

func (p RegistryLabelManifestProvider) LabelManifest() ([]byte, error) {
	if p.Registry == nil || p.Registry.Len() == 0 {
		return nil, nil
	}
	m := p.Registry.Snapshot(time.Now(), p.Source)
	if len(m.Goroutines) == 0 {
		return nil, nil
	}
	return m.Marshal()
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
