// Package memledger provides a concurrent in-memory reference implementation of
// tool.ReadLedger (ADR 0294). It is the default a Workspace selects when no
// durable ledger is configured, and the offline fixture the shared
// engine/adapter/ledgerconformance suite validates. It never returns
// tool.ErrLedgerUnavailable: an in-memory map cannot go unavailable or corrupt,
// so RecordRead/RecordedVersion only ever succeed.
package memledger

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/tool"
)

// Ledger is a concurrency-safe in-memory tool.ReadLedger. Each Ledger instance
// is an independent evidence scope: two Ledgers never share state, which is
// what lets two Workspaces over one file-content backend select two isolated
// ledgers (ADR 0294 Scenario 1).
type Ledger struct {
	mu      sync.Mutex
	entries map[string]tool.FileVersion
}

// compile-time assertion that Ledger satisfies the port.
var _ tool.ReadLedger = (*Ledger)(nil)

// New returns a fresh, empty in-memory ReadLedger.
func New() *Ledger {
	return &Ledger{entries: make(map[string]tool.FileVersion)}
}

// RecordRead stores a valid version under key. It performs no file-content I/O.
func (l *Ledger) RecordRead(_ context.Context, key string, version tool.FileVersion) error {
	if _, err := tool.EncodeFileVersion(version); err != nil {
		return err
	}
	l.mu.Lock()
	l.entries[key] = version
	l.mu.Unlock()
	return nil
}

// RecordedVersion returns the version previously recorded for key. ok is false
// when key was never recorded; err is always nil (an in-memory map has no
// unavailable/corrupt state to report).
func (l *Ledger) RecordedVersion(_ context.Context, key string) (tool.FileVersion, bool, error) {
	l.mu.Lock()
	version, ok := l.entries[key]
	l.mu.Unlock()
	return version, ok, nil
}
