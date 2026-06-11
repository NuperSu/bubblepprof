package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const bundlePath = "/debug/memusage/bundle"

// runFetch implements "bubblepprof fetch <url> [-o file] [-gc=true] [-timeout 5m]".
func runFetch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output file (default bubblepprof-<host>-<unixtime>.tar; \"-\" writes to stdout)")
	gc := fs.Bool("gc", true, "run a garbage collection in the target before the heap dump")
	timeout := fs.Duration("timeout", 5*time.Minute, "total request timeout")
	target, err := parseWithOneArg(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, "bubblepprof fetch: exactly one target URL is required")
		fs.Usage()
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	body, host, err := fetchBundle(ctx, target, *gc)
	if err != nil {
		fmt.Fprintf(stderr, "bubblepprof fetch: %v\n", err)
		return exitFailure
	}
	defer body.Close()

	var dst io.Writer
	name := *out
	switch name {
	case "-":
		dst = stdout
	case "":
		name = fmt.Sprintf("bubblepprof-%s-%d.tar", host, time.Now().Unix())
		fallthrough
	default:
		f, err := os.Create(name)
		if err != nil {
			fmt.Fprintf(stderr, "bubblepprof fetch: %v\n", err)
			return exitFailure
		}
		defer f.Close()
		dst = f
	}

	if _, err := io.Copy(dst, body); err != nil {
		fmt.Fprintf(stderr, "bubblepprof fetch: download: %v\n", err)
		return exitFailure
	}
	if name != "-" {
		fmt.Fprintf(stderr, "wrote %s\n", name)
	}
	return exitOK
}

// fetchBundle issues the GET request and returns the response body and
// the target hostname (for default file naming). The caller must close
// the body.
func fetchBundle(ctx context.Context, target string, gc bool) (io.ReadCloser, string, error) {
	u, err := bundleURL(target, gc)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("target returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp.Body, u.Hostname(), nil
}

// bundleURL normalizes a target URL: a base URL (no /debug/memusage/bundle
// suffix) gets the canonical path appended, and the gc query parameter is
// set explicitly.
func bundleURL(target string, gc bool) (*url.URL, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL %q: %w", target, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("target URL %q must use http or https", target)
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), bundlePath) {
		u.Path = strings.TrimRight(u.Path, "/") + bundlePath
	}
	q := u.Query()
	if gc {
		q.Set("gc", "1")
	} else {
		q.Set("gc", "0")
	}
	u.RawQuery = q.Encode()
	return u, nil
}
