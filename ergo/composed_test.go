package ergo

import (
	"context"
	"errors"
	"testing"
)

func TestNewComposedOp_GeneratesUniqueIDs(t *testing.T) {
	a, err := NewComposedOp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewComposedOp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Errorf("expected distinct op ids, got %q twice", a.ID)
	}
	if a.Correlation == b.Correlation {
		t.Errorf("expected distinct correlation ids, got %q twice", a.Correlation)
	}
}

func TestNewComposedOp_InheritsCorrelation(t *testing.T) {
	parent := WithCorrelationID(context.Background(), "trace-42")
	op, err := NewComposedOp(parent)
	if err != nil {
		t.Fatal(err)
	}
	if op.Correlation != "trace-42" {
		t.Errorf("expected inherited correlation, got %q", op.Correlation)
	}
}

func TestRun_DerivesPerLegKeys(t *testing.T) {
	op, _ := NewComposedOp(context.Background())
	var seen []string
	for range 3 {
		_, _ = Run(context.Background(), op, func(ctx context.Context) (struct{}, error) {
			key, ok := IdempotencyKey(ctx)
			if !ok {
				t.Fatal("expected idempotency key in ctx")
			}
			seen = append(seen, key)
			return struct{}{}, nil
		})
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(seen))
	}
	for i, key := range seen {
		want := op.ID + "/" + itoa(i)
		if key != want {
			t.Errorf("leg %d key = %q, want %q", i, key, want)
		}
	}
}

func TestRun_PropagatesCorrelation(t *testing.T) {
	op, _ := NewComposedOp(context.Background())
	_, _ = Run(context.Background(), op, func(ctx context.Context) (struct{}, error) {
		got, ok := CorrelationID(ctx)
		if !ok || got != op.Correlation {
			t.Errorf("got correlation (%q, %v), want (%q, true)", got, ok, op.Correlation)
		}
		return struct{}{}, nil
	})
}

func TestRun_PropagatesFnError(t *testing.T) {
	op, _ := NewComposedOp(context.Background())
	sentinel := errors.New("boom")
	_, err := Run(context.Background(), op, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got %v", err)
	}
}

func TestRun_NilOpReturnsError(t *testing.T) {
	_, err := Run(context.Background(), (*ComposedOp)(nil), func(ctx context.Context) (struct{}, error) {
		return struct{}{}, nil
	})
	if !errors.Is(err, ErrNilComposedOp) {
		t.Errorf("expected ErrNilComposedOp, got %v", err)
	}
}

// itoa is a local helper to avoid a strconv import in the test
// (the tested code uses strconv internally).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
