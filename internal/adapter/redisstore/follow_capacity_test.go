package redisstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive-core/redisconn"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestRedisFollowCapacity_Scenario1_GenerationPublishesIsolatedClientPair(t *testing.T) {
	server := miniredis.RunT(t)
	var configs []redisconn.Config
	var clients []redis.UniversalClient
	deps := defaultStoreDependencies()
	deps.initialPair = func(ctx context.Context, base *redisconn.Config, poolSize int) (clientPair, error) {
		return buildClientPair(ctx, base, poolSize, func(_ context.Context, cfg *redisconn.Config) (redis.UniversalClient, error) {
			configs = append(configs, *cfg)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			clients = append(clients, client)
			return client, nil
		})
	}
	store, err := newWithConfig(Config{
		Addr: server.Addr(), AllowPlaintext: true, FollowPoolSize: 7, MaxFollowers: 6,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if len(configs) != 2 {
		t.Fatalf("client configurations = %d, want durability and follow", len(configs))
	}
	if configs[0].PoolSize != 0 || configs[0].MaxActiveConns != 0 {
		t.Fatalf("durability pool limits = %d/%d, want existing zero/default values", configs[0].PoolSize, configs[0].MaxActiveConns)
	}
	if configs[1].PoolSize != 7 || configs[1].MaxActiveConns != 7 {
		t.Fatalf("follow pool limits = %d/%d, want 7/7", configs[1].PoolSize, configs[1].MaxActiveConns)
	}
	if len(clients) != 2 || clients[0] == clients[1] || store.testClient() == store.testFollowClient() {
		t.Fatal("published generation does not own distinct durability and follow clients")
	}
	if err := store.testClient().Ping(t.Context()).Err(); err != nil {
		t.Fatalf("durability client unusable: %v", err)
	}
	if err := store.testFollowClient().Ping(t.Context()).Err(); err != nil {
		t.Fatalf("follow client unusable: %v", err)
	}
	if got := store.followers.limit; got != 6 {
		t.Fatalf("follower limit = %d, want 6", got)
	}

	t.Run("omission resolves both adapter defaults to 32", func(t *testing.T) {
		poolSize, maxFollowers, err := effectiveFollowLimits(Config{})
		if err != nil {
			t.Fatal(err)
		}
		if poolSize != 32 || maxFollowers != 32 {
			t.Fatalf("defaults = %d/%d, want 32/32", poolSize, maxFollowers)
		}
	})
	t.Run("invalid effective bounds fail closed", func(t *testing.T) {
		for _, cfg := range []Config{
			{FollowPoolSize: -1},
			{MaxFollowers: -1},
			{FollowPoolSize: 4, MaxFollowers: 5},
		} {
			if _, _, err := effectiveFollowLimits(cfg); err == nil {
				t.Fatalf("effectiveFollowLimits(%+v) succeeded", cfg)
			}
		}
	})
}

func TestADR_0330_ProductionWiringIsolatesBoundedFollowPool(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewWithConfig(Config{
		Addr: server.Addr(), AllowPlaintext: true, FollowPoolSize: 2, MaxFollowers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	durability, ok := store.testClient().(*redis.Client)
	if !ok {
		t.Fatalf("production durability client = %T, want *redis.Client", store.testClient())
	}
	follow, ok := store.testFollowClient().(*redis.Client)
	if !ok {
		t.Fatalf("production follow client = %T, want *redis.Client", store.testFollowClient())
	}
	if durability == follow {
		t.Fatal("production wiring published one client for durability and follow traffic")
	}
	if opts := follow.Options(); opts.PoolSize != 2 || opts.MaxActiveConns != 2 {
		t.Fatalf("production follow pool limits = %d/%d, want 2/2", opts.PoolSize, opts.MaxActiveConns)
	}

	// Hold every real follow-pool connection. If production wiring accidentally
	// shares this bounded pool with durability traffic, either operation below
	// waits for capacity until its context expires.
	for range 2 {
		conn := follow.Conn()
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.Ping(t.Context()).Err(); err != nil {
			t.Fatalf("occupy follow connection: %v", err)
		}
	}
	if stats := follow.PoolStats(); stats.TotalConns != 2 || stats.IdleConns != 0 {
		t.Fatalf("saturated follow pool = total %d idle %d, want 2/0", stats.TotalConns, stats.IdleConns)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := store.AppendEvent(ctx, "production-isolation-append", session.Event{Type: session.EvResult}); err != nil {
		t.Fatalf("AppendEvent waited behind saturated follow pool: %v", err)
	}
	if err := store.Save(ctx, testSession("production-isolation-save")); err != nil {
		t.Fatalf("Save waited behind saturated follow pool: %v", err)
	}
}

func TestRedisFollowCapacity_Scenario1_CredentialReloadSwapsPairAtomically(t *testing.T) {
	oldServer := miniredis.RunT(t)
	store, err := New(oldServer.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	oldDurability, oldFollow := store.testClient(), store.testFollowClient()

	newServer := miniredis.RunT(t)
	partial := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: newServer.Addr()})}
	deps := defaultStoreDependencies()
	deps.candidatePair = func(ctx context.Context, base *redisconn.Config, poolSize int) (clientPair, error) {
		calls := 0
		return buildClientPair(ctx, base, poolSize, func(context.Context, *redisconn.Config) (redis.UniversalClient, error) {
			calls++
			if calls == 1 {
				return partial, nil
			}
			return nil, errors.New("follow probe failed")
		})
	}
	cfg := Config{Addr: newServer.Addr(), AllowPlaintext: true, FollowPoolSize: 3, MaxFollowers: 3}
	if err := reloadCandidate(t.Context(), store, cfg, deps); err == nil {
		t.Fatal("reloadCandidate succeeded with an unusable follow half")
	}
	if partial.closes.Load() != 1 {
		t.Fatalf("partial candidate closes = %d, want 1", partial.closes.Load())
	}
	if store.testClient() != oldDurability || store.testFollowClient() != oldFollow {
		t.Fatal("partial candidate displaced one half of the current generation")
	}

	var replacements []redis.UniversalClient
	deps.candidatePair = func(ctx context.Context, base *redisconn.Config, poolSize int) (clientPair, error) {
		return buildClientPair(ctx, base, poolSize, func(context.Context, *redisconn.Config) (redis.UniversalClient, error) {
			client := redis.NewClient(&redis.Options{Addr: newServer.Addr()})
			replacements = append(replacements, client)
			return client, nil
		})
	}
	if err := reloadCandidate(t.Context(), store, cfg, deps); err != nil {
		t.Fatal(err)
	}
	if len(replacements) != 2 || store.testClient() != replacements[0] || store.testFollowClient() != replacements[1] {
		t.Fatal("successful reload did not atomically publish the complete replacement pair")
	}
}

func TestRedisFollowCapacity_Scenario1_ReadRoutingIsComplete(t *testing.T) {
	server := miniredis.RunT(t)
	durability := redis.NewClient(&redis.Options{Addr: server.Addr()})
	follow := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := newPairedTestStore(t, durability, follow, 2, 300*time.Millisecond)
	id := session.SessionID("routing")
	if _, err := store.AppendEvent(t.Context(), id, session.Event{Type: session.EvResult}); err != nil {
		t.Fatal(err)
	}

	durabilitySpy, followSpy := &cmdSpy{}, &cmdSpy{}
	durability.AddHook(durabilitySpy)
	follow.AddHook(followSpy)
	clearSpy(durabilitySpy)
	clearSpy(followSpy)
	for _, err := range store.ReadAfter(t.Context(), id, "", port.ReadOptions{Follow: true, Limit: 1}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(allCommands(durabilitySpy)); got != 0 {
		t.Fatalf("Follow:true issued %d commands through durability client: %v", got, allCommands(durabilitySpy))
	}
	if got := len(allCommands(followSpy)); got == 0 {
		t.Fatal("Follow:true issued no command through follow client")
	}

	clearSpy(durabilitySpy)
	clearSpy(followSpy)
	for _, err := range store.ReadAfter(t.Context(), id, "", port.ReadOptions{Limit: 1}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(allCommands(followSpy)); got != 0 {
		t.Fatalf("Follow:false issued %d commands through follow client: %v", got, allCommands(followSpy))
	}
	if got := len(allCommands(durabilitySpy)); got == 0 {
		t.Fatal("Follow:false issued no command through durability client")
	}

	clearSpy(durabilitySpy)
	clearSpy(followSpy)
	if _, err := store.AppendEvent(t.Context(), id, session.Event{Type: session.EvMessageDelta, Text: "routed append"}); err != nil {
		t.Fatal(err)
	}
	if len(allCommands(durabilitySpy)) == 0 || len(allCommands(followSpy)) != 0 {
		t.Fatalf("AppendEvent routing: durability=%v follow=%v", allCommands(durabilitySpy), allCommands(followSpy))
	}

	clearSpy(durabilitySpy)
	clearSpy(followSpy)
	if err := store.Save(t.Context(), testSession("routing-save")); err != nil {
		t.Fatal(err)
	}
	if len(allCommands(durabilitySpy)) == 0 || len(allCommands(followSpy)) != 0 {
		t.Fatalf("Save routing: durability=%v follow=%v", allCommands(durabilitySpy), allCommands(followSpy))
	}
}

func TestADR_0330_FollowersDoNotStarveDurability(t *testing.T) {
	server := miniredis.RunT(t)
	durability := redis.NewClient(&redis.Options{Addr: server.Addr()})
	followBase := redis.NewClient(&redis.Options{Addr: server.Addr()})
	follow := newControlledFollowClient(followBase, followCooperative)
	store := newPairedTestStore(t, durability, follow, 2, 300*time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := startFollowers(ctx, t, store, 2)
	awaitSignals(t, follow.started, 2, "followers did not park")

	operationDone := make(chan error, 1)
	go func() {
		if _, err := store.AppendEvent(context.Background(), "durable-append", session.Event{Type: session.EvResult}); err != nil {
			operationDone <- err
			return
		}
		operationDone <- store.Save(context.Background(), testSession("durable-save"))
	}()
	select {
	case err := <-operationDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("durability operations waited behind parked followers")
	}
	cancel()
	awaitClosed(t, done, time.Second, "followers did not stop")
}

func TestRedisFollowCapacity_Scenario2_AdmissionCoversIteratorLifetime(t *testing.T) {
	server := miniredis.RunT(t)
	controlled := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followCooperative)
	store := newPairedTestStore(t,
		redis.NewClient(&redis.Options{Addr: server.Addr()}), controlled, 1, 300*time.Millisecond)
	id := session.SessionID("admission-lifetime")
	if _, err := store.AppendEvent(t.Context(), id, session.Event{Type: session.EvMessageDelta, Text: "one"}); err != nil {
		t.Fatal(err)
	}

	t.Run("normal completion", func(t *testing.T) {
		consumeFollow(t, store, id, "", port.ReadOptions{Follow: true, Limit: 1})
		assertNoActiveFollowers(t, store)
	})
	t.Run("read error", func(t *testing.T) {
		consumeFollowExpectError(t, store, id, port.Cursor("not-a-cursor"), port.ErrCursorMalformed)
		assertNoActiveFollowers(t, store)
	})
	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		done := startFollowers(ctx, t, store, 1)
		awaitSignals(t, controlled.started, 1, "follower did not enter blocking cycle")
		cancel()
		awaitClosed(t, done, time.Second, "cancelled follower did not stop")
		assertNoActiveFollowers(t, store)
	})
	t.Run("early consumer break", func(t *testing.T) {
		if _, err := store.AppendEvent(t.Context(), id, session.Event{Type: session.EvMessageDelta, Text: "two"}); err != nil {
			t.Fatal(err)
		}
		for _, err := range store.ReadAfter(t.Context(), id, "", port.ReadOptions{Follow: true}) {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		assertNoActiveFollowers(t, store)
	})
}

func TestADR_0330_CapacityFailsFast(t *testing.T) {
	server := miniredis.RunT(t)
	durability := redis.NewClient(&redis.Options{Addr: server.Addr()})
	followBase := redis.NewClient(&redis.Options{Addr: server.Addr()})
	follow := newControlledFollowClient(followBase, followCooperative)
	store := newPairedTestStore(t, durability, follow, 1, 300*time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := startFollowers(ctx, t, store, 1)
	awaitSignals(t, follow.started, 1, "first follower did not park")
	before := follow.calls.Load()

	started := time.Now()
	var got error
	for _, err := range store.ReadAfter(t.Context(), "capacity", "", port.ReadOptions{Follow: true}) {
		got = err
		break
	}
	if !errors.Is(got, port.ErrEventFollowCapacity) {
		t.Fatalf("saturated follow error = %v, want ErrEventFollowCapacity", got)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("saturated admission waited %v instead of failing immediately", elapsed)
	}
	if calls := follow.calls.Load(); calls != before {
		t.Fatalf("rejected follower performed Redis work: calls %d -> %d", before, calls)
	}
	cancel()
	awaitClosed(t, done, time.Second, "first follower did not stop")
}

func TestRedisFollowCapacity_Scenario2_ReplayBypassesAdmission(t *testing.T) {
	server := miniredis.RunT(t)
	durability := redis.NewClient(&redis.Options{Addr: server.Addr()})
	follow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followCooperative)
	store := newPairedTestStore(t, durability, follow, 1, 300*time.Millisecond)
	id := session.SessionID("replay-bypass")
	if _, err := store.AppendEvent(t.Context(), id, session.Event{Type: session.EvResult}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := startFollowers(ctx, t, store, 1)
	awaitSignals(t, follow.started, 1, "first follower did not park")

	var records int
	for _, err := range store.ReadAfter(t.Context(), id, "", port.ReadOptions{Limit: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		records++
	}
	if records != 1 {
		t.Fatalf("replay records = %d, want 1 while follow admission is saturated", records)
	}
	cancel()
	awaitClosed(t, done, time.Second, "first follower did not stop")
}

func TestRedisFollowCapacity_Scenario3_CloseLinearizesWithAdmission(t *testing.T) {
	t.Run("admission wins", func(t *testing.T) {
		server := miniredis.RunT(t)
		follow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followCooperative)
		store := newPairedTestStore(t, redis.NewClient(&redis.Options{Addr: server.Addr()}), follow, 1, 300*time.Millisecond)
		errorsSeen := make(chan error, 1)
		done := startFollowersReporting(context.Background(), store, 1, errorsSeen)
		awaitSignals(t, follow.started, 1, "admitted follower did not enter its blocking read")

		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		awaitClosed(t, done, store.closeGrace, "admitted follower missed the close cancellation snapshot")
		assertNoReportedErrors(t, errorsSeen)
		assertNoActiveFollowers(t, store)
	})

	t.Run("close wins", func(t *testing.T) {
		server := miniredis.RunT(t)
		follow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followCooperative)
		store := newPairedTestStore(t, redis.NewClient(&redis.Options{Addr: server.Addr()}), follow, 1, 300*time.Millisecond)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}

		var got error
		for _, err := range store.ReadAfter(t.Context(), "closed-before-admission", "", port.ReadOptions{Follow: true}) {
			got = err
			break
		}
		if !errors.Is(got, errStoreClosed) {
			t.Fatalf("post-close admission error = %v, want errStoreClosed", got)
		}
		if calls := follow.calls.Load(); calls != 0 {
			t.Fatalf("post-close admission performed %d Redis calls, want 0", calls)
		}
	})

	// Keep an adversarial concurrent stress pass in addition to the deterministic
	// proofs above. Every outcome must be one of the two mutex-linearized cases.
	for attempt := range 40 {
		server := miniredis.RunT(t)
		follow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followCooperative)
		store := newPairedTestStore(t, redis.NewClient(&redis.Options{Addr: server.Addr()}), follow, 8, 300*time.Millisecond)
		start := make(chan struct{})
		results := make(chan error, 8)
		for range 8 {
			go func() {
				<-start
				var got error
				for _, err := range store.ReadAfter(context.Background(), "close-race", "", port.ReadOptions{Follow: true}) {
					got = err
					break
				}
				results <- got
			}()
		}
		close(start)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		for range 8 {
			select {
			case err := <-results:
				if err != nil && !errors.Is(err, errStoreClosed) {
					t.Fatalf("attempt %d racing follower error = %v", attempt, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("attempt %d: admitted follower missed close cancellation", attempt)
			}
		}
	}
}

func TestRedisFollowCapacity_Scenario3_CloseCancelsAndJoinsFollowers(t *testing.T) {
	server := miniredis.RunT(t)
	follow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followCooperative)
	store := newPairedTestStore(t, redis.NewClient(&redis.Options{Addr: server.Addr()}), follow, 2, 300*time.Millisecond)
	diagnostics := &closeDiag{}
	store.diagnostics = diagnostics
	errorsSeen := make(chan error, 2)
	done := startFollowersReporting(context.Background(), store, 2, errorsSeen)
	awaitSignals(t, follow.started, 2, "followers did not park")
	closeDone := make(chan error, 1)
	go func() { closeDone <- store.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(store.closeGrace):
		t.Fatal("Close did not join cooperative followers within its configured grace")
	}
	awaitClosed(t, done, time.Second, "Close returned without joining cooperative followers")
	assertNoReportedErrors(t, errorsSeen)
	diagnostics.mu.Lock()
	timedOut := diagnostics.timedOut
	diagnostics.mu.Unlock()
	if timedOut {
		t.Fatal("cooperative follower shutdown produced an outstanding-operation warning")
	}
	if follow.closes.Load() != 1 {
		t.Fatalf("follow closes = %d, want normal generation close once", follow.closes.Load())
	}
}

func TestADR_0330_ForceCloseIsFollowOnly(t *testing.T) {
	server := miniredis.RunT(t)
	durability := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	follow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followCloseUnblocks)
	store := newPairedTestStore(t, durability, follow, 1, 200*time.Millisecond)
	leased, release, err := store.clients.acquire()
	if err != nil {
		t.Fatal(err)
	}
	errorsSeen := make(chan error, 1)
	done := startFollowersReporting(context.Background(), store, 1, errorsSeen)
	awaitSignals(t, follow.started, 1, "follower did not park")

	started := time.Now()
	deadline := started.Add(store.closeGrace)
	closeDeadline := deadline.Add(50 * time.Millisecond)
	closeDone := make(chan error, 1)
	go func() { closeDone <- store.Close() }()
	select {
	case <-follow.closed:
		if elapsed := time.Since(started); elapsed < store.closeGrace/3 {
			t.Fatalf("follow client force-closed before the cooperative wait elapsed: %v", elapsed)
		}
	case <-time.After(time.Until(deadline)):
		t.Fatal("follow client was not force-closed within the total close grace")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Until(closeDeadline)):
		t.Fatal("Store.Close did not return after force-closing the follow client")
	}
	select {
	case <-done:
	default:
		t.Fatal("Store.Close returned before the force-closed follower was joined")
	}
	assertNoReportedErrors(t, errorsSeen)
	if follow.closes.Load() != 1 {
		t.Fatalf("follow closes = %d, want 1", follow.closes.Load())
	}
	if durability.closes.Load() != 0 {
		t.Fatalf("durability client force-closed with active lease: closes=%d", durability.closes.Load())
	}
	if err := leased.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("leased durability client unusable after follower force-close: %v", err)
	}
	release()
	awaitCloseCount(t, &durability.closes, 1)
}

