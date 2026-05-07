// Package clock provides a small abstraction over time so retry, idempotency,
// and token caching can be tested deterministically.
package clock

import "time"

// Clock returns the current time and provides delays. Inject in interceptors
// instead of calling time.Now or time.NewTimer directly.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is the subset of time.Timer we need.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Real returns a [Clock] backed by the standard library.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time { return r.t.C }
func (r *realTimer) Stop() bool          { return r.t.Stop() }
