// Command bubblepprof is the external analyser for bubblepprof capture
// bundles: it fetches a bundle from a target process serving
// GET /debug/memusage/bundle (or reads a saved bundle file) and runs the
// same label-selected heap reachability analysis as the in-process
// POST /debug/memusage endpoint, out of process.
package main

import (
	"os"

	"github.com/NuperSu/bubblepprof/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
