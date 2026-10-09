//go:build linux || darwin || freebsd

package diskspace

import (
	"fmt"
	"golang.org/x/sys/unix"
	"math"
)

func Available(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	if st.Bsize <= 0 {
		return 0, fmt.Errorf("invalid filesystem block size")
	}
	// FreeBSD can report a negative user-available block count.
	if st.Bavail <= 0 {
		return 0, nil
	}
	blocks, size := uint64(st.Bavail), uint64(st.Bsize)
	if blocks > math.MaxUint64/size {
		return math.MaxUint64, nil
	}
	return blocks * size, nil
}
