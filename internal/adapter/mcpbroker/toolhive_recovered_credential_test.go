package mcpbroker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestRecoveredCredentialSourceReissuesOnlyAfterExpiryAndStopsWhenInvalidated(t *testing.T) {
	var valid atomic.Bool
	valid.Store(true)
	var issued atomic.Int32
	source := &recoveredCredentialSource{
		active:   valid.Load,
		validate: func(context.Context) error { return nil },
		issue: func(_ context.Context) (*oauth2.Token, error) {
			issued.Add(1)
			return &oauth2.Token{AccessToken: "fresh", Expiry: time.Now().Add(time.Minute)}, nil
		},
	}
	first, err := source.Token()
	if err != nil || first.AccessToken != "fresh" || issued.Load() != 1 {
		t.Fatalf("first Token = %#v, %v; issued=%d", first, err, issued.Load())
	}
	if _, err := source.Token(); err != nil || issued.Load() != 1 {
		t.Fatalf("cached Token = %v; issued=%d", err, issued.Load())
	}
	source.mu.Lock()
	source.token.Expiry = time.Now().Add(-time.Second)
	source.mu.Unlock()
	if _, err := source.Token(); err != nil || issued.Load() != 2 {
		t.Fatalf("expired Token = %v; issued=%d", err, issued.Load())
	}
	valid.Store(false)
	if _, err := source.Token(); err == nil || issued.Load() != 2 {
		t.Fatalf("invalidated Token = %v; issued=%d", err, issued.Load())
	}
	source.close()
	valid.Store(true)
	if _, err := source.Token(); err == nil || issued.Load() != 2 {
		t.Fatalf("closed Token = %v; issued=%d", err, issued.Load())
	}
}
