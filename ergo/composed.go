package ergo

import (
	"context"
	"errors"
	"strconv"
)

// ErrNilComposedOp is returned by [Run] when the supplied
// ComposedOp pointer is nil. Indicates a programming error.
var ErrNilComposedOp = errors.New("ergo: nil ComposedOp")

// ComposedOp orchestrates a multi-RPC operation under one L1.5
// entry point per RFC 0016. Each call to [Run] derives a fresh
// idempotency key as `{ID}/{leg}` and threads the same correlation
// id through every leg. Distinct ComposedOp instances get distinct
// IDs (concurrent invocations do not collide); distinct legs of
// one instance get distinct keys (independent retryability).
type ComposedOp struct {
	// ID uniquely identifies this composed-op invocation. Generated
	// at construction; never reused.
	ID string

	// Correlation propagates through all legs as the correlation
	// id. Inherited from the parent context when present; otherwise
	// generated at construction.
	Correlation string

	leg int
}

// NewComposedOp constructs a ComposedOp seeded with a fresh ID and a
// correlation id (inherited from parent if present, generated
// otherwise). Returns an error only when crypto/rand is unavailable.
func NewComposedOp(parent context.Context) (*ComposedOp, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	correlation, ok := CorrelationID(parent)
	if !ok || correlation == "" {
		correlation, err = newID()
		if err != nil {
			return nil, err
		}
	}
	return &ComposedOp{ID: id, Correlation: correlation}, nil
}

// Run executes fn as the next leg of op, deriving an idempotency
// key of the form `{op.ID}/{legIndex}` and threading the
// correlation id into ctx. fn receives the enriched context and
// returns its own result.
//
// Each call advances op's leg counter by one; concurrent calls to
// Run on the same ComposedOp are not supported because legs are
// sequential by definition.
func Run[T any](ctx context.Context, op *ComposedOp, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if op == nil {
		return zero, ErrNilComposedOp
	}
	if fn == nil {
		return zero, errors.New("ergo: Run requires a non-nil fn")
	}
	key := op.ID + "/" + strconv.Itoa(op.leg)
	op.leg++
	ctx = WithIdempotencyKey(ctx, key)
	ctx = WithCorrelationID(ctx, op.Correlation)
	return fn(ctx)
}
