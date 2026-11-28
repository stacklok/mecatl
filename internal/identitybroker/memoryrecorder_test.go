package identitybroker

import (
	"context"
	"slices"
	"sync"
)

// maxMemoryRecords bounds each kind of MemoryRecorder's records.
const maxMemoryRecords = 10000

// MemoryRecorder is the tests' Recorder: it holds the most recent records in
// memory, up to maxMemoryRecords of each kind, dropping the oldest, and
// accepts every record. Production records through the audit sink
// (adapter/auditsink). Its methods are safe for concurrent use.
type MemoryRecorder struct {
	mu            sync.Mutex
	opens         []SessionOpenRecord
	closes        []SessionCloseRecord
	registrations []RegistrationRecord
	delegations   []DelegationRecord
	idjags        []IDJAGRecord
	decisions     []DecisionRecord
}

// NewMemoryRecorder returns an empty MemoryRecorder.
func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{} }

// Record implements Recorder. It keeps a copy of rec.
func (r *MemoryRecorder) Record(_ context.Context, rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch rec := rec.(type) {
	case *SessionOpenRecord:
		r.opens = appendBounded(r.opens, rec.clone())
	case *SessionCloseRecord:
		r.closes = appendBounded(r.closes, *rec)
	case *RegistrationRecord:
		r.registrations = appendBounded(r.registrations, *rec)
	case *DelegationRecord:
		r.delegations = appendBounded(r.delegations, *rec)
	case *IDJAGRecord:
		r.idjags = appendBounded(r.idjags, *rec)
	case *DecisionRecord:
		r.decisions = appendBounded(r.decisions, *rec)
	}
	return nil
}

// Opens returns a copy of the session open records, oldest first.
func (r *MemoryRecorder) Opens() []SessionOpenRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SessionOpenRecord, len(r.opens))
	for i := range r.opens {
		out[i] = r.opens[i].clone()
	}
	return out
}

// clone returns a copy of r that shares no memory with it.
func (r *SessionOpenRecord) clone() SessionOpenRecord {
	c := *r
	if c.User != nil {
		u := *c.User
		c.User = &u
	}
	if c.Replaced != nil {
		g := *c.Replaced
		c.Replaced = &g
	}
	return c
}

// Closes returns a copy of the session close records, oldest first.
func (r *MemoryRecorder) Closes() []SessionCloseRecord { return cloneLocked(&r.mu, &r.closes) }

// Registrations returns a copy of the registration records, oldest first.
func (r *MemoryRecorder) Registrations() []RegistrationRecord {
	return cloneLocked(&r.mu, &r.registrations)
}

// Delegations returns a copy of the delegation records, oldest first.
func (r *MemoryRecorder) Delegations() []DelegationRecord { return cloneLocked(&r.mu, &r.delegations) }

// IDJAGs returns a copy of the ID-JAG records, oldest first.
func (r *MemoryRecorder) IDJAGs() []IDJAGRecord { return cloneLocked(&r.mu, &r.idjags) }

// Decisions returns a copy of the decision records, oldest first.
func (r *MemoryRecorder) Decisions() []DecisionRecord { return cloneLocked(&r.mu, &r.decisions) }

// cloneLocked returns a copy of *list, read under mu. It takes a pointer so
// that the slice header too is read with mu held.
func cloneLocked[T any](mu *sync.Mutex, list *[]T) []T {
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(*list)
}

// appendBounded appends rec to list, dropping the oldest records past
// maxMemoryRecords.
func appendBounded[T any](list []T, rec T) []T {
	list = append(list, rec)
	if len(list) > maxMemoryRecords {
		list = list[len(list)-maxMemoryRecords:]
	}
	return list
}
