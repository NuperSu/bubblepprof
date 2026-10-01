package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

	name := *out
	if name == "-" {
		if _, err := io.Copy(stdout, body); err != nil {
			fmt.Fprintf(stderr, "bubblepprof fetch: download: %v\n", err)
			return exitFailure
		}
	} else {
		if name == "" {
			name = fmt.Sprintf("bubblepprof-%s-%d.tar", host, time.Now().Unix())
		}
		if err := saveDownload(name, body); err != nil {
			fmt.Fprintf(stderr, "bubblepprof fetch: download: %v\n", err)
			return exitFailure
		}
		fmt.Fprintf(stderr, "wrote %s\n", name)
	}
	return exitOK
}

// saveDownload replaces the destination only after a complete, private download.
func saveDownload(name string, src io.Reader) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".bubblepprof-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
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
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, bundlePath) {
		u.Path = path
	} else {
		u.Path = path + bundlePath
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
