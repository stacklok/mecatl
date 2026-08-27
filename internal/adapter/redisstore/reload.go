package redisstore

import (
	"context"
	"errors"
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
)

type reloadLifecycle struct {
	watcher *filewatch.Watcher
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

func (cfg Config) reloadEnabled() bool {
	return cfg.Reload && (cfg.CAFile != "" || cfg.UsernameFile != "" || cfg.PasswordFile != "")
}

func (cfg Config) reloadPaths() []string {
	paths := make([]string, 0, 3)
	for _, path := range []string{cfg.CAFile, cfg.UsernameFile, cfg.PasswordFile} {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func startReloadLifecycle(st *Store, cfg Config) (*reloadLifecycle, error) {
	diagnostics := cfg.Diagnostics
	if diagnostics == nil {
		diagnostics = port.NopDiagnostics{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	lifecycle := &reloadLifecycle{cancel: cancel, done: make(chan struct{})}
	watcher, err := filewatch.New(cfg.reloadPaths(), reloadDebounce, reloadMaxDebounce, func() {
		select {
		case events <- struct{}{}:
		default:
		}
	}, func(err error) {
		diagnostics.Log(context.Background(), port.LevelWarn, "redis credential reload", "component", "redis", "operation", "watch", "outcome", "failed", "err", err.Error())
	})
	if err != nil {
		cancel()
		return nil, err
	}
	lifecycle.watcher = watcher
	go runReload(ctx, lifecycle.done, events, st, cfg, diagnostics)
	return lifecycle, nil
}

func runReload(ctx context.Context, done chan<- struct{}, events <-chan struct{}, st *Store, cfg Config, diagnostics port.Diagnostics) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
		}

		backoff := reloadInitialBackoff
		for attempt := 1; attempt <= reloadMaxAttempts; attempt++ {
			attemptCtx, cancel := context.WithCancel(ctx)
			result := make(chan error, 1)
			go func() { result <- reloadCandidate(attemptCtx, st, cfg) }()
			var err error
			select {
			case err = <-result:
				cancel()
			case <-events:
				cancel()
				<-result
				attempt = 0
				backoff = reloadInitialBackoff
				continue
			case <-ctx.Done():
				cancel()
				<-result
				return
			}
			if err == nil {
				diagnostics.Log(context.Background(), port.LevelInfo, "redis credential reload", "component", "redis", "operation", "reload", "outcome", "succeeded", "attempt", attempt)
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, errStoreClosed) {
				return
			}
			if attempt == reloadMaxAttempts {
				diagnostics.Log(context.Background(), port.LevelWarn, "redis credential reload", "component", "redis", "operation", "reload", "outcome", "failed", "attempts", attempt, "reason", "candidate_rejected")
				break
			}
			diagnostics.Log(context.Background(), port.LevelInfo, "redis credential reload", "component", "redis", "operation", "reload", "outcome", "retrying", "attempt", attempt, "reason", "candidate_rejected")
			timer := time.NewTimer(backoff)
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
				attempt = 0
				backoff = reloadInitialBackoff
				continue
			case <-timer.C:
			}
			backoff *= 2
			if backoff > reloadMaxBackoff {
				backoff = reloadMaxBackoff
			}
		}
	}
}

func reloadCandidate(ctx context.Context, st *Store, cfg Config) error {
	conn, err := connectionConfig(cfg)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, reloadProbeTimeout)
	defer cancel()
	factory := cfg.candidateFactory
	if factory == nil {
		factory = func(ctx context.Context, cfg *tcredis.Config) (redis.UniversalClient, error) {
			return tcredis.NewClient(ctx, cfg)
		}
	}
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
		_ = l.watcher.Close()
		l.cancel()
		<-l.done
	})
}
