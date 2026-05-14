package goroutineprofile

import (
	"bytes"
	"strings"
	"testing"

	pprofprofile "github.com/google/pprof/profile"
)

func TestParseEmptyBytes(t *testing.T) {
	_, err := Parse(nil)
	if err == nil {
		t.Fatal("expected error on empty input")
	}
	if !strings.Contains(err.Error(), "empty profile") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsCorrupt(t *testing.T) {
	_, err := Parse([]byte{0x00, 0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// Profile without a goroutine SampleType — should fall back to Value[0].
func TestParseFallbackSampleCount(t *testing.T) {
	fn := &pprofprofile.Function{ID: 1, Name: "x.fn"}
	loc := &pprofprofile.Location{ID: 1, Line: []pprofprofile.Line{{Function: fn}}}
	p := &pprofprofile.Profile{
		SampleType: []*pprofprofile.ValueType{
			{Type: "samples", Unit: "count"},
		},
		Sample: []*pprofprofile.Sample{
			{Location: []*pprofprofile.Location{loc}, Value: []int64{7}},
		},
		Function: []*pprofprofile.Function{fn},
		Location: []*pprofprofile.Location{loc},
	}
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(out.Goroutines) != 1 {
		t.Fatalf("samples = %d", len(out.Goroutines))
	}
	if out.Goroutines[0].Count != 7 {
		t.Fatalf("Count = %d, want 7", out.Goroutines[0].Count)
	}
}

// Profile with location having empty Line slice exercises the synthetic-
// frame fallback in framesFromLocations.
func TestParseSyntheticFrameOnEmptyLine(t *testing.T) {
	loc := &pprofprofile.Location{ID: 1}
	p := &pprofprofile.Profile{
		SampleType: []*pprofprofile.ValueType{{Type: "goroutine", Unit: "count"}},
		Sample: []*pprofprofile.Sample{
			{Location: []*pprofprofile.Location{loc}, Value: []int64{1}},
		},
		Location: []*pprofprofile.Location{loc},
	}
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(out.Goroutines) != 1 || len(out.Goroutines[0].Frames) != 1 {
		t.Fatalf("expected one synthetic frame, got %+v", out.Goroutines)
	}
	if out.Goroutines[0].Frames[0].Func != "" {
		t.Fatalf("expected empty synthetic Func, got %q", out.Goroutines[0].Frames[0].Func)
	}
}

// Numeric label decoding path.
func TestParseNumericLabelsExtra(t *testing.T) {
	fn := &pprofprofile.Function{ID: 1, Name: "x.fn"}
	loc := &pprofprofile.Location{ID: 1, Line: []pprofprofile.Line{{Function: fn}}}
	p := &pprofprofile.Profile{
		SampleType: []*pprofprofile.ValueType{{Type: "goroutine", Unit: "count"}},
		Sample: []*pprofprofile.Sample{
			{
				Location: []*pprofprofile.Location{loc},
				Value:    []int64{1},
				NumLabel: map[string][]int64{"goid": {99}},
			},
		},
		Function: []*pprofprofile.Function{fn},
		Location: []*pprofprofile.Location{loc},
	}
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := out.Goroutines[0].Numeric["goid"]; got != 99 {
		t.Fatalf("Numeric[goid] = %d, want 99", got)
	}
}
