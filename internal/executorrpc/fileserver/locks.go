package fileserver

import (
	"context"
	"sync"
)

// pathLocks serializes writes per path for the life of the server.
// An entry exists only while a caller holds or awaits it.
type pathLocks struct {
	mu   sync.Mutex
	held map[string]*pathLock
}

type pathLock struct {
	token chan struct{}
	refs  int
}

func (l *pathLocks) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	e := l.held[key]
	if e == nil {
		e = &pathLock{token: make(chan struct{}, 1)}
		l.held[key] = e
	}
	e.refs++
	l.mu.Unlock()
	select {
	case e.token <- struct{}{}:
		return func() {
			<-e.token
			l.release(key, e)
		}, nil
	case <-ctx.Done():
		l.release(key, e)
		return nil, ctx.Err()
	}
}

func (l *pathLocks) release(key string, e *pathLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(l.held, key)
	}
}
