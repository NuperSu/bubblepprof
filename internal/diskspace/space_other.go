//go:build !linux && !darwin && !freebsd && !windows

package diskspace

func Available(string) (uint64, error) { return 0, ErrUnsupported }
