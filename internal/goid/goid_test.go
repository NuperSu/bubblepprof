package goid

import (
	"sync"
	"testing"
)

func TestParseHeader(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		wantID uint64
		wantOK bool
	}{
		{"basic running", "goroutine 1 [running]:\nmain.main()\n", 1, true},
		{"larger id", "goroutine 12345 [select]:\n", 12345, true},
		{"max-ish id", "goroutine 18446744073709551615 [running]:\n", 18446744073709551615, true},
		{"empty", "", 0, false},
		{"short prefix", "gorout", 0, false},
		{"missing space after id", "goroutine 1[running]:", 0, false},
		{"missing bracket", "goroutine 1 running:", 0, false},
		{"no digits", "goroutine X [running]:", 0, false},
		{"trailing only digits then EOF", "goroutine 7", 0, false},
		{"wrong prefix", "Goroutine 1 [running]:", 0, false},
		{"overflow", "goroutine 99999999999999999999999 [running]:", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := ParseHeader([]byte(tc.in))
			if ok != tc.wantOK || id != tc.wantID {
				t.Fatalf("ParseHeader(%q) = (%d,%v), want (%d,%v)", tc.in, id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestCurrentGoroutineID(t *testing.T) {
	id, ok := CurrentGoroutineID()
	if !ok || id == 0 {
		t.Fatalf("CurrentGoroutineID() = (%d, %v), want non-zero ok", id, ok)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var childID uint64
	var childOK bool
	go func() {
		defer wg.Done()
		childID, childOK = CurrentGoroutineID()
	}()
	wg.Wait()
	if !childOK || childID == 0 {
		t.Fatalf("child CurrentGoroutineID() = (%d, %v)", childID, childOK)
	}
	if childID == id {
		t.Fatalf("parent and child IDs collided: %d", id)
	}
}
