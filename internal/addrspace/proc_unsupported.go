//go:build !linux && !darwin && !windows

package addrspace

// ProcessReader is a stub for platforms where process memory reading
// is not yet implemented. OpenSelfProcessReader always returns
// ErrUnsupported.
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
