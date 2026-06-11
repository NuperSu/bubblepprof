// Package cli implements the bubblepprof external analyser command:
//
//	bubblepprof fetch <url> [-o file]
//	bubblepprof memusage <bundle-or-url> -labels k=v[,k=v...]
//
// The analysis path is the same memusage.AnalyzeDump pipeline used by
// the in-process /debug/memusage endpoint, fed by a capture bundle
// instead of a live heap dump, so responses and error codes are
// identical in both modes.
package cli

import (
	"flag"
	"fmt"
	"io"
)

// Exit codes: 0 success, 1 usage error, 2 analysis/transport failure.
const (
	exitOK      = 0
	exitUsage   = 1
	exitFailure = 2
)

const usage = `bubblepprof is the external analyser for bubblepprof bundles.

Usage:

  bubblepprof fetch <url> [flags]
        Download a capture bundle from a target process that serves
        GET /debug/memusage/bundle.

  bubblepprof memusage <bundle-file-or-url> -labels k=v[,k=v...] [flags]
        Report the heap memory reachable from goroutines whose pprof
        labels contain every requested key/value pair. Accepts a saved
        bundle file or a target base URL (fetched on the fly).

Run "bubblepprof <command> -h" for command flags.
`

// parseWithOneArg parses args allowing flags and exactly one positional
// argument in any order (stdlib flag stops at the first positional, so
// "memusage app.tar -labels a=b" needs a re-parse of the remainder).
func parseWithOneArg(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	target := ""
	for fs.NArg() > 0 {
		rest := fs.Args()
		if target != "" {
			return "", fmt.Errorf("unexpected extra argument %q", rest[0])
		}
		target = rest[0]
		if err := fs.Parse(rest[1:]); err != nil {
			return "", err
		}
	}
	if target == "" {
		return "", fmt.Errorf("missing argument")
	}
	return target, nil
}

// Main runs the CLI and returns its process exit code. It is the whole
// implementation behind cmd/bubblepprof, kept here so tests can drive
// it in-process.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "fetch":
		return runFetch(args[1:], stdout, stderr)
	case "memusage":
		return runMemUsage(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "bubblepprof: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}
