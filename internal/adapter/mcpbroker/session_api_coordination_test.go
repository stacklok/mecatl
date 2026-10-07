package mcpbroker

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"golang.org/x/oauth2"
)

// Fixtures seed records through the same gate and publication barrier as the API.
func saveAPIRecord(t *testing.T, api *SessionAPI, ctx context.Context, st *apiState) error {
	t.Helper()
	ctx, release, err := api.operation(ctx, st.record.Ref, apiControl{})
	if err != nil {
		return err
	}
	defer release()
	return api.saveRecord(ctx, st, st.record)
}

type coordinationDiscovery struct {
	capabilityQuerier
	entered chan context.Context
	release chan struct{}
}

func (d *coordinationDiscovery) QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	d.entered <- ctx
	<-d.release
	return d.capabilityQuerier.QueryCapabilities(ctx, backend)
}

func TestSessionAPICoordinationRecoveryDiscoveryAndLatePublish(t *testing.T) {
	api, f, ctx, record := seedCleanupConnection(t)
	other, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	block := &coordinationDiscovery{capabilityQuerier: f.process.discovery.capabilities, entered: make(chan context.Context, 1), release: make(chan struct{})}
	defer func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	}()
	f.process.discovery.capabilities = block
	recovered := make(chan error, 1)
	go func() { _, err := api.OpenSession(ctx, &record.Ref); recovered <- err }()
	op := awaitCoordination(t, block.entered)
	progress := make(chan error, 1)
	go func() { _, err := api.OpenSession(ctx, &other.Ref); progress <- err }()
	if err := awaitCoordination(t, progress); err != nil {
		t.Fatal(err)
	}
	disconnected := make(chan error, 1)
	go func() {
		_, err := api.DisconnectTools(ctx, record.Ref, c.ConnectionRef(record.Connection))
		disconnected <- err
	}()
	awaitCoordination(t, op.Done())
	select {
	case <-disconnected:
		t.Fatal("cleanup passed native recovery ownership")
	default:
	}
	close(block.release)
	if err := awaitCoordination(t, recovered); err == nil {
		t.Fatal("late recovery published catalogue")
	}
	if err := awaitCoordination(t, disconnected); err != nil {
		t.Fatal(err)
	}
	snapshot, err := api.OpenSession(ctx, &record.Ref)
	if err != nil || len(snapshot.Catalogue.Tools()) != 0 {
		t.Fatalf("recovery resurrected authority: %#v %v", snapshot, err)
	}
}

func TestSessionAPICoordinationRecoveredRefreshCancellation(t *testing.T) {
	api, _, ctx, record := seedCleanupConnection(t)
	recovered, err := api.OpenSession(ctx, &record.Ref)
	if err != nil {
		t.Fatal(err)
	}
	other, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := api.states[record.Ref].attachment.logical.recoveredSource
	source.mu.Lock()
	source.token = nil
	source.mu.Unlock()
	entered := make(chan context.Context, 1)
	source.issue = func(ctx context.Context) (*oauth2.Token, error) { entered <- ctx; <-ctx.Done(); return nil, ctx.Err() }
	attempt := session.NewBrokerAttempt()
	call := c.Call{ID: "refresh", Name: "mcp__private__status", Arguments: []byte(`{}`)}
	prepared := make(chan error, 1)
	go func() {
		_, err := api.CheckAuthorization(ctx, record.Ref, recovered.Catalogue.Ref(), &call, "", attempt)
		prepared <- err
	}()
	op := awaitCoordination(t, entered)
	progress := make(chan error, 1)
	go func() { _, err := api.OpenSession(ctx, &other.Ref); progress <- err }()
	if err := awaitCoordination(t, progress); err != nil {
		t.Fatal(err)
	}
	disconnected := make(chan error, 1)
	go func() {
		_, err := api.DisconnectTools(ctx, record.Ref, recovered.Catalogue.Connection())
		disconnected <- err
	}()
	awaitCoordination(t, op.Done())
	if err := awaitCoordination(t, prepared); err == nil {
		t.Fatal("cancelled refresh published readiness")
	}
	if err := awaitCoordination(t, disconnected); err != nil {
		t.Fatal(err)
	}
}

type coordinationLoads struct {
	redis.UniversalClient
	entered chan struct{}
	release chan struct{}
}

