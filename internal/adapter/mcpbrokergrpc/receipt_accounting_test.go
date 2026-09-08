package mcpbrokergrpc

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestReceiptAccountingClearedByRetentionSweep(t *testing.T) {
	attachment := accountedTerminalAttachment(37, time.Now().Add(-time.Second))
	server := &Server{
		cfg:     Config{SweepInterval: time.Millisecond},
		handles: map[string]*serverAttachment{"expired": attachment},
		owners:  make(map[session.SessionID]*sessionOwner),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go server.sweep()
	t.Cleanup(func() {
		select {
		case <-server.stop:
		default:
			close(server.stop)
		}
		<-server.done
	})

	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		_, retained := server.handles["expired"]
		bytes, receipts := attachment.receiptBytes, len(attachment.receipts)
		server.mu.Unlock()
		if !retained {
			if bytes != 0 || receipts != 0 {
				t.Fatalf("swept receipt accounting = %d bytes/%d receipts", bytes, receipts)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("receipt was not swept")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReceiptAccountingClearedByShutdown(t *testing.T) {
	attachment := accountedTerminalAttachment(41, time.Now().Add(time.Hour))
	executeCtx, executeStop := context.WithCancel(context.Background())
	server := &Server{
		cfg:         Config{SweepInterval: time.Hour},
		handles:     map[string]*serverAttachment{"retained": attachment},
		owners:      make(map[session.SessionID]*sessionOwner),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		executeCtx:  executeCtx,
		executeStop: executeStop,
	}
	go server.sweep()
	if err := server.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if attachment.receiptBytes != 0 || len(attachment.receipts) != 0 {
		t.Fatalf("shutdown receipt accounting = %d bytes/%d receipts", attachment.receiptBytes, len(attachment.receipts))
	}
}

func accountedTerminalAttachment(bytes int, expiresAt time.Time) *serverAttachment {
	return &serverAttachment{
		expiresAt:    expiresAt,
		changed:      make(chan struct{}),
		receipts:     map[session.ToolCallID]*executeReceipt{"call": {bytes: bytes, done: make(chan struct{})}},
		receiptBytes: bytes,
		terminal:     lifecycleClose,
	}
}
