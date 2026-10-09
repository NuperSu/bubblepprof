package offline

import (
	"github.com/NuperSu/bubblepprof/internal/memusage"
	"math"
	"testing"
)

func TestExactSignedDeltas(t *testing.T) {
	for _, tt := range []struct {
		a, b uint64
		want string
	}{{0, math.MaxUint64, "18446744073709551615"}, {math.MaxUint64, 0, "-18446744073709551615"}, {7, 7, "0"}} {
		if got := measureDelta(memusage.Measure{Bytes: tt.a}, memusage.Measure{Bytes: tt.b}); got.Bytes != tt.want {
			t.Fatal(got)
		}
	}
}
func TestStructuralChangesAndPresence(t *testing.T) {
	a := &memusage.Summary{Present: true, Goroutines: 1, Roots: 2, Metrics: map[string]memusage.Measure{"reachable": {Objects: 1, Bytes: 8}}, Distribution: []memusage.SizeBucket{{Min: 8, Max: 15, Measure: memusage.Measure{Objects: 1, Bytes: 8}}}, RootCategories: map[string]int{"stack": 2}, RootFunctions: map[string]int{"main.worker": 2}}
	b := &memusage.Summary{Metrics: map[string]memusage.Measure{"reachable": {}}}
	c := change(map[string]string{"tenant": "a"}, a, b)
	if !c.Before.Present || c.After.Present || c.Metrics["reachable"].Bytes != "-8" || c.Distribution[0].Bytes != "-8" || c.RootFunctions["main.worker"] != -2 || c.RootCategories["stack"] != -2 {
		t.Fatalf("%+v", c)
	}
	b.Present = true
	b.Goroutines = 1
	c = change(nil, a, b)
	if !c.After.Present || c.After.Metrics["reachable"].Bytes != 0 {
		t.Fatal(c)
	}
}