func (r *coordinationLoads) Options() *redis.Options {
	return r.UniversalClient.(interface{ Options() *redis.Options }).Options()
}

func (r *coordinationLoads) Get(ctx context.Context, key string) *redis.StringCmd {
	r.entered <- struct{}{}
	<-r.release
	return r.UniversalClient.Get(ctx, key)
}

func TestSessionAPICoordinationPendingCapacity(t *testing.T) {
	api, _, ctx := reviewAPI(t)
	opened, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := api.states[opened.Ref].record
	refs := make([]c.SessionRef, 33)
	for i := range refs {
		record := base
		record.Ref = c.SessionRef(apiRef())
		refs[i] = record.Ref
		b, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := api.redis.Set(ctx, sessionAPIPrefix+string(record.Ref), b, time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
	}
	block := &coordinationLoads{UniversalClient: api.redis, entered: make(chan struct{}, 32), release: make(chan struct{})}
	defer func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	}()
	cold, err := NewSessionAPI(api.process, block, api.workload)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cold.Close() })
	done := make(chan error, 32)
	for _, ref := range refs[:32] {
		go func() { _, err := cold.OpenSession(ctx, &ref); done <- err }()
	}
	for range 32 {
		awaitCoordination(t, block.entered)
	}
	if _, err := cold.OpenSession(ctx, &refs[32]); err != c.ErrCapacity {
		t.Fatalf("pending loads bypassed capacity: %v", err)
	}
	cold.mu.Lock()
	count := len(cold.states)
	cold.mu.Unlock()
	if count != 32 {
		t.Fatalf("loading registry: %d", count)
	}
	close(block.release)
	for range 32 {
		if err := awaitCoordination(t, done); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionAPICoordinationWorkersAcrossSessions(t *testing.T) {
	started, release := make(chan struct{}, 2), make(chan struct{})
	defer close(release)
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		started <- struct{}{}
		<-release
		return slotResult(), nil
	})
	other, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := api.BeginEnrollment(ctx, other.Ref)
	if err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan c.InvocationOutcome, 2)
	for _, snapshot := range []c.SessionSnapshot{{Ref: opened.Ref, Catalogue: cat}, {Ref: other.Ref, Catalogue: enrolled.Catalogue}} {
		go func() {
			out, _ := api.InvokeTool(caller, snapshot.Ref, snapshot.Catalogue.Ref(), c.Call{ID: "parallel", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}, session.NewBrokerAttempt())
			done <- out
		}()
	}
	for range 2 {
		awaitCoordination(t, started)
	}
	cancel()
	for range 2 {
		if out := awaitCoordination(t, done); out.Kind != c.InvocationOutcomeUnknown {
			t.Fatalf("caller cancellation: %#v", out)
		}
	}
}

func TestSessionAPICoordinationDelayedWriteDeleteWins(t *testing.T) {
	api, ctx, opened, _ := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return slotResult(), nil
	})
	block := &coordinationRedis{UniversalClient: api.redis, key: sessionAPIPrefix + string(opened.Ref), set: true, entered: make(chan context.Context, 1), release: make(chan struct{})}
	defer func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	}()
	if _, err := api.DisconnectTools(ctx, opened.Ref, api.states[opened.Ref].catalogue.Connection()); err != nil {
		t.Fatal(err)
	}
	block.armed.Store(true)
	api.redis = block
	prepared := make(chan error, 1)
	go func() {
		_, err := api.BeginEnrollment(ctx, opened.Ref)
		prepared <- err
	}()
	op := awaitCoordination(t, block.entered)
	deleted := make(chan error, 1)
	go func() { _, err := api.DeleteSession(ctx, opened.Ref); deleted <- err }()
	awaitCoordination(t, op.Done())
	select {
	case <-deleted:
		t.Fatal("DEL passed outstanding SET")
	default:
	}
	close(block.release)
	if err := awaitCoordination(t, prepared); err == nil {
		t.Fatal("deleted preparation published")
	}
	if err := awaitCoordination(t, deleted); err != nil {
		t.Fatal(err)
	}
	if err := block.UniversalClient.Get(ctx, block.key).Err(); err != redis.Nil {
		t.Fatalf("late SET resurrected deleted record: %v", err)
	}
}

