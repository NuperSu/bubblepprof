package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/NuperSu/bubblepprof/internal/bundle"
)

// stageDownload validates a private spool before it can replace a destination
// or be copied to stdout. The caller closes/removes the returned file.
func stageDownload(dir string, src io.Reader, maxBytes int64) (*os.File, error) {
	f, err := os.CreateTemp(dir, ".bubblepprof-download-*")
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { f.Close(); os.Remove(f.Name()); return nil, err }
	if maxBytes < 0 {
		return fail(fmt.Errorf("maximum download bytes must be non-negative"))
	}
	if maxBytes == 0 {
		_, err = io.Copy(f, src)
	} else {
		var n int64
		n, err = io.Copy(f, io.LimitReader(src, maxBytes))
		if err == nil && n == maxBytes {
			var extra [1]byte
			n, e := io.ReadFull(src, extra[:])
			if n > 0 {
				err = fmt.Errorf("download exceeds limit %d bytes", maxBytes)
			} else if !errors.Is(e, io.EOF) {
				err = e
			}
		}
	}
	if err != nil {
		return fail(err)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	if err = bundle.ValidateArchive(f); err != nil {
		return fail(err)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return f, nil
}
