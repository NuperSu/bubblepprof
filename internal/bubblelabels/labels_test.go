package bubblelabels

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMakeLabelSet(t *testing.T) {
	if _, err := MakeLabelSet("k"); err == nil {
		t.Fatal("expected error on odd kv count")
	}
	ls, err := MakeLabelSet("bubble", "alpha", "job", "42")
	if err != nil {
		t.Fatalf("MakeLabelSet: %v", err)
	}
	if got, want := ls.Len(), 2; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}
	if got := ls.Map(); !reflect.DeepEqual(got, map[string]string{"bubble": "alpha", "job": "42"}) {
		t.Fatalf("Map = %v", got)
	}

	// Duplicate keys keep the last value.
	dup, err := MakeLabelSet("k", "v1", "k", "v2")
	if err != nil {
		t.Fatalf("MakeLabelSet dup: %v", err)
	}
	if got := dup.Map(); !reflect.DeepEqual(got, map[string]string{"k": "v2"}) {
		t.Fatalf("dup map = %v", got)
	}
}

func TestLabelSetEqualAndRange(t *testing.T) {
	a, _ := MakeLabelSet("a", "1", "b", "2")
	b, _ := MakeLabelSet("b", "2", "a", "1")
	if !a.Equal(b) {
		t.Fatalf("expected equal sets")
	}
	c, _ := MakeLabelSet("a", "1")
	if a.Equal(c) {
		t.Fatalf("did not expect a == c")
	}

	var keys []string
	a.Range(func(k, v string) bool {
		keys = append(keys, k+"="+v)
		return true
	})
	if got, want := keys, []string{"a=1", "b=2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Range = %v, want %v", got, want)
	}

	keys = keys[:0]
	a.Range(func(k, v string) bool {
		keys = append(keys, k+"="+v)
		return false // short-circuit
	})
	if got, want := keys, []string{"a=1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Range short-circuit = %v, want %v", got, want)
	}
}

func TestRegistryPushPop(t *testing.T) {
	r := NewRegistry()
	close1 := r.Push(7, map[string]string{"bubble": "alpha"})
	if got := r.Lookup(7); !reflect.DeepEqual(got, map[string]string{"bubble": "alpha"}) {
		t.Fatalf("lookup after push = %v", got)
	}
	close2 := r.Push(7, map[string]string{"bubble": "beta"})
	if got := r.Lookup(7); !reflect.DeepEqual(got, map[string]string{"bubble": "beta"}) {
		t.Fatalf("nested lookup = %v", got)
	}
	close2()
	if got := r.Lookup(7); !reflect.DeepEqual(got, map[string]string{"bubble": "alpha"}) {
		t.Fatalf("lookup after inner close = %v", got)
	}
	// Idempotent close.
	close2()
	if got := r.Lookup(7); !reflect.DeepEqual(got, map[string]string{"bubble": "alpha"}) {
		t.Fatalf("idempotent inner close changed state = %v", got)
	}
	close1()
	if got := r.Lookup(7); got != nil {
		t.Fatalf("lookup after outer close = %v, want nil", got)
	}
	if r.Len() != 0 {
		t.Fatalf("registry not empty after all closes")
	}
}

func TestRegistrySetAndClear(t *testing.T) {
	r := NewRegistry()
	r.Set(11, map[string]string{"k": "v"})
	if got := r.Lookup(11); got["k"] != "v" {
		t.Fatalf("set/lookup failed: %v", got)
	}
	r.Set(11, map[string]string{"k": "w"})
	if got := r.Lookup(11); got["k"] != "w" {
		t.Fatalf("set overwrite failed: %v", got)
	}
	r.Clear(11)
	if got := r.Lookup(11); got != nil {
		t.Fatalf("clear left state: %v", got)
	}
}

func TestRegistryIgnoresZeroID(t *testing.T) {
	r := NewRegistry()
	closer := r.Push(0, map[string]string{"k": "v"})
	if r.Len() != 0 {
		t.Fatalf("registry recorded zero-ID push")
	}
	closer()
	r.Set(0, map[string]string{"k": "v"})
	if r.Len() != 0 {
		t.Fatalf("registry recorded zero-ID Set")
	}
}

func TestRegistrySnapshotIsCopy(t *testing.T) {
	r := NewRegistry()
	r.Push(3, map[string]string{"bubble": "alpha"})
	r.Push(1, map[string]string{"bubble": "beta"})

	m := r.Snapshot(time.Unix(1, 0), "test")
	if m.Format != ManifestFormatV1 {
		t.Fatalf("format = %q", m.Format)
	}
	if len(m.Goroutines) != 2 {
		t.Fatalf("goroutines = %d", len(m.Goroutines))
	}
	// Sorted by id: 1, 3.
	if m.Goroutines[0].ID != 1 || m.Goroutines[1].ID != 3 {
		t.Fatalf("not sorted: %+v", m.Goroutines)
	}

	// Mutate the snapshot; registry must be unaffected.
	m.Goroutines[0].Labels["bubble"] = "mutated"
	if got := r.Lookup(1); got["bubble"] != "beta" {
		t.Fatalf("registry mutated through snapshot copy: %v", got)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	in := Manifest{
		Format:    ManifestFormatV1,
		CreatedAt: time.Unix(1700000000, 0).UTC(),
		Goroutines: []GoroutineLabels{
			{ID: 1, Labels: map[string]string{"bubble": "alpha"}, Source: "bubblepprof.Do"},
			{ID: 2, Labels: map[string]string{"bubble": "beta", "tenant": "acme"}},
		},
	}
	b, err := in.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := DecodeManifest(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(in, *out) {
		t.Fatalf("round trip mismatch:\n in: %+v\nout: %+v", in, *out)
	}
}

func TestDecodeManifestRejectsBadFormat(t *testing.T) {
	bad := []byte(`{"format":"other","goroutines":[]}`)
	if _, err := DecodeManifest(bad); err == nil {
		t.Fatalf("expected error on bad format")
	}
	if _, err := DecodeManifest([]byte("not json")); err == nil {
		t.Fatalf("expected error on invalid json")
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N * 3)
	for i := 0; i < N; i++ {
		id := uint64(i + 1)
		go func() {
			defer wg.Done()
			r.Push(id, map[string]string{"k": "v"})()
		}()
		go func() {
			defer wg.Done()
			r.Snapshot(time.Now(), "test")
		}()
		go func() {
			defer wg.Done()
			r.Lookup(id)
		}()
	}
	wg.Wait()
	if r.Len() != 0 {
		t.Fatalf("registry leaked entries: %d", r.Len())
	}
}
