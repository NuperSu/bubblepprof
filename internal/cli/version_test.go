package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	for _, cmd := range []string{"version", "--version"} {
		var out, err bytes.Buffer
		if code := Main([]string{cmd}, &out, &err); code != 0 || !strings.HasPrefix(out.String(), "bubblepprof ") {
			t.Fatal(code, &out, &err)
		}
	}
	var out, err bytes.Buffer
	if code := Main([]string{"version", "extra"}, &out, &err); code != exitUsage {
		t.Fatal(code)
	}
}
