package memusage

import (
	"bytes"
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/google/pprof/profile"
)

func TestPprofExportNativeParity(t *testing.T) {
	s := inspectionFixture(t)
	defer s.Close()
	r, err := s.Export(context.Background(), selector("a"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Pprof()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatal(err)
	}
	decoded, err := profile.ParseData(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	totals := make([]int64, 6)
	for _, sample := range decoded.Sample {
		for i, v := range sample.Value {
			totals[i] += v
		}
		if !reflect.DeepEqual(sample.Label["tenant"], []string{"a"}) {
			t.Fatal(sample.Label)
		}
		for _, l := range sample.Location {
			if !strings.HasPrefix(l.Line[0].Function.Name, "[bubblepprof summary]") {
				t.Fatal("allocation-like stack")
			}
		}
	}
	if !reflect.DeepEqual(totals, []int64{6, 1112, 3, 1040, 3, 72}) {
		t.Fatal(totals)
	}
	if decoded.DefaultSampleType != "reachable_space" || len(decoded.Comments) == 0 {
		t.Fatal(decoded)
	}
	// No allocation stack is exported even if allocation evidence is present.
	if len(decoded.Sample) != 2 {
		t.Fatal("duplicate object contributions")
	}
	absent, err := s.Export(context.Background(), selector("absent"))
	if err != nil {
		t.Fatal(err)
	}
	p, err = absent.Pprof()
	if err != nil || len(p.Sample) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	r.Summary.Metrics["reachable"] = Measure{Bytes: math.MaxUint64}
	if _, err := r.Pprof(); err == nil {
		t.Fatal("signed pprof overflow")
	}
}
