package memusage

import "context"

// Summary contains aggregate evidence only; identities never cross snapshots.
type Summary struct {
	Present        bool               `json:"present"`
	Goroutines     int                `json:"goroutines"`
	Roots          int                `json:"roots"`
	Metrics        map[string]Measure `json:"metrics"`
	Distribution   []SizeBucket       `json:"size_distribution"`
	RootCategories map[string]int     `json:"root_categories"`
	RootFunctions  map[string]int     `json:"root_functions"`
}

func (s *Snapshot) Summary(ctx context.Context, req Request) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	in, err := s.inspect(ctx, req)
	if err != nil {
		return nil, err
	}
	out := &Summary{Present: len(in.gids) > 0, Goroutines: len(in.gids), Metrics: map[string]Measure{}, RootCategories: map[string]int{}, RootFunctions: map[string]int{}}
	sets := map[string]objectSet{"reachable": in.selected, "exclusive": difference(in.selected, in.outside), "shared": intersection(in.selected, in.outside), "global_overlap": intersection(in.selected, in.global), "system_overlap": intersection(in.selected, in.system), "other_goroutine_overlap": intersection(in.selected, in.other)}
	for k, set := range sets {
		m, e := s.measure(set)
		if e != nil {
			return nil, e
		}
		out.Metrics[k] = m
	}
	out.Distribution, err = s.distribution(ctx, in.selected)
	if err != nil {
		return nil, err
	}
	for _, g := range s.analysis.Goroutines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, ok := in.gids[g.GoroutineID]; !ok {
			continue
		}
		for _, r := range g.Roots {
			out.Roots++
			out.RootCategories[r.Kind]++
			if r.Kind == "stack" && r.Detail != "" {
				out.RootFunctions[r.Detail]++
			}
		}
	}
	return out, nil
}
