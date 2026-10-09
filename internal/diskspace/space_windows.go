package diskspace

import (
	"golang.org/x/sys/windows"
	"path/filepath"
)

func Available(path string) (uint64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	p, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return 0, err
	}
	var free, total, unused uint64
	err = windows.GetDiskFreeSpaceEx(p, &free, &total, &unused)
	return free, err
}
