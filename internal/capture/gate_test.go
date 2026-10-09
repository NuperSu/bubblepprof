package capture

import (
	"errors"
	"testing"
	"time"
)

func TestGateCooldownAndSingleFlight(t *testing.T) {
	now := time.Unix(1000, 0)
	g := NewGate(nil, 1500*time.Millisecond)
	g.now = func() time.Time { return now }
	release, err := g.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Acquire(); err == nil {
		t.Fatal("concurrent capture allowed")
	}
	release()
	release()
	_, err = g.Acquire()
	var busy *GateError
	if !errors.As(err, &busy) || !busy.Cooldown || busy.Code() != "cooldown" || busy.RetryAfterSeconds() != "2" {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	release, err = g.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := NewGate(nil, -1).Acquire(); err == nil {
		t.Fatal("negative cooldown accepted")
	}
}
