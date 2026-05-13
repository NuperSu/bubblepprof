package goroutineprofile

import (
	"bytes"
	"testing"

	pprofprofile "github.com/google/pprof/profile"
)

func buildTestProfile(t *testing.T) []byte {
	t.Helper()
	fnRun := &pprofprofile.Function{ID: 1, Name: "worker.run", Filename: "worker.go"}
	fnLoop := &pprofprofile.Function{ID: 2, Name: "worker.loop", Filename: "worker.go"}
	fnMain := &pprofprofile.Function{ID: 3, Name: "main.main", Filename: "main.go"}

	locRun := &pprofprofile.Location{ID: 1, Line: []pprofprofile.Line{{Function: fnRun, Line: 10}}}
	locLoop := &pprofprofile.Location{ID: 2, Line: []pprofprofile.Line{{Function: fnLoop, Line: 20}}}
	locMain := &pprofprofile.Location{ID: 3, Line: []pprofprofile.Line{{Function: fnMain, Line: 1}}}

	p := &pprofprofile.Profile{
		SampleType: []*pprofprofile.ValueType{
			{Type: "goroutine", Unit: "count"},
		},
		Sample: []*pprofprofile.Sample{
			{
				Location: []*pprofprofile.Location{locRun, locLoop, locMain},
				Value:    []int64{2},
				Label:    map[string][]string{"bubble": {"alpha"}, "job": {"42"}},
			},
			{
				Location: []*pprofprofile.Location{locLoop, locMain},
				Value:    []int64{1},
			},
		},
		Function: []*pprofprofile.Function{fnRun, fnLoop, fnMain},
		Location: []*pprofprofile.Location{locRun, locLoop, locMain},
	}

	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return buf.Bytes()
}

func TestParseLabeledAndUnlabeled(t *testing.T) {
	b := buildTestProfile(t)
	got, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Goroutines) != 2 {
		t.Fatalf("samples = %d", len(got.Goroutines))
	}
	first := got.Goroutines[0]
	if first.Count != 2 {
		t.Fatalf("first count = %d", first.Count)
	}
	if first.Labels["bubble"] != "alpha" || first.Labels["job"] != "42" {
		t.Fatalf("labels = %v", first.Labels)
	}
	if len(first.Frames) != 3 || first.Frames[0].Func != "worker.run" {
		t.Fatalf("frames = %+v", first.Frames)
	}

	second := got.Goroutines[1]
	if second.Count != 1 {
		t.Fatalf("second count = %d", second.Count)
	}
	if len(second.Labels) != 0 {
		t.Fatalf("expected no labels on second sample: %v", second.Labels)
	}
	if second.Frames[0].Func != "worker.loop" {
		t.Fatalf("second top frame = %s", second.Frames[0].Func)
	}
}

func TestParseEmptyAndInvalid(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatal("expected error on nil")
	}
	if _, err := Parse([]byte("not a pprof")); err == nil {
		t.Fatal("expected error on garbage bytes")
	}
}

func TestParseNumericLabels(t *testing.T) {
	fn := &pprofprofile.Function{ID: 1, Name: "x"}
	loc := &pprofprofile.Location{ID: 1, Line: []pprofprofile.Line{{Function: fn}}}
	p := &pprofprofile.Profile{
		SampleType: []*pprofprofile.ValueType{{Type: "goroutine", Unit: "count"}},
		Sample: []*pprofprofile.Sample{
			{
				Location: []*pprofprofile.Location{loc},
				Value:    []int64{1},
				NumLabel: map[string][]int64{"bytes": {1024}},
				NumUnit:  map[string][]string{"bytes": {"bytes"}},
			},
		},
		Function: []*pprofprofile.Function{fn},
		Location: []*pprofprofile.Location{loc},
	}
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	got, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Goroutines[0].Numeric["bytes"] != 1024 {
		t.Fatalf("numeric labels = %v", got.Goroutines[0].Numeric)
	}
}
