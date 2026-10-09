package capture

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Gate coordinates captures and their optional cooldown. A failed capture also
// starts the cooldown. Shared endpoints must use the same Gate.
type Gate struct {
	sem      chan struct{}
	mu       sync.Mutex
	next     time.Time
	cooldown time.Duration
	now      func() time.Time
}
type GateError struct {
	RetryAfter time.Duration
	Cooldown   bool
}

func (e *GateError) Error() string {
	if e.Cooldown {
		return "capture cooldown is active"
	}
	return "another memusage request is already running"
}
func NewGate(sem chan struct{}, cooldown time.Duration) *Gate {
	if sem == nil {
		sem = make(chan struct{}, 1)
	}
	return &Gate{sem: sem, cooldown: cooldown, now: time.Now}
}
func (g *Gate) Acquire() (func(), error) {
	if g.cooldown < 0 {
		return nil, fmt.Errorf("capture cooldown must be non-negative")
	}
	select {
	case g.sem <- struct{}{}:
	default:
		return nil, &GateError{}
	}
	g.mu.Lock()
	remaining := g.next.Sub(g.now())
	g.mu.Unlock()
	if remaining > 0 {
		<-g.sem
		return nil, &GateError{RetryAfter: remaining, Cooldown: true}
	}
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.next = g.now().Add(g.cooldown); g.mu.Unlock(); <-g.sem }) }, nil
}

func (e *GateError) Code() string {
	if e.Cooldown {
		return "cooldown"
	}
	return "busy"
}
func (e *GateError) RetryAfterSeconds() string {
	n := e.RetryAfter / time.Second
	if e.RetryAfter%time.Second != 0 {
		n++
	}
	return strconv.FormatInt(int64(n), 10)
}
