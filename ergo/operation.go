package ergo

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ErrNoPoll is returned by [Operation.Wait] when the operation was
// constructed without a Poll function.
var ErrNoPoll = errors.New("ergo: Operation requires a Poll function")

// DefaultInitialDelay is the first wait between polls when
// [Operation.InitialDelay] is zero.
const DefaultInitialDelay = time.Second

// DefaultMaxDelay caps the polling backoff ceiling when
// [Operation.MaxDelay] is zero.
const DefaultMaxDelay = 30 * time.Second

// DefaultMultiplier grows the ceiling between polls when
// [Operation.Multiplier] is < 1.
const DefaultMultiplier = 2.0

// Top-53-bits / 2^53 recipe constants for the uniform jitter draw,
// mirroring sdk-core-go/retry's full-jitter implementation per
// RFC 0007. Named constants keep the recipe legible.
const (
	jitterShiftBits = 11      // 64 - 53
	jitterMantissa  = 1 << 53 // float64 mantissa width
	midJitterDiv    = 2       // fallback divisor on entropy starvation
)

// OperationStatus is one snapshot of a long-running operation. Poll
// implementations return Done=true with Result (and optionally Err
// for terminal failure), or Done=false to request another poll.
// RetryAfter overrides the local backoff when non-zero, mirroring
// RFC 0006's server-supplied retry-after handling.
type OperationStatus[T any] struct {
	Result     T
	Err        error
	RetryAfter time.Duration
	Done       bool
}

// Operation is a long-running operation handle the Layer 1.5
// resource method returns. Consumers either poll manually via Poll
// or block via [Operation.Wait].
//
// Field order groups pointer-bearing fields at the front so the GC
// scan range stays tight; do not reorder without re-running the
// fieldalignment check.
type Operation[T any] struct {
	// Poll fetches the current status from the server. Required.
	// The Layer 1.5 method wires this to the schema's poll_rpc.
	Poll func(context.Context) (OperationStatus[T], error)

	// Sleep is the test seam for delays. Production code leaves
	// this nil so [Operation.Wait] uses time.After + ctx.Done.
	Sleep func(context.Context, time.Duration) error

	// ID identifies the underlying server-side operation. Populated
	// by the L1.5 method when it constructs the handle.
	ID string

	// InitialDelay is the first wait between polls. Zero defaults
	// to [DefaultInitialDelay].
	InitialDelay time.Duration

	// MaxDelay caps the backoff ceiling. Zero defaults to
	// [DefaultMaxDelay].
	MaxDelay time.Duration

	// Multiplier grows the ceiling between polls. Values < 1
	// default to [DefaultMultiplier].
	Multiplier float64
}

// Wait polls until the operation reports Done or ctx is cancelled.
// Total wait budget is bounded by ctx's deadline; per-poll timeouts
// come from the underlying RPC's own deadline (typically the L2
// timeout interceptor's default).
func (o *Operation[T]) Wait(ctx context.Context) (T, error) {
	var zero T
	if o == nil || o.Poll == nil {
		return zero, ErrNoPoll
	}
	initial, maxDelay, mult := o.tuning()
	sleep := o.Sleep
	if sleep == nil {
		sleep = defaultSleep
	}

	ceiling := initial
	for {
		status, err := o.Poll(ctx)
		if err != nil {
			return zero, fmt.Errorf("ergo: poll: %w", err)
		}
		if status.Done {
			if status.Err != nil {
				return status.Result, status.Err
			}
			return status.Result, nil
		}
		wait := nextDelay(ceiling, status.RetryAfter)
		if err := sleep(ctx, wait); err != nil {
			return zero, fmt.Errorf("ergo: wait: %w", err)
		}
		ceiling = grow(ceiling, mult, maxDelay)
	}
}

func (o *Operation[T]) tuning() (initial, maxDelay time.Duration, mult float64) {
	initial = o.InitialDelay
	if initial <= 0 {
		initial = DefaultInitialDelay
	}
	maxDelay = o.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultMaxDelay
	}
	mult = o.Multiplier
	if mult < 1 {
		mult = DefaultMultiplier
	}
	return initial, maxDelay, mult
}

func nextDelay(ceiling, serverHint time.Duration) time.Duration {
	if serverHint > 0 {
		return serverHint
	}
	// Full jitter per RFC 0006 using crypto/rand. FIPS-approved
	// source per the cross-SDK randomness policy.
	if ceiling <= 0 {
		return 0
	}
	return time.Duration(jitterDraw(int64(ceiling)))
}

func grow(d time.Duration, mult float64, maxDelay time.Duration) time.Duration {
	next := time.Duration(float64(d) * mult)
	if next > maxDelay {
		return maxDelay
	}
	return next
}

func jitterDraw(upper int64) int64 {
	if upper <= 0 {
		return 0
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Boot-time entropy starvation; return mid-jitter rather
		// than block the polling loop.
		return upper / midJitterDiv
	}
	bits := binary.BigEndian.Uint64(buf[:]) >> jitterShiftBits
	frac := float64(bits) / float64(uint64(jitterMantissa))
	return int64(frac * float64(upper))
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("ergo: sleep cancelled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
