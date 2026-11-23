package executioncontroller

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/port"
)

// DefaultPolicyRecheckInterval bounds how long a forwarded call outlives a
// withdrawal this replica did not perform itself.
const DefaultPolicyRecheckInterval = 10 * time.Second

// maxUnconfirmedRechecks is how many consecutive rechecks a call survives when
// authorization state cannot be read, such as during an API server outage.
const maxUnconfirmedRechecks = 1

// withdrawal is the cancellation cause of a forwarded call whose authority ended.
type withdrawal struct{ err error }

func (w *withdrawal) Error() string { return w.err.Error() }
func (w *withdrawal) Unwrap() error { return w.err }

type flight struct {
	proxy      *executorProxy
	ctx        context.Context
	rc         *executorv1.RequestContext
	key        string
	generation uint64
	withdraw   context.CancelCauseFunc
	missed     int
}

// inflightCalls indexes forwarded calls by environment ID.
type inflightCalls struct {
	mu       sync.Mutex
	byEnv    map[string]map[*flight]struct{}
	sweeping bool
}

func newInflightCalls() *inflightCalls {
	return &inflightCalls{byEnv: map[string]map[*flight]struct{}{}}
}

// add registers f and reports whether a recheck sweep must start for it.
func (r *inflightCalls) add(env string, f *flight) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byEnv[env] == nil {
		r.byEnv[env] = map[*flight]struct{}{}
	}
	r.byEnv[env][f] = struct{}{}
	start := !r.sweeping
	r.sweeping = true
	return start
}

func (r *inflightCalls) remove(env string, f *flight) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byEnv[env], f)
	if len(r.byEnv[env]) == 0 {
		delete(r.byEnv, env)
	}
}

// snapshot returns the calls by environment, or nil once none remain, which
// ends the sweep.
func (r *inflightCalls) snapshot() map[string][]*flight {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byEnv) == 0 {
		r.sweeping = false
		return nil
	}
	out := make(map[string][]*flight, len(r.byEnv))
	for env, calls := range r.byEnv {
		for f := range calls {
			out[env] = append(out[env], f)
		}
	}
	return out
}

// withdrawBelow closes env's calls admitted under a grant generation before generation.
func (r *inflightCalls) withdrawBelow(env string, generation uint64, err error) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	closed := 0
	for f := range r.byEnv[env] {
		if f.generation < generation {
			f.withdraw(&withdrawal{err: err})
			closed++
		}
	}
	return closed
}

func intervalTicks(interval time.Duration) func() (<-chan time.Time, func()) {
	return func() (<-chan time.Time, func()) {
		t := time.NewTicker(interval)
		return t.C, t.Stop
	}
}

// withdrawBelow closes calls under a revoked grant generation with the refusal
// a new call under it receives.
func (h *Handler) withdrawBelow(ctx context.Context, env string, generation uint64) {
	err := backendError(errRunClaimNotCurrent)
	if h.flights.withdrawBelow(env, generation, err) > 0 {
		h.logWithdrawal(ctx, "revocation", err)
	}
}

func (h *Handler) logWithdrawal(ctx context.Context, trigger string, err error) {
	if h.cfg.Diagnostics == nil {
		return
	}
	h.cfg.Diagnostics.Log(
		ctx,
		port.LevelInfo,
		"forwarded executor call withdrawn",
		"trigger", trigger,
		"code", status.Code(err).String(),
	)
}

// track keeps a forwarded call open only while rc would still be admitted:
// the returned context ends on revocation here or a refused recheck.
func (p *executorProxy) track(
	ctx context.Context,
	rc *executorv1.RequestContext,
) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	env := rc.GetEnvironment().GetId()
	f := &flight{
		proxy:      p,
		ctx:        ctx,
		rc:         rc,
		key:        recheckKey(ctx, p.capability, rc),
		generation: rc.GetGrantGeneration(),
		withdraw:   cancel,
	}
	if p.handler.flights.add(env, f) {
		go p.handler.sweep()
	}
	return ctx, func() {
		p.handler.flights.remove(env, f)
		cancel(nil)
	}
}

// recheckKey groups calls whose authorization one check decides.
func recheckKey(ctx context.Context, capability string, rc *executorv1.RequestContext) string {
	fields := [][]byte{[]byte(capability)}
	if chain, _, err := clientCertificates(ctx); err == nil {
		for _, cert := range chain {
			fields = append(fields, cert.Raw)
		}
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(rc)
	var key []byte
	for _, field := range append(fields, b) {
		key = binary.AppendUvarint(key, uint64(len(field)))
		key = append(key, field...)
	}
	return string(key)
}

// sweep rechecks every forwarded call on each tick until none remain.
func (h *Handler) sweep() {
	ticks, stop := h.recheckTicks()
	defer stop()
	for range ticks {
		calls := h.flights.snapshot()
		if calls != nil {
			reads := &recheckReads{reads: map[any]func() (any, error){}}
			var wg sync.WaitGroup
			for _, flights := range calls {
				wg.Go(func() { h.recheckEnvironment(reads, flights) })
			}
			wg.Wait()
		}
		if h.swept != nil {
			h.swept()
		}
		if calls == nil {
			return
		}
	}
}

func (h *Handler) recheckEnvironment(reads *recheckReads, flights []*flight) {
	groups := map[string][]*flight{}
	for _, f := range flights {
		if f.ctx.Err() == nil {
			groups[f.key] = append(groups[f.key], f)
		}
	}
	for _, group := range groups {
		probe := group[0]
		ctx := context.WithValue(context.WithoutCancel(probe.ctx), recheckReadsKey{}, reads)
		ctx, cancel := context.WithTimeout(ctx, h.cfg.PolicyRecheckInterval)
		_, transient, err := probe.proxy.admit(ctx, probe.rc)
		cancel()
		for _, f := range group {
			switch {
			case err == nil:
				f.missed = 0
			case transient && f.missed < maxUnconfirmedRechecks:
				f.missed++
			default:
				f.withdraw(&withdrawal{err: err})
				h.logWithdrawal(f.ctx, "recheck", err)
			}
		}
	}
}

type recheckReadsKey struct{}

// recheckReads shares one sweep's reads of each environment and of the
// security authority across every check in it.
type recheckReads struct {
	mu    sync.Mutex
	reads map[any]func() (any, error)
}

// recheckRead runs read once per key within a recheck sweep, and every time otherwise.
func recheckRead[T any](ctx context.Context, key any, read func() (T, error)) (T, error) {
	r, _ := ctx.Value(recheckReadsKey{}).(*recheckReads)
	if r == nil {
		return read()
	}
	r.mu.Lock()
	once, ok := r.reads[key]
	if !ok {
		once = sync.OnceValues(func() (any, error) { return read() })
		r.reads[key] = once
	}
	r.mu.Unlock()
	v, err := once()
	return v.(T), err
}

// withdrawnOr reports a failed call's withdrawal in place of the cancellation it caused.
func withdrawnOr(ctx context.Context, err error) error {
	var w *withdrawal
	if err != nil && errors.As(context.Cause(ctx), &w) {
		return w.err
	}
	return err
}
