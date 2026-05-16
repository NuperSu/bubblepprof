package labelresolve

import "testing"

func TestSourceString(t *testing.T) {
	cases := map[Source]string{
		SourceNone:         "none",
		SourceManifest:     "labels.json",
		SourceHeap:         "heap.dump",
		SourceProfileID:    "pprof.id",
		SourceProfileStack: "pprof.stack",
		Source(255):        "none",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Source(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestAttributionDescription(t *testing.T) {
	cases := map[AttributionMode]string{
		AttributionNone:              "no labels available",
		AttributionHeapNative:        "heap-native exact (runtime.g.labels from heap.dump)",
		AttributionManifest:          "labels.json exact",
		AttributionMixedExact:        "mixed exact: heap-native + labels.json",
		AttributionBestEffortProfile: "best effort: goroutine.pprof fallback used",
		AttributionMode("garbage"):   "no labels available",
	}
	for m, want := range cases {
		if got := m.Description(); got != want {
			t.Errorf("AttributionMode(%q).Description() = %q, want %q", m, got, want)
		}
	}
}

func TestResolveLabelsNilSnapshot(t *testing.T) {
	res := ResolveLabels(nil, nil, nil, Options{})
	if res.Diagnostics.Attribution != AttributionNone {
		t.Fatalf("attribution = %v", res.Diagnostics.Attribution)
	}
	if !hasWarningContaining(res.Warnings, "nil heap snapshot") {
		t.Fatalf("expected nil-snapshot warning, got %v", res.Warnings)
	}
	if len(res.LabelsByGID) != 0 {
		t.Fatalf("expected no labels, got %v", res.LabelsByGID)
	}
}

func TestNormalizeFuncName(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"runtime.goexit":  "",
		"   ":             "",
		"main.main":       "main.main",
		" main.fn   ":     "main.fn",
	}
	for in, want := range cases {
		if got := normalizeFuncName(in); got != want {
			t.Errorf("normalizeFuncName(%q) = %q, want %q", in, got, want)
		}
	}
}
