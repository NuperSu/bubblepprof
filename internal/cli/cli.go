// Package cli implements the bubblepprof external analyser command:
//
//	bubblepprof fetch <url> [-o file]
//	bubblepprof memusage <bundle-or-url> -labels k=v[,k=v...]
//	bubblepprof memusage <bundle-or-url> -label k=v [-label k=v ...]
//	bubblepprof bubbles <bundle-or-url>
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
  bubblepprof memusage <bundle-file-or-url> -label k=v [-label k=v ...] [flags]
        Report the heap memory reachable from goroutines whose pprof
        labels contain every requested key/value pair. Accepts a saved
        bundle file or a target base URL (fetched on the fly). Use
        -label for values containing commas.

  bubblepprof bubbles <bundle-file-or-url> [flags]
        List exact pprof label sets and their goroutine counts without
        building the heap object graph or computing reachability.

  bubblepprof explore <bundle-file-or-url> -queries queries.json
        Evaluate a JSON array of label requests using one graph, and report
        capture metadata, diagnostics, and indexed label groups.

  bubblepprof top <bundle-file-or-url> --by key [--sort reachable|exclusive|shared]
        Rank label groups, with exclusive and detailed shared reachability.

  bubblepprof compare <bundle-file-or-url> --a k=v --b k=v
        Compare exact reachable sets; report overlapping goroutine selections.

  bubblepprof shared <bundle-file-or-url> --a k=v --b k=v [--limit 20]
        Inspect shared object IDs, addresses, shallow sizes, and reference counts.

  bubblepprof exclusive <bundle-file-or-url> --labels k=v
        Compare a selection against all other modeled roots.

  bubblepprof objects <bundle-file-or-url> --labels k=v [--sort size --limit 20]
        Inspect reachable objects, classifications, root/group relationships,
        and a size distribution covering all selected objects.

  bubblepprof explain <bundle-file-or-url> --labels k=v --object 0xc000890000
  bubblepprof explain <bundle-file-or-url> --a k=v --b k=v --object 0xc000890000
  bubblepprof explain <bundle-file-or-url> --labels k=v --top 20
        Explain one object or aggregate findings with bounded witness paths.
        Use --by key to discover other groups reaching the objects.

These commands accept --format json for metadata and machine-readable results.
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
	case "objects", "explain":
		return runInspection(args[0], args[1:], stdout, stderr)
	case "top", "compare", "shared", "exclusive":
		return runMultiBubble(args[0], args[1:], stdout, stderr)
	case "explore":
		return runExplore(args[1:], stdout, stderr)
	case "bubbles":
		return runBubbles(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "bubblepprof: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}
