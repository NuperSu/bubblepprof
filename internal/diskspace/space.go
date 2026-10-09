// Package diskspace provides a capture-side free-space preflight, not an
// analysis dependency. A check is advisory: free space can change immediately.
package diskspace

import (
	"errors"
	"fmt"
)

var ErrUnsupported = errors.New("free-space query is unsupported on this platform")

const DefaultMinFreeBytes uint64 = 64 << 20

func Check(path string, minimum uint64) error {
	n, err := Available(path)
	if errors.Is(err, ErrUnsupported) && minimum == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check scratch space: %w", err)
	}
	if minimum == 0 {
		minimum = DefaultMinFreeBytes
	}
	if n < minimum {
		return fmt.Errorf("insufficient scratch space: available %d bytes, need at least %d", n, minimum)
	}
	return nil
}
