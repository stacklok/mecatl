package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

type delayedCandidateRedis struct {
	redis.UniversalClient
	writes chan []byte
}

func (r *delayedCandidateRedis) Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	r.writes <- append([]byte(nil), value.([]byte)...)
	cmd := redis.NewStatusCmd(ctx)
	cmd.SetErr(errors.New("transport returned before SET execution"))
	return cmd
}

func TestSessionAPICoordinationIdenticalCandidateAndDelayedWrite(t *testing.T) {
	api, _, ctx := reviewAPI(t)
	opened, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := api.states[opened.Ref]
	base := api.redis
	delayed := &delayedCandidateRedis{UniversalClient: base, writes: make(chan []byte, 1)}
	api.redis = delayed
	op, release, err := api.operation(ctx, opened.Ref, apiControl{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	// A durably verified recovery candidate must not issue another identical SET.
	if err := api.saveRecord(op, st, st.record); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delayed.writes:
		t.Fatal("redundant identical SET")
	default:
	}
	candidate := st.record
	candidate.Connection, candidate.Connected = apiRef(), true
	// This preexisting logical candidate is not evidence of the current write.
	old, _ := json.Marshal(candidate)
	key := sessionAPIPrefix + string(opened.Ref)
	if err := base.Set(ctx, key, old, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if err := api.saveRecord(op, st, candidate); err == nil {
		t.Fatal("old identical value proved delayed SET")
	}
	pending := awaitCoordination(t, delayed.writes)
	release()
	release = nil
	if st.pendingWrite == nil {
		t.Fatal("lost pending write fence")
	}
	if _, err := api.DisconnectTools(ctx, opened.Ref, c.ConnectionRef(candidate.Connection)); err == nil {
		t.Fatal("disconnect passed outstanding SET")
	}
	if _, err := api.DeleteSession(ctx, opened.Ref); err == nil {
		t.Fatal("delete passed outstanding SET")
	}
	// Complete the single execution, then cleanup can safely proceed.
	if err := base.Set(ctx, key, pending, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	api.redis = base
	if _, err := api.DisconnectTools(ctx, opened.Ref, c.ConnectionRef(candidate.Connection)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DeleteSession(ctx, opened.Ref); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(ctx, key).Err(); !errors.Is(err, redis.Nil) {
		t.Fatalf("metadata resurrected: %v", err)
	}
}

func TestSessionAPICoordinationResolvedEnrollmentRearmsCurrentGeneration(t *testing.T) {
	api, _, ctx := reviewAPI(t)
	opened, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := api.states[opened.Ref]
	ref := c.EnrollmentRef(apiRef())
	st.enrollment = &apiEnrollment{ref: ref, status: c.FlowStatus{Kind: c.FlowPending}}
	api.mu.Lock()
	st.enrollmentRef = ref
	api.mu.Unlock()
	op, release, err := api.operation(ctx, opened.Ref, apiControl{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	done := make(chan error, 1)
	go func() {
		out, err := api.CancelEnrollment(ctx, opened.Ref, ref)
		if err == nil && out != c.AlreadyResolved {
			err = errors.New("wrong cancel outcome")
		}
		done <- err
	}()
	awaitCoordination(t, op.Done())
	st.enrollment.status = c.FlowStatus{Kind: c.FlowCompleted}
	release()
	release = nil
	if err := awaitCoordination(t, done); err != nil {
		t.Fatal(err)
	}
	if _, err := api.OpenSession(ctx, &opened.Ref); err != nil {
		t.Fatalf("resolved cancel left session invalidated: %v", err)
	}
}

type completionReadRedis struct {
	redis.UniversalClient
	entered chan context.Context
	release chan struct{}
	blocked bool
}

func (r *completionReadRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	if !r.blocked {
		r.blocked = true
		r.entered <- ctx
		<-r.release
	}
	return r.UniversalClient.Get(context.WithoutCancel(ctx), key)
}

func TestSessionAPICoordinationReconciledDisconnectRearmsCurrentGeneration(t *testing.T) {
	api, _, ctx, old := seedCleanupConnection(t)
	fault := &cleanupAckRedis{UniversalClient: api.redis, final: true, landed: true, armed: true}
	api.redis = fault
	connection := c.ConnectionRef(old.Connection)
	if _, err := api.DisconnectTools(ctx, old.Ref, connection); err == nil {
		t.Fatal("expected ambiguous final save")
	}
	block := &completionReadRedis{UniversalClient: fault.UniversalClient, entered: make(chan context.Context, 1), release: make(chan struct{})}
	defer func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	}()
	api.redis = block
	first := make(chan error, 1)
	go func() { _, err := api.DisconnectTools(ctx, old.Ref, connection); first <- err }()
	op := awaitCoordination(t, block.entered)
	second := make(chan error, 1)
	go func() {
		out, err := api.DisconnectTools(ctx, old.Ref, connection)
		if err == nil && out != c.AlreadyDisconnected {
			err = errors.New("wrong disconnect outcome")
		}
		second <- err
	}()
	awaitCoordination(t, op.Done())
	close(block.release)
	if err := awaitCoordination(t, first); err == nil {
		t.Fatal("stale generation cleared fence")
	}
	if err := awaitCoordination(t, second); err != nil {
		t.Fatal(err)
	}
	if _, err := api.OpenSession(ctx, &old.Ref); err != nil {
		t.Fatalf("reconciled disconnect left invalidated: %v", err)
	}
}

func TestSessionAPICoordinationMetadataWritesDisableRedisRetries(t *testing.T) {
	api, _, ctx := reviewAPI(t)
	client := api.redis.(*redis.Client)
	config := *client.Options()
	config.MaxRetries = 3
	retrying := redis.NewClient(&config)
	defer retrying.Close()
	facade, err := NewSessionAPI(api.process, retrying, api.workload)
	if err != nil {
		t.Fatal(err)
	}
	defer facade.Close()
	if facade.metadata == nil || facade.metadata.Options().MaxRetries != 0 || retrying.Options().MaxRetries != 3 {
		t.Fatal("metadata SET retries not independently disabled")
	}
	if _, err := facade.OpenSession(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSessionAPICoordinationNoopCannotRearmUnfinishedCleanup(t *testing.T) {
	for _, fence := range []string{"write", "withdrawal", "recovery", "enrollment", "authorization", "stale-generation"} {
		t.Run(fence, func(t *testing.T) {
			api := &SessionAPI{}
			st := newAPIState(apiRecord{})
			st.invalidated, st.generation = true, 1
			op := &apiOperation{state: st, generation: 1, control: true}
			switch fence {
			case "write":
				st.pendingWrite = &apiRecord{}
			case "withdrawal":
				st.record.Withdrawing = true
			case "recovery":
				st.recovering = true
			case "enrollment":
				st.enrollment = &apiEnrollment{status: c.FlowStatus{Kind: c.FlowPending}}
			case "authorization":
				st.parked["pending"] = &apiParked{cleanupPending: true}
			case "stale-generation":
				op.generation = 0
			}
			api.rearm(context.WithValue(t.Context(), apiOperationKey{}, op), st)
			if !st.invalidated {
				t.Fatal("noop cleared unfinished cleanup fence")
			}
		})
	}
}
