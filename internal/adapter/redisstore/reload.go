package redisstore

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/stacklok/toolhive-core/redis"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/filewatch"
)

const (
	reloadDebounce       = 100 * time.Millisecond
	reloadMaxDebounce    = time.Second
	reloadProbeTimeout   = 5 * time.Second
	reloadMaxAttempts    = 8
	reloadInitialBackoff = 100 * time.Millisecond
	reloadMaxBackoff     = 2 * time.Second
	storeCloseGrace      = 2 * time.Second
	reloadJoinGrace      = 2 * time.Second
)

type clientFactory func(context.Context, *tcredis.Config) (redis.UniversalClient, error)
type watcherFactory func([]string, time.Duration, time.Duration, func(), func(error)) (*filewatch.Watcher, error)
type credentialFileReader func(*os.File, int64) ([]byte, error)

func readCredentialFile(file *os.File, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return body, errors.New("credential file is too large")
	}
	return body, nil
}

type storeDependencies struct {
	initialClient clientFactory
	candidate     clientFactory
	watcher       watcherFactory
	backoff       func(int) time.Duration
	jitter        func(time.Duration) time.Duration
	readFile      credentialFileReader
	closeGrace    time.Duration
	joinGrace     time.Duration
}

func defaultStoreDependencies() storeDependencies {
	newClient := func(ctx context.Context, cfg *tcredis.Config) (redis.UniversalClient, error) {
		return tcredis.NewClient(ctx, cfg)
	}
	return storeDependencies{
		initialClient: newClient,
		candidate:     newClient,
		watcher:       filewatch.New,
		jitter: func(delay time.Duration) time.Duration {
			spread := delay / 4
			if spread == 0 {
				return delay
			}
			offset, err := rand.Int(rand.Reader, big.NewInt(int64(2*spread+1)))
			if err != nil {
				return delay
			}
			return delay - spread + time.Duration(offset.Int64())
		},
		readFile:   readCredentialFile,
		closeGrace: storeCloseGrace,
		joinGrace:  reloadJoinGrace,
	}
}

type reloadLifecycle struct {
	watcher     *filewatch.Watcher
	cancel      context.CancelFunc
	done        chan struct{}
	diagnostics port.Diagnostics
	joinGrace   time.Duration
	once        sync.Once
}

func (cfg Config) reloadEnabled() bool {
	return cfg.CAFile != "" || cfg.UsernameFile != "" || cfg.PasswordFile != ""
}

func (cfg Config) reloadPaths() []string {
	paths := make([]string, 0, 3)
	for _, path := range []string{cfg.CAFile, cfg.UsernameFile, cfg.PasswordFile} {
		if path == "" {
			continue
		}
		duplicate := false
		for _, existing := range paths {
			if existing == path {
				duplicate = true
				break
			}
		}
		if !duplicate {
			paths = append(paths, path)
		}
	}
	return paths
}

func startReloadLifecycle(st *Store, cfg Config, deps storeDependencies) (*reloadLifecycle, error) {
	diagnostics := cfg.Diagnostics
	if diagnostics == nil {
		diagnostics = port.NopDiagnostics{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	lifecycle := &reloadLifecycle{
		cancel: cancel, done: make(chan struct{}), diagnostics: diagnostics, joinGrace: deps.joinGrace,
	}
	newWatcher := deps.watcher
	watcher, err := newWatcher(cfg.reloadPaths(), reloadDebounce, reloadMaxDebounce, func() {
		select {
		case events <- struct{}{}:
		default:
		}
	}, func(error) {
		diagnostics.Log(context.Background(), port.LevelWarn, "redis credential reload", "component", "redis", "operation", "watch", "outcome", "failed", "reason", "watch_error")
	})
	if err != nil {
		cancel()
		return nil, err
	}
	lifecycle.watcher = watcher
	go runReload(ctx, lifecycle.done, events, st, cfg, deps, diagnostics)
	return lifecycle, nil
}

func runReload(ctx context.Context, done chan<- struct{}, events <-chan struct{}, st *Store, cfg Config, deps storeDependencies, diagnostics port.Diagnostics) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
		}

	retryCycle:
		for {
			backoff := reloadInitialBackoff
			for attempt := 1; attempt <= reloadMaxAttempts; attempt++ {
				attemptCtx, cancel := context.WithCancel(ctx)
				result := make(chan error, 1)
				go func() { result <- reloadCandidate(attemptCtx, st, cfg, deps) }()
				var err error
				select {
				case err = <-result:
					cancel()
				case <-events:
					cancel()
					<-result
					continue retryCycle
				case <-ctx.Done():
					cancel()
					<-result
					return
				}
				if err == nil {
					diagnostics.Log(context.Background(), port.LevelInfo, "redis credential reload", "component", "redis", "operation", "reload", "outcome", "succeeded", "attempt", attempt)
					break retryCycle
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, errStoreClosed) {
					return
				}
				if attempt == reloadMaxAttempts {
					diagnostics.Log(context.Background(), port.LevelWarn, "redis credential reload", "component", "redis", "operation", "reload", "outcome", "failed", "attempts", attempt, "reason", "candidate_rejected")
					break retryCycle
				}
				diagnostics.Log(context.Background(), port.LevelInfo, "redis credential reload", "component", "redis", "operation", "reload", "outcome", "retrying", "attempt", attempt, "reason", "candidate_rejected")
				delay := reloadRetryDelay(attempt, backoff, deps)
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case <-events:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					continue retryCycle
				case <-timer.C:
				}
				backoff *= 2
				if backoff > reloadMaxBackoff {
					backoff = reloadMaxBackoff
				}
			}
		}
	}
}

func reloadRetryDelay(attempt int, backoff time.Duration, deps storeDependencies) time.Duration {
	delay := backoff
	if deps.backoff != nil {
		delay = deps.backoff(attempt)
	}
	if delay < 0 {
		return 0
	}
	if deps.jitter != nil {
		// The default jitter adds up to 25%, so leave that headroom before
		// jittering instead of flattening every positive maximum-base sample.
		maxJitterBase := reloadMaxBackoff * 4 / 5
		if delay > maxJitterBase {
			delay = maxJitterBase
		}
		delay = deps.jitter(delay)
	}
	if delay < 0 {
		return 0
	}
	if delay > reloadMaxBackoff {
		return reloadMaxBackoff
	}
	return delay
}

func reloadCandidate(ctx context.Context, st *Store, cfg Config, deps storeDependencies) error {
	conn, err := connectionConfigWithReader(cfg, deps.readFile)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, reloadProbeTimeout)
	defer cancel()
	factory := deps.candidate
	candidate, err := factory(probeCtx, &conn)
	if err != nil {
		return err
	}
	if err := probeCtx.Err(); err != nil {
		_ = candidate.Close()
		return err
	}
	if err := st.clients.swap(candidate); err != nil {
		_ = candidate.Close()
		return err
	}
	return nil
}

func (l *reloadLifecycle) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.watcher != nil {
			_ = l.watcher.Close()
		}
		l.cancel()
		timer := time.NewTimer(l.joinGrace)
		defer timer.Stop()
		select {
		case <-l.done:
		case <-timer.C:
			l.diagnostics.Log(context.Background(), port.LevelWarn, "redis credential reload shutdown",
				"component", "redis", "operation", "shutdown", "outcome", "timed_out",
				"reason", "reload_worker", "count", 1)
		}
	})
}