func TestRedisFollowCapacity_Scenario3_PathologicalCloseRemainsBounded(t *testing.T) {
	server := miniredis.RunT(t)
	durability := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	follow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: server.Addr()}), followPathological)
	store := newPairedTestStore(t, durability, follow, 1, 500*time.Millisecond)
	done := startFollowersReporting(context.Background(), store, 1, nil)
	awaitSignals(t, follow.started, 1, "pathological follower did not park")

	started := time.Now()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > store.closeGrace+100*time.Millisecond {
		t.Fatalf("Close exceeded fixed total grace: %v", elapsed)
	}
	if durability.closes.Load() != 0 {
		t.Fatalf("pathological follower caused durability force-close: %d", durability.closes.Load())
	}
	var admissionErr error
	for _, err := range store.ReadAfter(t.Context(), "late", "", port.ReadOptions{Follow: true}) {
		admissionErr = err
		break
	}
	if !errors.Is(admissionErr, errStoreClosed) {
		t.Fatalf("new admission after close = %v, want errStoreClosed", admissionErr)
	}

	close(follow.readRelease)
	close(follow.closeRelease)
	awaitClosed(t, done, time.Second, "late follower cleanup was not retained")
	awaitCloseCount(t, &durability.closes, 1)
}

func TestRedisFollowCapacity_Scenario3_RotationDoesNotPinGeneration(t *testing.T) {
	oldServer := miniredis.RunT(t)
	oldDurability := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: oldServer.Addr()})}
	oldFollow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: oldServer.Addr()}), followReleaseReturnsNil)
	store := newPairedTestStore(t, oldDurability, oldFollow, 1, 500*time.Millisecond)
	done := startFollowersReporting(context.Background(), store, 1, nil)
	awaitSignals(t, oldFollow.started, 1, "old follow cycle did not start")

	newServer := miniredis.RunT(t)
	newDurability := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: newServer.Addr()})}
	newFollow := newControlledFollowClient(redis.NewClient(&redis.Options{Addr: newServer.Addr()}), followCooperative)
	if err := store.clients.swapPair(clientPair{durability: newDurability, follow: newFollow}); err != nil {
		t.Fatal(err)
	}
	close(oldFollow.readRelease)
	awaitSignals(t, newFollow.started, 1, "follower did not continue on rotated follow client")
	awaitCloseCount(t, &oldDurability.closes, 1)
	awaitCloseCount(t, &oldFollow.closes, 1)

	var capacityErr error
	for _, err := range store.ReadAfter(t.Context(), "rotation-capacity", "", port.ReadOptions{Follow: true}) {
		capacityErr = err
		break
	}
	if !errors.Is(capacityErr, port.ErrEventFollowCapacity) {
		t.Fatalf("rotation released full-iterator admission: %v", capacityErr)
	}
	store.Close()
	awaitClosed(t, done, time.Second, "rotated follower did not stop")
}

