//go:build unix

package flocklease_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/flocklease"
)

const helperEnv = "MECATL_FLOCKLEASE_HELPER"

func TestFlockleaseHelperProcess(_ *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) != 5 {
		os.Exit(2)
	}
	ttl, err := time.ParseDuration(args[4])
	if err != nil {
		os.Exit(2)
	}
	adapter, err := flocklease.New(args[1], ttl, wallclock.Clock{})
	if err != nil {
		os.Exit(2)
	}
	held, err := adapter.Acquire(context.Background(), session.SessionID(args[2]), args[3])
	if err != nil {
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(held); err != nil {
		os.Exit(2)
	}
	time.Sleep(24 * time.Hour)
}

type leaseHelper struct {
	cmd      *exec.Cmd
	held     port.Lease
	killOnce sync.Once
	waitOnce sync.Once
	killErr  error
	waitErr  error
}

func (h *leaseHelper) stop() {
	h.killOnce.Do(func() { h.killErr = h.cmd.Process.Kill() })
	h.waitOnce.Do(func() { h.waitErr = h.cmd.Wait() })
}

func startLeaseHelper(t *testing.T, dir string, id session.SessionID, owner string, ttl time.Duration) *leaseHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFlockleaseHelperProcess$", "--", dir, string(id), owner, ttl.String())
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	h := &leaseHelper{cmd: cmd}
	t.Cleanup(h.stop)
	if err := json.NewDecoder(stdout).Decode(&h.held); err != nil {
		t.Fatalf("decode helper lease: %v", err)
	}
	return h
}

func (h *leaseHelper) kill(t *testing.T) {
	t.Helper()
	h.stop()
	if h.killErr != nil {
		t.Fatalf("SIGKILL helper: %v", h.killErr)
	}
	if h.waitErr == nil {
		t.Fatal("SIGKILLed helper exited successfully")
	}
}

func TestDifferentSessionsDoNotShareInProcessSerialization(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adapter, err := flocklease.New(dir, time.Minute, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a, err := adapter.Acquire(ctx, "blocked-a", "owner-a")
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	locks, err := filepath.Glob(filepath.Join(dir, "*.lock"))
	if err != nil || len(locks) != 1 {
		t.Fatalf("find A stable lock: paths=%v err=%v", locks, err)
	}
	blocker := flock.New(locks[0])
	if err := blocker.Lock(); err != nil {
		t.Fatalf("lock A transition: %v", err)
	}
	defer blocker.Close()

	renewDone := make(chan error, 1)
	renewCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	go func() {
		_, renewErr := adapter.Renew(renewCtx, a)
		renewDone <- renewErr
	}()
	time.Sleep(30 * time.Millisecond)

	started := time.Now()
	b, err := adapter.Acquire(ctx, "unblocked-b", "owner-b")
	if err != nil {
		t.Fatalf("Acquire B while A is blocked: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("Acquire B took %s while A was blocked; sessions are globally serialized", elapsed)
	}
	if err := adapter.Release(ctx, b); err != nil {
		t.Fatalf("Release B: %v", err)
	}
	if err := blocker.Unlock(); err != nil {
		t.Fatalf("unlock A transition: %v", err)
	}
	if err := <-renewDone; err != nil {
		t.Fatalf("Renew A after unblock: %v", err)
	}
}

func TestRepeatedTakeoverReleaseDoesNotGrowLiveDirectoryEntries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adapter, err := flocklease.New(dir, time.Minute, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const cycles = 40
	for i := range cycles {
		held, acquireErr := adapter.Acquire(ctx, "bounded-live-files", "owner-"+strconv.Itoa(i))
		if acquireErr != nil {
			t.Fatalf("Acquire cycle %d: %v", i, acquireErr)
		}
		if releaseErr := adapter.Release(ctx, held); releaseErr != nil {
			t.Fatalf("Release cycle %d: %v", i, releaseErr)
		}
		paths, globErr := filepath.Glob(filepath.Join(dir, "*.live"))
		if globErr != nil {
			t.Fatalf("glob live entries: %v", globErr)
		}
		if len(paths) != 0 {
			t.Fatalf("cycle %d left obsolete live entries: %v", i, paths)
		}
	}
}

func TestSIGKILLImmediatelyReleasesRetainedFlock(t *testing.T) {
	const (
		id  session.SessionID = "sigkill-takeover"
		ttl                   = time.Hour
	)
	dir := t.TempDir()
	help := startLeaseHelper(t, dir, id, "owner-a", ttl)
	contender, err := flocklease.New(dir, ttl, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New contender: %v", err)
	}
	if _, err := contender.Acquire(context.Background(), id, "owner-b"); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("Acquire while helper is live = %v, want ErrLeaseHeld", err)
	}

	started := time.Now()
	help.kill(t)
	taken, err := contender.Acquire(context.Background(), id, "owner-b")
	if err != nil {
		t.Fatalf("Acquire immediately after SIGKILL: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("post-SIGKILL takeover took %s, want well before %s TTL", elapsed, ttl)
	}
	if taken.Token <= help.held.Token {
		t.Fatalf("takeover token = %d, want > killed holder token %d", taken.Token, help.held.Token)
	}
}

func TestExpiryAllowsTakeoverWhileOldProcessLives(t *testing.T) {
	const (
		id  session.SessionID = "live-past-expiry"
		ttl                   = 100 * time.Millisecond
	)
	dir := t.TempDir()
	help := startLeaseHelper(t, dir, id, "owner-a", ttl)
	time.Sleep(3 * ttl)

	contender, err := flocklease.New(dir, ttl, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New contender: %v", err)
	}
	taken, err := contender.Acquire(context.Background(), id, "owner-b")
	if err != nil {
		t.Fatalf("Acquire after expiry while old holder lives: %v", err)
	}
	if taken.Token <= help.held.Token {
		t.Fatalf("takeover token = %d, want > expired holder token %d", taken.Token, help.held.Token)
	}
}

func TestStaleReleaseCannotUnlockSuccessor(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adapters := make([]*flocklease.Lease, 3)
	for i := range adapters {
		var err error
		adapters[i], err = flocklease.New(dir, time.Minute, wallclock.Clock{})
		if err != nil {
			t.Fatalf("New #%d: %v", i, err)
		}
	}
	first, err := adapters[0].Acquire(ctx, "stale-release", "owner-a")
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	if err := adapters[0].Release(ctx, first); err != nil {
		t.Fatalf("Release first: %v", err)
	}
	successor, err := adapters[1].Acquire(ctx, first.SessionID, "owner-b")
	if err != nil {
		t.Fatalf("Acquire successor: %v", err)
	}
	if successor.Token <= first.Token {
		t.Fatalf("successor token = %d, want > first token %d", successor.Token, first.Token)
	}
	if err := adapters[0].Release(ctx, first); err != nil {
		t.Fatalf("stale Release: %v", err)
	}
	if _, err := adapters[2].Acquire(ctx, first.SessionID, "owner-c"); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("Acquire after stale Release = %v, want ErrLeaseHeld", err)
	}
	if err := adapters[1].Release(ctx, successor); err != nil {
		t.Fatalf("Release successor: %v", err)
	}
	third, err := adapters[2].Acquire(ctx, first.SessionID, "owner-c")
	if err != nil {
		t.Fatalf("Acquire after exact Release: %v", err)
	}
	if third.Token <= successor.Token {
		t.Fatalf("third token = %d, want > successor token %d", third.Token, successor.Token)
	}
}

func TestAcquireErrorDoesNotLeakFlockHandle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	seed, err := flocklease.New(dir, time.Minute, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New seed: %v", err)
	}
	held, err := seed.Acquire(ctx, "error-cleanup", "seed")
	if err != nil {
		t.Fatalf("seed Acquire: %v", err)
	}
	if err := seed.Release(ctx, held); err != nil {
		t.Fatalf("seed Release: %v", err)
	}
	records, err := filepath.Glob(filepath.Join(dir, "*.lease.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("find record: paths=%v err=%v", records, err)
	}
	if err := os.WriteFile(records[0], []byte("not-json"), 0o600); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}

	broken, err := flocklease.New(dir, time.Minute, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New broken: %v", err)
	}
	if _, err := broken.Acquire(ctx, held.SessionID, "broken"); err == nil {
		t.Fatal("Acquire with corrupt record succeeded")
	}
	if err := os.Remove(records[0]); err != nil {
		t.Fatalf("remove corrupt record: %v", err)
	}
	contender, err := flocklease.New(dir, time.Minute, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New contender: %v", err)
	}
	if _, err := contender.Acquire(ctx, held.SessionID, "contender"); err != nil {
		t.Fatalf("Acquire after prior read error leaked its flock handle: %v", err)
	}
}

