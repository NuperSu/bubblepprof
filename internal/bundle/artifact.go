package bundle

import (
	"fmt"
	"io"
)

type artifactWriter struct {
	w            io.Writer
	max, written int64
}

func (w *artifactWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.max-w.written {
		return 0, fmt.Errorf("capture artifact exceeds limit %d bytes", w.max)
	}
	n, err := w.w.Write(p)
	w.written += int64(n)
	return n, err
}