func newPairedTestStore(
	t *testing.T,
	durability redis.UniversalClient,
	follow redis.UniversalClient,
	maxFollowers int,
	grace time.Duration,
) *Store {
	t.Helper()
	store := &Store{
		clients:   newClientGenerationsPair(clientPair{durability: durability, follow: follow}),
		followers: newFollowerRegistry(maxFollowers), diagnostics: port.NopDiagnostics{}, closeGrace: grace,
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testSession(id session.SessionID) *session.Session {
	return session.New(id, session.ModeDefault,
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"},
		session.Limits{}, time.Now().UTC())
}

func clearSpy(spy *cmdSpy) {
	spy.mu.Lock()
	spy.sent = nil
	spy.mu.Unlock()
}

func allCommands(spy *cmdSpy) [][]any {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	out := make([][]any, len(spy.sent))
	copy(out, spy.sent)
	return out
}

type followClientMode int

const (
	followCooperative followClientMode = iota
	followCloseUnblocks
	followPathological
	followReleaseReturnsNil
)

type controlledFollowClient struct {
	redis.UniversalClient
	mode         followClientMode
	started      chan struct{}
	readRelease  chan struct{}
	closeRelease chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
	calls        atomic.Int32
	closes       atomic.Int32
}

func newControlledFollowClient(client redis.UniversalClient, mode followClientMode) *controlledFollowClient {
	c := &controlledFollowClient{
		UniversalClient: client,
		mode:            mode, started: make(chan struct{}, 64), readRelease: make(chan struct{}),
		closeRelease: make(chan struct{}), closed: make(chan struct{}),
	}
	if mode != followPathological {
		close(c.closeRelease)
	}
	return c
}

func (c *controlledFollowClient) XRead(ctx context.Context, args *redis.XReadArgs) *redis.XStreamSliceCmd {
	c.calls.Add(1)
	if args.Block <= 0 {
		return c.UniversalClient.XRead(ctx, args)
	}
	c.started <- struct{}{}
	cmd := redis.NewXStreamSliceCmd(ctx)
	switch c.mode {
	case followCooperative:
		<-ctx.Done()
		cmd.SetErr(ctx.Err())
	case followCloseUnblocks:
		<-c.closed
		cmd.SetErr(redis.ErrClosed)
	case followPathological:
		<-c.readRelease
		cmd.SetErr(redis.ErrClosed)
	case followReleaseReturnsNil:
		<-c.readRelease
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

func (c *controlledFollowClient) Close() error {
	c.closes.Add(1)
	c.closeOnce.Do(func() { close(c.closed) })
	<-c.closeRelease
	return c.UniversalClient.Close()
}

func startFollowers(ctx context.Context, t *testing.T, store *Store, count int) <-chan struct{} {
	t.Helper()
	return startFollowersReporting(ctx, store, count, nil)
}

func startFollowersReporting(ctx context.Context, store *Store, count int, errs chan<- error) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(count)
	for i := range count {
		go func(n int) {
			defer wg.Done()
			for _, err := range store.ReadAfter(ctx, session.SessionID(fmt.Sprintf("follower-%d", n)), "", port.ReadOptions{Follow: true}) {
				if errs != nil {
					errs <- err
				}
				if err != nil {
					return
				}
			}
		}(i)
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

func assertNoReportedErrors(t *testing.T, errorsSeen chan error) {
	t.Helper()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("store-owned shutdown surfaced as event-log fault: %v", err)
		}
	}
}

func consumeFollow(t *testing.T, store *Store, id session.SessionID, cursor port.Cursor, opts port.ReadOptions) {
	t.Helper()
	for _, err := range store.ReadAfter(t.Context(), id, cursor, opts) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func consumeFollowExpectError(t *testing.T, store *Store, id session.SessionID, cursor port.Cursor, want error) {
	t.Helper()
	var got error
	for _, err := range store.ReadAfter(t.Context(), id, cursor, port.ReadOptions{Follow: true}) {
		got = err
		break
	}
	if !errors.Is(got, want) {
		t.Fatalf("ReadAfter error = %v, want %v", got, want)
	}
}

func assertNoActiveFollowers(t *testing.T, store *Store) {
	t.Helper()
	if got := store.followers.activeCount(); got != 0 {
		t.Fatalf("active followers = %d, want 0 after iterator exit", got)
	}
}

func awaitSignals(t *testing.T, signals <-chan struct{}, count int, failure string) {
	t.Helper()
	for range count {
		select {
		case <-signals:
		case <-time.After(time.Second):
			t.Fatal(failure)
		}
	}
}

func awaitClosed(t *testing.T, done <-chan struct{}, timeout time.Duration, failure string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal(failure)
	}
}

func awaitCloseCount(t *testing.T, count *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if count.Load() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("close count = %d, want %d", count.Load(), want)
}
