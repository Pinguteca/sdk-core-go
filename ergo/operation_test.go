package ergo

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperation_Wait_ReturnsResultOnDone(t *testing.T) {
	var calls atomic.Int32
	op := &Operation[string]{
		Poll: func(ctx context.Context) (OperationStatus[string], error) {
			calls.Add(1)
			return OperationStatus[string]{Done: true, Result: "ok"}, nil
		},
		InitialDelay: time.Millisecond,
		Sleep:        instantSleep,
	}
	got, err := op.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Errorf("got %q, want ok", got)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 poll, got %d", calls.Load())
	}
}

func TestOperation_Wait_PollsUntilDone(t *testing.T) {
	var calls atomic.Int32
	op := &Operation[int]{
		Poll: func(ctx context.Context) (OperationStatus[int], error) {
			n := calls.Add(1)
			if n < 3 {
				return OperationStatus[int]{Done: false}, nil
			}
			return OperationStatus[int]{Done: true, Result: int(n)}, nil
		},
		InitialDelay: time.Millisecond,
		MaxDelay:     time.Millisecond * 10,
		Sleep:        instantSleep,
	}
	got, err := op.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 polls, got %d", calls.Load())
	}
}

func TestOperation_Wait_RespectsContextDeadline(t *testing.T) {
	op := &Operation[string]{
		Poll: func(ctx context.Context) (OperationStatus[string], error) {
			return OperationStatus[string]{Done: false}, nil
		},
		InitialDelay: time.Hour,
		Sleep: func(ctx context.Context, d time.Duration) error {
			// Honour ctx.Done immediately so the test does not actually wait.
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return ctx.Err()
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := op.Wait(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestOperation_Wait_PropagatesPollError(t *testing.T) {
	sentinel := errors.New("server boom")
	op := &Operation[string]{
		Poll: func(ctx context.Context) (OperationStatus[string], error) {
			return OperationStatus[string]{}, sentinel
		},
		Sleep: instantSleep,
	}
	_, err := op.Wait(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got %v", err)
	}
}

func TestOperation_Wait_PropagatesTerminalErr(t *testing.T) {
	terminal := errors.New("operation failed")
	op := &Operation[string]{
		Poll: func(ctx context.Context) (OperationStatus[string], error) {
			return OperationStatus[string]{Done: true, Err: terminal, Result: "partial"}, nil
		},
		Sleep: instantSleep,
	}
	got, err := op.Wait(context.Background())
	if !errors.Is(err, terminal) {
		t.Errorf("expected terminal, got %v", err)
	}
	if got != "partial" {
		t.Errorf("expected partial result returned even on terminal err, got %q", got)
	}
}

func TestOperation_Wait_HonoursRetryAfterHint(t *testing.T) {
	var observedDelays []time.Duration
	var calls atomic.Int32
	op := &Operation[string]{
		Poll: func(ctx context.Context) (OperationStatus[string], error) {
			n := calls.Add(1)
			if n == 1 {
				return OperationStatus[string]{
					Done:       false,
					RetryAfter: 250 * time.Millisecond,
				}, nil
			}
			return OperationStatus[string]{Done: true, Result: "ok"}, nil
		},
		InitialDelay: time.Hour, // ignored because RetryAfter overrides
		Sleep: func(ctx context.Context, d time.Duration) error {
			observedDelays = append(observedDelays, d)
			return nil
		},
	}
	_, err := op.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(observedDelays) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(observedDelays))
	}
	if observedDelays[0] != 250*time.Millisecond {
		t.Errorf("expected server hint honoured (250ms), got %v", observedDelays[0])
	}
}

func TestOperation_Wait_NilOperationErrors(t *testing.T) {
	_, err := (*Operation[string])(nil).Wait(context.Background())
	if !errors.Is(err, ErrNoPoll) {
		t.Errorf("expected ErrNoPoll, got %v", err)
	}
}

func TestOperation_Wait_NoPollErrors(t *testing.T) {
	op := &Operation[string]{}
	_, err := op.Wait(context.Background())
	if !errors.Is(err, ErrNoPoll) {
		t.Errorf("expected ErrNoPoll, got %v", err)
	}
}

func instantSleep(ctx context.Context, _ time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