func TestSessionAPICoordinationAmbiguityBlocksDelete(t *testing.T) {
	api, _, ctx, record := seedCleanupConnection(t)
	fault := &cleanupAckRedis{UniversalClient: api.redis, armed: true}
	api.redis = fault
	if _, err := api.DisconnectTools(ctx, record.Ref, c.ConnectionRef(record.Connection)); err == nil {
		t.Fatal("ambiguous withdrawal reported success")
	}
	before, err := fault.UniversalClient.Get(ctx, sessionAPIPrefix+string(record.Ref)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.DeleteSession(ctx, record.Ref); err == nil {
		t.Fatal("unresolved write allowed deletion")
	}
	after, err := fault.UniversalClient.Get(ctx, sessionAPIPrefix+string(record.Ref)).Result()
	if err != nil || before != after {
		t.Fatalf("ambiguity allowed cleanup SET/DEL: %v", err)
	}
}

type coordinationRedis struct {
	redis.UniversalClient
	key     string
	set     bool
	armed   atomic.Bool
	entered chan context.Context
	release chan struct{}
}

func (r *coordinationRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	if !r.set && key == r.key && r.armed.CompareAndSwap(true, false) {
		r.entered <- ctx
		<-r.release
	}
	return r.UniversalClient.Get(ctx, key)
}

func (r *coordinationRedis) Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	if r.set && key == r.key && r.armed.CompareAndSwap(true, false) {
		r.entered <- ctx
		<-r.release
		// The transport can finish a write after local generation cancellation.
		return r.UniversalClient.Set(context.WithoutCancel(ctx), key, value, ttl)
	}
	return r.UniversalClient.Set(ctx, key, value, ttl)
}

func awaitCoordination[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("coordination did not progress")
	}
	var zero T
	return zero
}

func TestSessionAPICoordinationBlockedRedisAndCloseAdmission(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(map[bool]string{false: "other-session", true: "close-joins"}[closing], func(t *testing.T) {
			api, ctx, opened, _ := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				return slotResult(), nil
			})
			other, err := api.OpenSession(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			block := &coordinationRedis{UniversalClient: api.redis, key: sessionAPIPrefix + string(opened.Ref), entered: make(chan context.Context, 1), release: make(chan struct{})}
			block.armed.Store(true)
			defer func() {
				select {
				case <-block.release:
				default:
					close(block.release)
				}
			}()
			api.redis = block
			admission := make(chan error, 1)
			go func() { _, err := api.OpenSession(ctx, &opened.Ref); admission <- err }()
			op := awaitCoordination(t, block.entered)
			if !closing {
				progress := make(chan error, 1)
				go func() { _, err := api.OpenSession(ctx, &other.Ref); progress <- err }()
				if err := awaitCoordination(t, progress); err != nil {
					t.Fatal(err)
				}
				close(block.release)
				if err := awaitCoordination(t, admission); err != nil {
					t.Fatal(err)
				}
				return
			}
			closed := make(chan error, 1)
			go func() { closed <- api.Close() }()
			awaitCoordination(t, op.Done())
			select {
			case <-closed:
				t.Fatal("Close did not join admission I/O")
			default:
			}
			close(block.release)
			if err := awaitCoordination(t, admission); err == nil {
				t.Fatal("closed admission published")
			}
			if err := awaitCoordination(t, closed); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type coordinationAuthorization struct {
	tool.Tool
	entered chan context.Context
}

func (t *coordinationAuthorization) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}

func (t *coordinationAuthorization) RequestAuthorization(ctx context.Context, _ session.ToolCall) (session.ExternalAuthorization, bool, error) {
	t.entered <- ctx
	<-ctx.Done()
	return session.ExternalAuthorization{}, false, ctx.Err()
}

func TestSessionAPICoordinationWithdrawalInterruptsPreparation(t *testing.T) {
	var calls atomic.Int32
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return slotResult(), nil
	})
	st := api.states[opened.Ref]
	entered := make(chan context.Context, 1)
	tools := cat.Tools()
	for i, t := range tools {
		if t.Spec().Name == "mcp__slots__echo" {
			tools[i] = &coordinationAuthorization{Tool: t, entered: entered}
		}
	}
	var err error
	st.catalogue, err = c.NewCatalogue(cat.Ref(), cat.Connection(), tools)
	if err != nil {
		t.Fatal(err)
	}
	attempt := session.NewBrokerAttempt()
	call := c.Call{ID: "prepare", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}
	prepared := make(chan error, 1)
	go func() { _, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, attempt); prepared <- err }()
	op := awaitCoordination(t, entered)
	foreign := session.WithPrincipal(ctx, &session.Principal{Issuer: "https://owner.test", Subject: "foreign"})
	if _, err := api.DisconnectTools(foreign, opened.Ref, cat.Connection()); err == nil {
		t.Fatal("foreign cleanup accepted")
	}
	select {
	case <-op.Done():
		t.Fatal("foreign control cancelled preparation")
	default:
	}
	disconnected := make(chan error, 1)
	go func() { _, err := api.DisconnectTools(ctx, opened.Ref, cat.Connection()); disconnected <- err }()
	awaitCoordination(t, op.Done())
	if err := awaitCoordination(t, prepared); err == nil {
		t.Fatal("invalidated preparation published")
	}
	if err := awaitCoordination(t, disconnected); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || !receiptFinished(st.running) {
		t.Fatalf("preparation dispatched or retained worker: calls=%d", calls.Load())
	}
}

