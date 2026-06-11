//go:build !linux && !darwin && !windows && !freebsd

package addrspace

// ProcessReader is a stub for platforms where process memory reading
// is not yet implemented. OpenSelfProcessReader always returns
// ErrUnsupported.
type ProcessReader struct{}

// OpenSelfProcessReader returns ErrUnsupported on platforms without an
// implementation (i.e. anything other than Linux, macOS, Windows, FreeBSD).
func OpenSelfProcessReader() (*ProcessReader, error) {
	return nil, ErrUnsupported
}

// Close is a no-op on platforms without a process-memory implementation.
func (r *ProcessReader) Close() error { return nil }

// Name implements NamedReader.
func (r *ProcessReader) Name() string { return "process" }

// Source returns a fixed "unsupported" sentinel on platforms without an
// implementation. Used for diagnostics (e.g. by cmd/labeloffsetprobe).
func (r *ProcessReader) Source() string { return "unsupported" }

// Mappings always returns nil on platforms without an implementation.
func (r *ProcessReader) Mappings() []Mapping { return nil }

// EligibleStringRanges always returns nil on platforms without an
// implementation.
func (r *ProcessReader) EligibleStringRanges() []Mapping { return nil }

// ReadAtAddr always returns ok=false (with the size==0 exception) on
// platforms without an implementation.
func (r *ProcessReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if size == 0 {
		return []byte{}, true
	}
	return nil, false
}
