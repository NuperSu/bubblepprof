package goroutineprofile

import (
	"bytes"
	"fmt"

	pprofprofile "github.com/google/pprof/profile"
)

// Parse decodes a pprof-format goroutine profile (debug=0) into a
// Profile. The payload is the bytes stored as goroutine.pprof inside
// snapshot.tar.
//
// Parsing ignores the value vector (it is informational for the
// "goroutine" profile) and reads Sample.NumLabel into Numeric for the
// caller's convenience. Numeric labels are not used for correlation by
// default.
func Parse(b []byte) (*Profile, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("goroutineprofile: empty profile")
	}
	p, err := pprofprofile.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("goroutineprofile: parse pprof: %w", err)
	}
	if err := p.CheckValid(); err != nil {
		return nil, fmt.Errorf("goroutineprofile: invalid pprof: %w", err)
	}

	out := &Profile{
		Goroutines: make([]GoroutineSample, 0, len(p.Sample)),
	}

	for _, s := range p.Sample {
		gs := GoroutineSample{
			Count: sampleCount(s, p.SampleType),
		}
		if len(s.Label) > 0 {
			gs.Labels = make(map[string]string, len(s.Label))
			for k, v := range s.Label {
				if len(v) == 0 {
					continue
				}
				gs.Labels[k] = v[0]
			}
		}
		if len(s.NumLabel) > 0 {
			gs.Numeric = make(map[string]int64, len(s.NumLabel))
			for k, v := range s.NumLabel {
				if len(v) == 0 {
					continue
				}
				gs.Numeric[k] = v[0]
			}
		}
		gs.Frames = framesFromLocations(s.Location)
		out.Goroutines = append(out.Goroutines, gs)
	}

	return out, nil
}

func framesFromLocations(locs []*pprofprofile.Location) []Frame {
	var frames []Frame
	for _, loc := range locs {
		if loc == nil {
			continue
		}
		if len(loc.Line) == 0 {
			// Some profiles record locations without inlined Line data;
			// fall back to a synthetic frame so the slot is preserved
			// for stack-signature correlation.
			frames = append(frames, Frame{})
			continue
		}
		// pprof records callee-first within each Location.Line. The
		// outermost (least inlined) function is at the end of the
		// slice. We want callee-first too, so the slice order matches.
		for _, ln := range loc.Line {
			fr := Frame{Line: ln.Line}
			if ln.Function != nil {
				fr.Func = ln.Function.Name
				fr.File = ln.Function.Filename
			}
			frames = append(frames, fr)
		}
	}
	return frames
}

// sampleCount picks the "count" value from a sample. The runtime emits
// the goroutine profile with a "goroutine/count" sample type as Value[0]
// and "cpu/nanoseconds" as Value[1]; we prefer the dedicated count when
// available and fall back to the first value otherwise.
func sampleCount(s *pprofprofile.Sample, types []*pprofprofile.ValueType) int64 {
	if len(s.Value) == 0 {
		return 0
	}
	for i, t := range types {
		if t != nil && t.Type == "goroutine" && i < len(s.Value) {
			return s.Value[i]
		}
	}
	return s.Value[0]
}
