package hedgev2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/Pinguteca/sdk-core-go/experimental/hedgev2"
)

var (
	hedgedSpec = connect.Spec{
		Procedure:        "/test.v1.EchoService/Echo",
		StreamType:       connect.StreamTypeUnary,
		IdempotencyLevel: connect.IdempotencyNoSideEffects,
	}
	unsafeSpec = connect.Spec{
		Procedure:  "/test.v1.EchoService/Write",
		StreamType: connect.StreamTypeUnary,
	}
)

// handlerFunc decides how the server treats the nth request (1-based).
type handlerFunc func(ctx context.Context, n int64) error

// authInterceptor writes a request header on every call it sees. Inside
// a hedge fan-out it runs once per attempt, which is the composition that
// races on shared state under connect-go v1.
func authInterceptor(next connect.ClientFunc) connect.ClientFunc {
	return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
		if info, ok := connect.CallInfoForClientContext(ctx); ok {
			info.RequestHeader().Set("Authorization", "Bearer token")
		}
		return next(ctx, spec)
	}
}

func newClient(t *testing.T, cfg hedgev2.Config, behave handlerFunc) (*connect.Client, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	handler := func(ctx context.Context, _ connect.Spec, stream connect.ServerStream) error {
		n := calls.Add(1)
		req := &wrapperspb.StringValue{}
		if err := stream.Receive(req); err != nil {
			return err
		}
		if err := behave(ctx, n); err != nil {
			return err
		}
		info, _ := connect.CallInfoForServerContext(ctx)
		info.ResponseHeader().Set("X-Attempt", strconv.FormatInt(n, 10))
		return stream.Send(wrapperspb.String(req.GetValue() + "|" + info.RequestHeader().Get("Authorization")))
	}

	srv := connect.NewServer()
	srv.Register(
		connect.Method{Spec: hedgedSpec, Handler: handler},
		connect.Method{Spec: unsafeSpec, Handler: handler},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	transport := connecthttp.NewTransport(hs.Client(), hs.URL)
	return connect.NewClient(transport, hedgev2.ClientInterceptor(cfg), authInterceptor), &calls
}

func sleepOrCancel(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestHedge_HedgeWinsWhilePrimaryStalls(t *testing.T) {
	client, calls := newClient(t, hedgev2.Config{MaxAttempts: 3, Delay: 10 * time.Millisecond},
		func(ctx context.Context, n int64) error {
			if n == 1 {
				return sleepOrCancel(ctx, 5*time.Second)
			}
			return nil
		})

	ctx, info := connect.NewClientContext(context.Background())
	resp := &wrapperspb.StringValue{}
	start := time.Now()
	if err := client.CallUnary(ctx, hedgedSpec, wrapperspb.String("ping"), resp); err != nil {
		t.Fatalf("CallUnary: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v; the stalled primary should have lost the race", elapsed)
	}
	if got, want := resp.GetValue(), "ping|Bearer token"; got != want {
		t.Errorf("response = %q, want %q", got, want)
	}
	if calls.Load() < 2 {
		t.Errorf("server saw %d attempts, want at least 2", calls.Load())
	}
	if got := info.ResponseHeader().Get("X-Attempt"); got == "" || got == "1" {
		t.Errorf("caller CallInfo X-Attempt = %q, want the winning hedge attempt", got)
	}
}

// Many concurrent hedged calls, each fanning out to every attempt, with a
// header-writing interceptor inside the fan-out. The point of this test is
// what -race reports, not the assertions.
func TestHedge_ConcurrentFanOutUnderRace(t *testing.T) {
	client, _ := newClient(t, hedgev2.Config{MaxAttempts: 3, Delay: time.Millisecond},
		func(ctx context.Context, _ int64) error {
			return sleepOrCancel(ctx, 20*time.Millisecond)
		})

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Go(func() {
			resp := &wrapperspb.StringValue{}
			want := "call-" + strconv.Itoa(i)
			if err := client.CallUnary(context.Background(), hedgedSpec, wrapperspb.String(want), resp); err != nil {
				errs <- err
				return
			}
			if resp.GetValue() != want+"|Bearer token" {
				t.Errorf("call %d got %q", i, resp.GetValue())
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("CallUnary: %v", err)
	}
}

func TestHedge_SkipsMethodsWithoutNoSideEffects(t *testing.T) {
	client, calls := newClient(t, hedgev2.Config{MaxAttempts: 3, Delay: time.Millisecond},
		func(ctx context.Context, _ int64) error {
			return sleepOrCancel(ctx, 50*time.Millisecond)
		})

	resp := &wrapperspb.StringValue{}
	if err := client.CallUnary(context.Background(), unsafeSpec, wrapperspb.String("w"), resp); err != nil {
		t.Fatalf("CallUnary: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d attempts for an unsafe method, want 1", got)
	}
}

func TestHedge_ReturnsLastErrorWhenEveryAttemptFails(t *testing.T) {
	client, calls := newClient(t, hedgev2.Config{MaxAttempts: 3, Delay: time.Millisecond},
		func(context.Context, int64) error {
			return connect.Errorf(connect.CodeUnavailable, "down")
		})

	err := client.CallUnary(context.Background(), hedgedSpec, wrapperspb.String("x"), &wrapperspb.StringValue{})
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Fatalf("code = %v (err %v), want Unavailable", got, err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("server saw %d attempts, want 3", got)
	}
}
