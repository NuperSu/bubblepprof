package bubblepprof

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/goid"
)

func withFreshRegistry(t *testing.T) *bubblelabels.Registry {
	t.Helper()
	r := bubblelabels.NewRegistry()
	prev := UseRegistry(r)
	t.Cleanup(func() { UseRegistry(prev) })
	return r
}

func TestLabelsRejectsOddKVs(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on odd kv count")
		}
	}()
	Labels("k")
}

func TestDoRegistersAndRestores(t *testing.T) {
	r := withFreshRegistry(t)
	ctx := context.Background()

	id, ok := goid.CurrentGoroutineID()
	if !ok {
		t.Skip("goroutine ID unavailable")
	}

	if got := r.Lookup(id); got != nil {
		t.Fatalf("pre-Do lookup = %v, want nil", got)
	}

	Do(ctx, Labels("bubble", "alpha"), func(ctx context.Context) {
		if got := r.Lookup(id); !reflect.DeepEqual(got, map[string]string{"bubble": "alpha"}) {
			t.Fatalf("inside outer Do = %v", got)
		}
		Do(ctx, Labels("bubble", "beta", "job", "42"), func(ctx context.Context) {
			if got := r.Lookup(id); !reflect.DeepEqual(got, map[string]string{"bubble": "beta", "job": "42"}) {
				t.Fatalf("inside inner Do = %v", got)
			}
		})
		if got := r.Lookup(id); !reflect.DeepEqual(got, map[string]string{"bubble": "alpha"}) {
			t.Fatalf("after inner Do = %v", got)
		}
	})
	if got := r.Lookup(id); got != nil {
		t.Fatalf("after outer Do = %v, want nil", got)
	}
}

func TestGoRegistersChild(t *testing.T) {
	r := withFreshRegistry(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(1)
	var childID uint64
	var inside map[string]string
	Do(ctx, Labels("bubble", "alpha"), func(ctx context.Context) {
		Go(ctx, func(ctx context.Context) {
			defer wg.Done()
			id, ok := goid.CurrentGoroutineID()
			if !ok {
				return
			}
			childID = id
			inside = r.Lookup(id)
		})
		wg.Wait()
	})
	if childID == 0 {
		t.Skip("child goroutine ID unavailable")
	}
	if !reflect.DeepEqual(inside, map[string]string{"bubble": "alpha"}) {
		t.Fatalf("child saw labels = %v", inside)
	}
	if got := r.Lookup(childID); got != nil {
		t.Fatalf("child labels not cleared on exit: %v", got)
	}
}

func TestSnapshotLabelsCopiesRegistry(t *testing.T) {
	r := withFreshRegistry(t)
	r.Set(123, map[string]string{"bubble": "alpha"})
	r.Set(456, map[string]string{"bubble": "beta"})

	m := SnapshotLabels()
	if m.Format != bubblelabels.ManifestFormatV1 {
		t.Fatalf("format = %q", m.Format)
	}
	if len(m.Goroutines) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m.Goroutines))
	}
	// Mutate snapshot and confirm registry is untouched.
	m.Goroutines[0].Labels["bubble"] = "mutated"
	if got := r.Lookup(m.Goroutines[0].ID); got["bubble"] == "mutated" {
		t.Fatalf("snapshot mutation leaked into registry")
	}
}

func TestRegisterCurrentGoroutine(t *testing.T) {
	r := withFreshRegistry(t)
	id, ok := goid.CurrentGoroutineID()
	if !ok {
		t.Skip("goroutine ID unavailable")
	}
	ctx := context.Background()
	Do(ctx, Labels("bubble", "alpha"), func(ctx context.Context) {
		// Simulate an adopted goroutine: clear the registry, then
		// re-register from context.
		r.Clear(id)
		RegisterCurrentGoroutine(ctx)
		if got := r.Lookup(id); !reflect.DeepEqual(got, map[string]string{"bubble": "alpha"}) {
			t.Fatalf("after RegisterCurrentGoroutine = %v", got)
		}
	})
}
