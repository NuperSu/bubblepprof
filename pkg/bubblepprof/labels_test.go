package bubblepprof

import (
	"context"
	"runtime/pprof"
	"testing"
)

func TestLabelsRejectsOddKVs(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on odd kv count")
		}
	}()
	Labels("k")
}

func TestLabelsMapRoundtrip(t *testing.T) {
	ls := Labels("bubble", "alpha", "job", "42")
	m := ls.Map()
	if m["bubble"] != "alpha" {
		t.Fatalf("bubble = %q, want alpha", m["bubble"])
	}
	if m["job"] != "42" {
		t.Fatalf("job = %q, want 42", m["job"])
	}
	if ls.Len() != 2 {
		t.Fatalf("Len = %d, want 2", ls.Len())
	}
}

func TestLabelsMapIsCopy(t *testing.T) {
	ls := Labels("k", "v")
	m := ls.Map()
	m["k"] = "mutated"
	if ls.Map()["k"] != "v" {
		t.Fatal("Map mutation should not affect LabelSet")
	}
}

func TestDoSetsLabels(t *testing.T) {
	ctx := context.Background()
	var got map[string]string
	Do(ctx, Labels("bubble", "alpha"), func(ctx context.Context) {
		got = make(map[string]string)
		pprof.ForLabels(ctx, func(k, v string) bool {
			got[k] = v
			return true
		})
	})
	if got["bubble"] != "alpha" {
		t.Fatalf("inside Do: bubble = %q, want alpha", got["bubble"])
	}
}
