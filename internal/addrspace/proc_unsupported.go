//go:build !linux

package addrspace

// Mapping mirrors the Linux variant so callers that import this type
// compile on every platform. Fields are unused on non-Linux builds.
type Mapping struct {
	Start uint64
	End   uint64
	Read  bool
	Write bool
	Exec  bool
	Path  string
}

// ProcessReader is a stub for platforms where /proc/self/mem is not
// available. OpenSelfProcessReader always returns ErrUnsupported.
type ProcessReader struct{}

// OpenSelfProcessReader returns ErrUnsupported on non-Linux builds.
func OpenSelfProcessReader() (*ProcessReader, error) {
	return nil, ErrUnsupported
}

// Close is a no-op on non-Linux builds.
func (r *ProcessReader) Close() error { return nil }

// Name implements NamedReader.
func (r *ProcessReader) Name() string { return "process" }

// Mappings always returns nil on non-Linux builds.
func (r *ProcessReader) Mappings() []Mapping { return nil }

// ReadAtAddr always returns ok=false (with the size==0 exception) on
// non-Linux builds because there is no implementation.
func (r *ProcessReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if size == 0 {
		return []byte{}, true
	}
	return nil, false
}
