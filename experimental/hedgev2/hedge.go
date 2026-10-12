// Package hedgev2 is an unreleased prototype of the hedge interceptor
// written against connect-go v2. It exists to answer one question before
// RFC 0022 is settled: whether v2's interceptor model lets hedge attempts
// run concurrently without sharing mutable per-call state.
//
// In v2 an interceptor wraps the opening of a stream, request metadata
// lives on a per-call [connect.CallInfo] carried by the context, and
// [connect.NewClientContext] attaches a fresh one. Each attempt here gets
// its own CallInfo, its own copy of the request headers and its own copy
// of the request message, so nothing mutable is shared between attempts.
//
// This module is not listed in cog.toml and is never tagged or published.
package hedgev2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
)

// Default tuning values for [DefaultConfig].
const (
	DefaultMaxAttempts = 3
	DefaultDelay       = 50 * time.Millisecond
)

// errNoResult is returned defensively if every attempt reported without
// producing either a response or an error.
var errNoResult = errors.New("hedge: no result")

// Config tunes hedge behaviour. Field semantics match the v1 hedge
// package so a port is a wiring change.
type Config struct {
	Clock           sdkclock.Clock
	Delay           time.Duration
	MaxAttempts     int
	HedgeIdempotent bool
}

// DefaultConfig returns 3 attempts, 50ms apart, hedging only
// NO_SIDE_EFFECTS methods.
func DefaultConfig() Config {
	return Config{MaxAttempts: DefaultMaxAttempts, Delay: DefaultDelay}
}

// ClientInterceptor returns the hedge interceptor.
func ClientInterceptor(cfg Config) connect.ClientInterceptor {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if cfg.Delay < 0 {
		cfg.Delay = 0
	}
	if cfg.Clock == nil {
		cfg.Clock = sdkclock.Real()
	}
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if !eligible(cfg, spec) {
				stream, err := next(ctx, spec)
				if err != nil {
					return nil, fmt.Errorf("hedge: open stream: %w", err)
				}
				return stream, nil
			}
			return &hedgedStream{ctx: ctx, spec: spec, next: next, cfg: cfg}, nil
		}
	}
}

func eligible(cfg Config, spec connect.Spec) bool {
	if cfg.MaxAttempts <= 1 || spec.StreamType != connect.StreamTypeUnary {
		return false
	}
	switch spec.IdempotencyLevel {
	case connect.IdempotencyNoSideEffects:
		return true
	case connect.IdempotencyIdempotent:
		return cfg.HedgeIdempotent
	default:
		return false
	}
}

// hedgedStream defers opening any attempt until the response is read,
// because the request message is only known after Send. Unary callers
// drive it as Send, CloseSend, Receive.
type hedgedStream struct {
	ctx      context.Context //nolint:containedctx // attempts launch lazily in Receive; lives for one call
	next     connect.ClientFunc
	req      proto.Message
	cancel   context.CancelFunc
	fallback connect.ClientStream
	spec     connect.Spec
	cfg      Config
	mu       sync.Mutex
	received bool
}

func (s *hedgedStream) SendHeaders() error { return nil }

func (s *hedgedStream) Send(msg any) error {
	if s.fallback != nil {
		return wrapStreamErr("send", s.fallback.Send(msg))
	}
	pm, ok := msg.(proto.Message)
	if !ok {
		// A non-protobuf codec gives no way to copy the message per
		// attempt, so run a single unhedged attempt instead.
		stream, err := s.next(s.ctx, s.spec)
		if err != nil {
			return fmt.Errorf("hedge: open stream: %w", err)
		}
		s.fallback = stream
		return wrapStreamErr("send", stream.Send(msg))
	}
	s.req = proto.Clone(pm)
	return nil
}

func (s *hedgedStream) CloseSend() error {
	if s.fallback != nil {
		return wrapStreamErr("close send", s.fallback.CloseSend())
	}
	return nil
}

func (s *hedgedStream) Receive(msg any) error {
	if s.fallback != nil {
		return wrapStreamErr("receive", s.fallback.Receive(msg))
	}
	if s.received {
		return io.EOF
	}
	s.received = true
	out, ok := msg.(proto.Message)
	if !ok || s.req == nil {
		return connect.Errorf(connect.CodeInternal, "hedge: receive before a protobuf send")
	}
	return s.race(out)
}

func (s *hedgedStream) Close() error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if s.fallback != nil {
		return wrapStreamErr("close", s.fallback.Close())
	}
	return nil
}

