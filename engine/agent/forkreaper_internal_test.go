package agent

import (
	"runtime"
	"testing"
)

func TestLRUForkReaperCloseWaitsForDetachedEvictionCleanup(t *testing.T) {
	r := NewLRUForkReaper(1)
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	r.Preserve("/first", func() error {
		close(cleanupStarted)
		<-releaseCleanup
		return nil
	})

	preserveDone := make(chan struct{})
	go func() {
		r.Preserve("/second", func() error { return nil })
		close(preserveDone)
	}()
	<-cleanupStarted

	closeReturned := make(chan struct{})
	go func() {
		r.Close()
		close(closeReturned)
	}()

	for {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if closed {
			break
		}
		runtime.Gosched()
	}
	select {
	case <-closeReturned:
		t.Fatal("Close returned before the detached eviction cleanup finished")
	default:
	}

	close(releaseCleanup)
	<-preserveDone
	<-closeReturned
}
