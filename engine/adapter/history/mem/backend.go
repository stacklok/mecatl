package mem

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

// Backend is a concurrent append-only in-memory history.Backend. Construct it
// with New; its zero value is not usable and it must not be copied after use.
// All accepted bytes are retained unchanged until the backend is discarded.
// Acceptance is process-local, not durable across process loss.
type Backend struct {
	mu        sync.RWMutex
	histories map[history.HistoryID][]history.ByteCommit
}

var _ history.Backend = (*Backend)(nil)

// New returns an empty isolated byte backend, with no codec or retry policy.
func New() *Backend {
	return &Backend{histories: make(map[history.HistoryID][]history.ByteCommit)}
}

// Observe reads the head and requested ordinal under one lock. An absent history
// has zero evidence. Returned references contain no mutable storage.
func (s *Backend) Observe(ctx context.Context, id history.HistoryID, sequence int64) (history.Observation, error) {
	if err := checkScope(ctx, id); err != nil {
		return history.Observation{}, err
	}
	if sequence < 0 {
		return history.Observation{}, invalid("sequence must not be negative")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return history.Observation{}, err
	}
	return observe(s.histories[id], sequence), nil
}

// observe requires a read or write lock and a nonnegative ordinal. Entries are
// immutable after acceptance, and their one-based positions are their sequences.
func observe(entries []history.ByteCommit, sequence int64) history.Observation {
	var o history.Observation
	if len(entries) > 0 {
		o.Head = entries[len(entries)-1].Ref
		if sequence > 0 && sequence <= int64(len(entries)) {
			o.Commit = entries[sequence-1].Ref
		}
	}
	return o
}

// Append copies opaque bytes only on successful expected-head comparison. It
// validates index metadata, not canonical encoding or parent links. A failed CAS
// returns coherent head/ordinal evidence without copying or retrying the write.
func (s *Backend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, commit history.ByteCommit) (history.Observation, error) {
	if err := checkScope(ctx, id); err != nil {
		return history.Observation{}, err
	}
	if (expected != (history.CommitRef{}) && expected.Validate() != nil) || commit.Ref.Validate() != nil ||
		expected.Sequence == math.MaxInt64 || commit.Ref.Sequence != expected.Sequence+1 || len(commit.Bytes) == 0 {
		return history.Observation{}, invalid("invalid append metadata")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return history.Observation{}, err
	}
	entries := s.histories[id]
	o := observe(entries, commit.Ref.Sequence)
	if o.Head != expected {
		return o, nil
	}
	owned := bytes.Clone(commit.Bytes)
	if err := ctx.Err(); err != nil {
		return history.Observation{}, err
	}
	s.histories[id] = append(entries, history.ByteCommit{Ref: commit.Ref, Bytes: owned})
	return history.Observation{Head: commit.Ref, Commit: commit.Ref}, nil
}

// Read verifies indexed boundaries and captures immutable entries under one lock.
// It sizes the result and checks every byte/count cap before allocating or copying
// commit storage. Errors return nil output, including on a per-commit cap breach.
func (s *Backend) Read(ctx context.Context, id history.HistoryID, request history.ByteReadRequest) ([]history.ByteCommit, error) {
	if err := checkScope(ctx, id); err != nil {
		return nil, err
	}
	entries, err := s.readEntries(ctx, id, request)
	if err != nil {
		return nil, err
	}
	var page []history.ByteCommit

	remaining, count := request.MaxBytes, 0
	for _, e := range entries[:min(len(entries), request.MaxCommits)] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(e.Bytes) > request.MaxCommitBytes {
			return nil, invalid("stored commit exceeds read byte cap")
		}
		if len(e.Bytes) > remaining {
			if count == 0 {
				return nil, history.PageTooSmallError{RequiredBytes: len(e.Bytes)}
			}
			break
		}
		remaining -= len(e.Bytes)
		count++
	}
	if count > 0 {
		page = make([]history.ByteCommit, count)
		for i, e := range entries[:count] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page[i] = history.ByteCommit{Ref: e.Ref, Bytes: bytes.Clone(e.Bytes)}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

// readEntries validates the request and both indexed boundaries, then captures
// immutable entries under one lock. Returned entries borrow backend storage for
// read-only use; errors return nil entries.
func (s *Backend) readEntries(ctx context.Context, id history.HistoryID, request history.ByteReadRequest) ([]history.ByteCommit, error) {
	if (request.After != (history.CommitRef{}) && request.After.Validate() != nil) ||
		request.Through.Validate() != nil || request.After.Sequence > request.Through.Sequence ||
		(request.After.Sequence == request.Through.Sequence && request.After != request.Through) ||
		request.MaxBytes <= 0 || request.MaxCommits <= 0 || request.MaxCommitBytes <= 0 {
		return nil, invalid("invalid byte read request")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries := s.histories[id]
	if len(entries) == 0 {
		return nil, history.ErrNotFound
	}
	if request.Through.Sequence > int64(len(entries)) || entries[request.Through.Sequence-1].Ref != request.Through {
		return nil, invalid("read endpoint is not accepted in history")
	}
	if request.After != (history.CommitRef{}) && entries[request.After.Sequence-1].Ref != request.After {
		return nil, invalid("read lower boundary is not accepted in history")
	}
	return entries[request.After.Sequence:request.Through.Sequence:request.Through.Sequence], nil
}

// checkScope validates public byte operations before map access or allocation.
func checkScope(ctx context.Context, id history.HistoryID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == (history.HistoryID{}) {
		return invalid("history ID must be set")
	}
	return nil
}

// invalid adds constant context without exposing caller-provided byte contents.
func invalid(reason string) error { return fmt.Errorf("%w: %s", history.ErrInvalid, reason) }
