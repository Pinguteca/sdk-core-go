package ergo

import "context"

// Context keys for idempotency-key and correlation-id propagation.
// Unexported so the consumer cannot collide; exposed via the
// Get/With helpers.
type ctxKey int

const (
	idempotencyKey ctxKey = iota
	correlationKey
)

// WithIdempotencyKey returns a copy of ctx carrying key. The Layer 2
// idempotency interceptor reads the key from this slot and attaches
// it to the outgoing call's metadata.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, idempotencyKey, key)
}

// IdempotencyKey returns the key carried by ctx, if any. The second
// return is false when no key is set.
func IdempotencyKey(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(idempotencyKey).(string)
	return v, ok && v != ""
}

// WithCorrelationID returns a copy of ctx carrying id. The logging
// companion's caller-attr hook reads this slot and emits it as the
// `correlation.id` attribute on every record.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationKey, id)
}

// CorrelationID returns the id carried by ctx, if any. The second
// return is false when no id is set.
func CorrelationID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(correlationKey).(string)
	return v, ok && v != ""
}
