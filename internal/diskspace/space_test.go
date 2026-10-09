package diskspace

import (
	"errors"
	"math"
	"testing"
)

func TestSpacePreflight(t *testing.T) {
	dir := t.TempDir()
	n, err := Available(dir)
	if errors.Is(err, ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil || n == 0 {
		t.Fatalf("%d %v", n, err)
	}
	if err := Check(dir, 1); err != nil {
		t.Fatal(err)
	}
	if err := Check(dir, math.MaxUint64); err == nil {
		t.Fatal("impossible minimum accepted")
	}
	if _, err := Available(dir + "/missing"); err == nil {
		t.Fatal("missing directory accepted")
	}
}
