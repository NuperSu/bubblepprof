package memusage

import (
	"context"
	"fmt"
	"math"

	"github.com/google/pprof/profile"
)

const ExportSchemaVersion = 1

// ExportReport is a portable aggregate report; the native graph is retained by
// the session. Neither this report nor pprof replaces reference graph evidence.
type ExportReport struct {
	SchemaVersion int               `json:"schema_version"`
	Labels        map[string]string `json:"labels"`
	Metadata      SnapshotMetadata  `json:"metadata"`
	Summary       *Summary          `json:"summary"`
	Semantics     string            `json:"semantics"`
}

const exportSemantics = "Distinct reachable objects and shallow bytes, partitioned into exclusive and shared reachability relative to other modeled roots. Synthetic summary frames are not allocation or call stacks. Separate selectors are not additive."

func (s *Snapshot) Export(ctx context.Context, req Request) (*ExportReport, error) {
	summary, err := s.Summary(ctx, req)
	if err != nil {
		return nil, err
	}
	return &ExportReport{ExportSchemaVersion, copyLabels(req.Labels), s.Metadata(), summary, exportSemantics}, nil
}

// Pprof represents the two disjoint reachability partitions using explicitly
// synthetic frames. Each object contributes to exactly one partition sample.
// No reference path is presented as an allocation stack.
func (r *ExportReport) Pprof() (*profile.Profile, error) {
	p := &profile.Profile{SampleType: []*profile.ValueType{{Type: "reachable_objects", Unit: "count"}, {Type: "reachable_space", Unit: "bytes"}, {Type: "exclusive_objects", Unit: "count"}, {Type: "exclusive_space", Unit: "bytes"}, {Type: "shared_objects", Unit: "count"}, {Type: "shared_space", Unit: "bytes"}}, DefaultSampleType: "reachable_space", PeriodType: &profile.ValueType{Type: "space", Unit: "bytes"}, Period: 1, Comments: []string{r.Semantics}}
	// pprof values and aggregations are signed int64. Fail explicitly rather than wrap.
	for _, k := range []string{"reachable", "exclusive", "shared"} {
		if r.Summary.Metrics[k].Bytes > math.MaxInt64 {
			return nil, fmt.Errorf("%s byte total exceeds pprof int64 range", k)
		}
	}
	root := &profile.Function{ID: 1, Name: "[bubblepprof summary] selected reachable set", SystemName: "bubblepprof.summary"}
	rootLoc := &profile.Location{ID: 1, Line: []profile.Line{{Function: root}}}
	p.Function = append(p.Function, root)
	p.Location = append(p.Location, rootLoc)
	for i, k := range []string{"exclusive", "shared"} {
		m := r.Summary.Metrics[k]
		if m.Objects == 0 {
			continue
		}
		f := &profile.Function{ID: uint64(i + 2), Name: "[bubblepprof summary] " + k + " reachability", SystemName: "bubblepprof.summary." + k}
		loc := &profile.Location{ID: f.ID, Line: []profile.Line{{Function: f}}}
		p.Function = append(p.Function, f)
		p.Location = append(p.Location, loc)
		values := []int64{int64(m.Objects), int64(m.Bytes), 0, 0, 0, 0}
		values[2+i*2] = int64(m.Objects)
		values[3+i*2] = int64(m.Bytes)
		labels := map[string][]string{}
		for k, v := range r.Labels {
			labels[k] = []string{v}
		}
		p.Sample = append(p.Sample, &profile.Sample{Location: []*profile.Location{loc, rootLoc}, Value: values, Label: labels})
	}
	return p, p.CheckValid()
}