func TestMaxFencingTokenFailsClosedWithoutWrap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adapter, err := flocklease.New(dir, time.Minute, wallclock.Clock{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seed, err := adapter.Acquire(ctx, "token-exhausted", "seed")
	if err != nil {
		t.Fatalf("seed Acquire: %v", err)
	}
	if err := adapter.Release(ctx, seed); err != nil {
		t.Fatalf("seed Release: %v", err)
	}
	records, err := filepath.Glob(filepath.Join(dir, "*.lease.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("find record: paths=%v err=%v", records, err)
	}
	exhausted, err := json.Marshal(map[string]any{
		"owner": "", "token": uint64(math.MaxUint64), "expiry": time.Time{},
	})
	if err != nil {
		t.Fatalf("marshal exhausted record: %v", err)
	}
	if err := os.WriteFile(records[0], exhausted, 0o600); err != nil {
		t.Fatalf("write exhausted record: %v", err)
	}
	if _, err := adapter.Acquire(ctx, seed.SessionID, "next"); err == nil {
		t.Fatal("Acquire at MaxUint64 succeeded and could wrap the fencing token")
	}
}

func TestConcurrentAcquireHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adapters := make([]*flocklease.Lease, 2)
	for i := range adapters {
		var err error
		adapters[i], err = flocklease.New(dir, time.Minute, wallclock.Clock{})
		if err != nil {
			t.Fatalf("New #%d: %v", i, err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(adapters))
	var wg sync.WaitGroup
	for i, adapter := range adapters {
		wg.Add(1)
		go func(i int, adapter *flocklease.Lease) {
			defer wg.Done()
			<-start
			_, err := adapter.Acquire(ctx, "concurrent", "owner-"+strconv.Itoa(i))
			results <- err
		}(i, adapter)
	}
	close(start)
	wg.Wait()
	close(results)

	var wins, held int
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, port.ErrLeaseHeld):
			held++
		default:
			t.Fatalf("Acquire returned unexpected error: %v", err)
		}
	}
	if wins != 1 || held != 1 {
		t.Fatalf("concurrent results: wins=%d held=%d, want 1/1", wins, held)
	}
}
