package main

import (
	"io"
	"os"

	"delve_first_project/internal/snapshotinfo"
)

func runSnapshotCommand(out, errOut io.Writer, args []string) int {
	return snapshotinfo.Run(out, errOut, os.Args[0], args)
}