func TestSessionAPICoordinationDelayedWriteWithdrawalWins(t *testing.T) {
	api, _, ctx, record := seedCleanupConnection(t)
	opened := c.SessionSnapshot{Ref: record.Ref}
	block := &coordinationRedis{UniversalClient: api.redis, key: sessionAPIPrefix + string(opened.Ref), set: true, entered: make(chan context.Context, 1), release: make(chan struct{})}
	block.armed.Store(true)
	defer func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	}()
	api.redis = block
	prepared := make(chan error, 1)
	go func() {
		_, err := api.OpenSession(ctx, &opened.Ref)
		prepared <- err
	}()
	op := awaitCoordination(t, block.entered)
	disconnected := make(chan error, 1)
	go func() {
		_, err := api.DisconnectTools(ctx, opened.Ref, c.ConnectionRef(record.Connection))
		disconnected <- err
	}()
	awaitCoordination(t, op.Done())
	select {
	case <-disconnected:
		t.Fatal("cleanup passed unresolved SET")
	default:
	}
	close(block.release)
	if err := awaitCoordination(t, prepared); err == nil {
		t.Fatal("late admission published readiness")
	}
	if err := awaitCoordination(t, disconnected); err != nil {
		t.Fatal(err)
	}
	b, err := block.UniversalClient.Get(ctx, block.key).Bytes()
	var persisted apiRecord
	if err != nil || json.Unmarshal(b, &persisted) != nil || persisted.Connected || persisted.Withdrawing {
		t.Fatalf("late SET resurrected authority: %#v %v", persisted, err)
	}
}

type coordinationExecution struct {
	tool.Tool
	started chan struct{}
	release chan struct{}
}

func (t *coordinationExecution) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.started <- struct{}{}
	<-t.release
	return session.NewToolResult(call.ID, "ok"), nil
}

func TestSessionAPICoordinationInvalidatedWorkerReceipt(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return slotResult(), nil
	})
	tools := cat.Tools()
	for i, t := range tools {
		if t.Spec().Name == "mcp__slots__echo" {
			tools[i] = &coordinationExecution{Tool: t, started: started, release: release}
		}
	}
	var err error
	api.states[opened.Ref].catalogue, err = c.NewCatalogue(cat.Ref(), cat.Connection(), tools)
	if err != nil {
		t.Fatal(err)
	}
	call := c.Call{ID: "worker", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}
	attempt := session.NewBrokerAttempt()
	observed := make(chan c.InvocationOutcome, 1)
	go func() { out, _ := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, attempt); observed <- out }()
	awaitCoordination(t, started)
	if _, err := api.DisconnectTools(ctx, opened.Ref, cat.Connection()); err != nil {
		close(release)
		t.Fatal(err)
	}
	if receiptFinished(api.states[opened.Ref].running) {
		t.Fatal("live worker retired")
	}
	close(release)
	if out := awaitCoordination(t, observed); out.Kind != c.InvocationOutcomeUnknown {
		t.Fatalf("withdrawn worker claimed completion: %#v", out)
	}
	if !receiptFinished(api.states[opened.Ref].running) {
		t.Fatal("finished worker remains busy")
	}
}