type result struct {
	info *connect.CallInfo
	resp proto.Message
	err  error
}

// race launches attempts on the configured stagger and returns the first
// success. Losers are cancelled when race returns.
func (s *hedgedStream) race(out proto.Message) error {
	raceCtx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	parent, _ := connect.CallInfoForClientContext(s.ctx)
	results := make(chan result, s.cfg.MaxAttempts)
	launch := func() { s.launch(raceCtx, parent, out, results) }

	st := raceState{limit: s.cfg.MaxAttempts}
	st.fire(launch)
	for st.pending() {
		timer := s.cfg.Clock.NewTimer(s.cfg.Delay)
		select {
		case r := <-results:
			timer.Stop()
			if st.record(r, launch) {
				adopt(parent, r, out)
				return nil
			}
		case <-timer.C():
			st.fire(launch)
		case <-s.ctx.Done():
			timer.Stop()
			return fmt.Errorf("hedge: %w", s.ctx.Err())
		}
	}
	return st.final()
}

// raceState counts attempts across iterations of [hedgedStream.race].
type raceState struct {
	lastErr   error
	launched  int
	completed int
	limit     int
}

func (st *raceState) pending() bool {
	return st.completed < st.launched || st.launched < st.limit
}

// fire launches one more attempt if the limit allows.
func (st *raceState) fire(launch func()) {
	if st.launched < st.limit {
		launch()
		st.launched++
	}
}

// record reports whether r is the winner. A failure with nothing else in
// flight launches the next attempt immediately rather than waiting out
// the stagger.
func (st *raceState) record(r result, launch func()) bool {
	st.completed++
	if r.err == nil {
		return true
	}
	st.lastErr = r.err
	if st.completed == st.launched {
		st.fire(launch)
	}
	return false
}

func (st *raceState) final() error {
	if st.lastErr == nil {
		return errNoResult
	}
	return st.lastErr
}

// launch prepares one attempt's isolated state on the caller goroutine,
// then runs the attempt on its own goroutine.
func (s *hedgedStream) launch(ctx context.Context, parent *connect.CallInfo, out proto.Message, results chan<- result) {
	attemptCtx, info := connect.NewClientContext(ctx)
	if parent != nil {
		copyHeader(info.RequestHeader(), parent.RequestHeader())
	}
	req := proto.Clone(s.req)
	resp := out.ProtoReflect().New().Interface()
	go func() {
		results <- result{info: info, resp: resp, err: s.attempt(attemptCtx, req, resp)}
	}()
}

func (s *hedgedStream) attempt(ctx context.Context, req, resp proto.Message) (err error) {
	stream, err := s.next(ctx, s.spec)
	if err != nil {
		return fmt.Errorf("hedge: open attempt: %w", err)
	}
	defer func() { err = errors.Join(err, wrapStreamErr("close attempt", stream.Close())) }()
	if sendErr := stream.Send(req); sendErr != nil && !errors.Is(sendErr, io.EOF) {
		return wrapStreamErr("send attempt", sendErr)
	}
	if closeErr := stream.CloseSend(); closeErr != nil {
		return wrapStreamErr("close send attempt", closeErr)
	}
	return wrapStreamErr("receive attempt", stream.Receive(resp))
}

// adopt copies the winning attempt's response and call metadata onto the
// caller's message and CallInfo.
func adopt(parent *connect.CallInfo, r result, out proto.Message) {
	proto.Reset(out)
	proto.Merge(out, r.resp)
	if parent == nil {
		return
	}
	parent.Spec = r.info.Spec
	parent.PeerAddr = r.info.PeerAddr
	parent.Protocol = r.info.Protocol
	parent.Codec = r.info.Codec
	parent.RequestEncoding = r.info.RequestEncoding
	parent.ResponseEncoding = r.info.ResponseEncoding
	parent.SendStats = r.info.SendStats
	parent.ReceiveStats = r.info.ReceiveStats
	parent.TransportInfo = r.info.TransportInfo
	copyHeader(parent.ResponseHeader(), r.info.ResponseHeader())
	copyHeader(parent.ResponseTrailer(), r.info.ResponseTrailer())
}

func copyHeader(dst, src *connect.Header) {
	for k, v := range src.All() {
		dst.SetValues(k, slices.Clone(v))
	}
}

func wrapStreamErr(op string, err error) error {
	// io.EOF stays unwrapped: stream callers compare against it directly.
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	return fmt.Errorf("hedge: %s: %w", op, err)
}
