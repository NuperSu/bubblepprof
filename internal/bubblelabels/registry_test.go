package bubblelabels

import (
	"testing"
	"time"
)

func TestPushNilReceiver(t *testing.T) {
	var r *Registry
	if got := r.Push(1, map[string]string{"a": "b"}); got == nil {
		t.Fatal("expected non-nil closer")
	}
	r.Push(1, nil)() // calling the closer must not panic
}

func TestPushZeroID(t *testing.T) {
	r := NewRegistry()
	r.Push(0, map[string]string{"a": "b"})() // must be a no-op
	if r.Len() != 0 {
		t.Fatalf("expected empty registry, got %d", r.Len())
	}
}

func TestPushDoublePopIdempotent(t *testing.T) {
	r := NewRegistry()
	pop := r.Push(42, map[string]string{"a": "b"})
	pop()
	pop() // second invocation must not panic or underflow
	if got := r.Lookup(42); got != nil {
		t.Fatalf("Lookup after pop = %v", got)
	}
}

func TestSetReplacesStack(t *testing.T) {
	r := NewRegistry()
	r.Push(7, map[string]string{"a": "1"})
	r.Set(7, map[string]string{"b": "2"})
	got := r.Lookup(7)
	if got["b"] != "2" || got["a"] != "" {
		t.Fatalf("Lookup = %v, want only b=2", got)
	}
	if r.Len() != 1 {
		t.Fatalf("len = %d", r.Len())
	}
}

func TestSetNilReceiverAndZeroID(t *testing.T) {
	var r *Registry
	r.Set(1, map[string]string{"a": "b"}) // no panic
	r2 := NewRegistry()
	r2.Set(0, map[string]string{"a": "b"})
	if r2.Len() != 0 {
		t.Fatal("Set with zero id should not register")
	}
}

func TestClearNilReceiver(t *testing.T) {
	var r *Registry
	r.Clear(1) // no panic
}

func TestClearMissingID(t *testing.T) {
	r := NewRegistry()
	r.Clear(123) // no panic, no effect
	if r.Len() != 0 {
		t.Fatalf("len = %d", r.Len())
	}
}

func TestLookupNilAndEmpty(t *testing.T) {
	var r *Registry
	if got := r.Lookup(1); got != nil {
		t.Fatalf("nil receiver lookup = %v", got)
	}
	r2 := NewRegistry()
	if got := r2.Lookup(1); got != nil {
		t.Fatalf("missing lookup = %v", got)
	}
}

func TestLenNilReceiver(t *testing.T) {
	var r *Registry
	if got := r.Len(); got != 0 {
		t.Fatalf("nil receiver Len = %d", got)
	}
}

func TestSnapshotNilReceiver(t *testing.T) {
	var r *Registry
	m := r.Snapshot(time.Now(), "source")
	if m.Format != ManifestFormatV1 {
		t.Fatalf("format = %q", m.Format)
	}
	if len(m.Goroutines) != 0 {
		t.Fatalf("goroutines = %v", m.Goroutines)
	}
}

func TestSnapshotSortsByID(t *testing.T) {
	r := NewRegistry()
	r.Set(3, map[string]string{"k": "v"})
	r.Set(1, map[string]string{"k": "v"})
	r.Set(2, map[string]string{"k": "v"})

	m := r.Snapshot(time.Now(), "src")
	if len(m.Goroutines) != 3 {
		t.Fatalf("len = %d", len(m.Goroutines))
	}
	for i := 1; i < len(m.Goroutines); i++ {
		if m.Goroutines[i-1].ID >= m.Goroutines[i].ID {
			t.Fatalf("not sorted: %+v", m.Goroutines)
		}
	}
	for _, g := range m.Goroutines {
		if g.Source != "src" {
			t.Fatalf("source = %q", g.Source)
		}
	}
}

func TestSnapshotSkipsEmptyStackEntries(t *testing.T) {
	r := NewRegistry()
	pop := r.Push(7, map[string]string{"a": "b"})
	pop() // stack becomes empty for goroutine 7
	m := r.Snapshot(time.Now(), "src")
	for _, g := range m.Goroutines {
		if g.ID == 7 {
			t.Fatalf("expected goroutine 7 to be skipped, got %v", g)
		}
	}
}

func TestLabelSetEqualOrderInsensitive(t *testing.T) {
	a, _ := MakeLabelSet("k1", "v1", "k2", "v2")
	b, _ := MakeLabelSet("k2", "v2", "k1", "v1")
	if !a.Equal(b) {
		t.Fatal("Equal should be order-insensitive")
	}
}

func TestLabelSetEqualMismatchedValues(t *testing.T) {
	a, _ := MakeLabelSet("k", "v1")
	b, _ := MakeLabelSet("k", "v2")
	if a.Equal(b) {
		t.Fatal("different values should not be equal")
	}
}

func TestLabelSetEqualMissingKey(t *testing.T) {
	a, _ := MakeLabelSet("k1", "v", "k2", "v")
	b, _ := MakeLabelSet("k1", "v", "k3", "v")
	if a.Equal(b) {
		t.Fatal("different keys should not be equal")
	}
}

func TestLabelSetEqualEmpty(t *testing.T) {
	if !(LabelSet{}).Equal(LabelSet{}) {
		t.Fatal("empty sets should be equal")
	}
}

func TestLabelSetEqualDifferentLen(t *testing.T) {
	a, _ := MakeLabelSet("k", "v")
	if a.Equal(LabelSet{}) {
		t.Fatal("different lens should not be equal")
	}
}

func TestLabelSetMapEmpty(t *testing.T) {
	if got := (LabelSet{}).Map(); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestLabelSetLenEmpty(t *testing.T) {
	if (LabelSet{}).Len() != 0 {
		t.Fatal("empty Len should be 0")
	}
}

func TestRangeShortCircuits(t *testing.T) {
	ls, _ := MakeLabelSet("a", "1", "b", "2", "c", "3")
	var seen []string
	ls.Range(func(k, v string) bool {
		seen = append(seen, k)
		return len(seen) < 2
	})
	if len(seen) != 2 {
		t.Fatalf("seen = %v, want 2 entries", seen)
	}
}
