package bubblelabels

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadManifestRoundTrip(t *testing.T) {
	src := Manifest{
		Format: ManifestFormatV1,
		Goroutines: []GoroutineLabels{
			{ID: 7, Labels: map[string]string{"bubble": "alpha"}},
			{ID: 8, Labels: map[string]string{"bubble": "beta", "job": "42"}},
		},
	}
	b, err := src.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := ReadManifest(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Format != ManifestFormatV1 {
		t.Fatalf("format = %q", got.Format)
	}
	if len(got.Goroutines) != 2 {
		t.Fatalf("len = %d", len(got.Goroutines))
	}
	if got.Goroutines[0].Labels["bubble"] != "alpha" {
		t.Fatalf("bubble[0] = %v", got.Goroutines[0].Labels)
	}
}

func TestReadManifestRejectsBadJSON(t *testing.T) {
	_, err := ReadManifest(strings.NewReader("{not json"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "decode labels manifest") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadManifestRejectsUnknownFormat(t *testing.T) {
	_, err := ReadManifest(strings.NewReader(`{"format":"future-v9"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported labels manifest format") {
		t.Fatalf("err = %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errReadFailed }

var errReadFailed = &readErr{"read failed"}

type readErr struct{ msg string }

func (e *readErr) Error() string { return e.msg }

func TestReadManifestPropagatesReadError(t *testing.T) {
	_, err := ReadManifest(errReader{})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "read labels manifest") {
		t.Fatalf("err = %v", err)
	}
}

func TestMarshalDefaultsFormat(t *testing.T) {
	m := Manifest{Goroutines: []GoroutineLabels{{ID: 1, Labels: map[string]string{"x": "y"}}}}
	b, err := m.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), ManifestFormatV1) {
		t.Fatalf("Marshal did not default format: %s", b)
	}
}
