// Package search provides reference SearchProvider adapters that travel with the
// importable engine core. fakesearch.go is the deterministic, OFFLINE fake used by
// the agent loop's tests (and any engine consumer's tests) to exercise the
// WebSearch tool without any network access — the mockllm of web search: scripted,
// deterministic, safe for concurrent use.
//
// It lives under engine/adapter/* so core test files may import it under the
// layering rules (the depguard core rules exclude $test; the DAG test reads
// non-test imports only). It is stdlib-only.
package search

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/tool"
)

// Fake is a deterministic, scripted tool.SearchProvider for offline tests. It
// returns a fixed result set (or a scripted error) and records the last
// SearchQuery it received so a test can assert site/freshness/limit passthrough.
// It is safe for concurrent use (the read-parallel dispatcher fans out concurrent
// WebSearch calls).
type Fake struct {
	mu      sync.Mutex
	results []tool.SearchResult
	err     error
	last    tool.SearchQuery
	calls   int
}

// NewFake constructs a Fake scripted to return results (and no error). Use the
// option-style setters (WithError / WithResults) or the constructor variants for
// other cases.
func NewFake(results ...tool.SearchResult) *Fake {
	return &Fake{results: results}
}

// NewFakeError constructs a Fake scripted to return err from every Search call
// (e.g. tool.ErrSearchUnavailable to drive the not-configured path, or an
// arbitrary backend fault).
func NewFakeError(err error) *Fake {
	return &Fake{err: err}
}

// Search returns the scripted results (or the scripted error), recording q so a
// test can later assert what the tool passed through.
func (f *Fake) Search(_ context.Context, q tool.SearchQuery) ([]tool.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = q
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	// Return a copy so a caller cannot mutate the scripted slice.
	out := make([]tool.SearchResult, len(f.results))
	copy(out, f.results)
	return out, nil
}

// LastQuery returns the most recent SearchQuery the fake received. Safe for
// concurrent use.
func (f *Fake) LastQuery() tool.SearchQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// Calls returns how many times Search was invoked. Safe for concurrent use.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Compile-time assertion that *Fake implements tool.SearchProvider.
var _ tool.SearchProvider = (*Fake)(nil)

// Unavailable is the honest "no search backend configured" SearchProvider: every
// Search returns tool.ErrSearchUnavailable. Reason, when set, is model-visible
// operator configuration context; an empty Reason preserves the --websearch=off
// wording used by the explicit kill switch.
type Unavailable struct {
	Reason string
}

// Search always returns tool.ErrSearchUnavailable.
func (Unavailable) Search(_ context.Context, _ tool.SearchQuery) ([]tool.SearchResult, error) {
	return nil, tool.ErrSearchUnavailable
}

// Compile-time assertion that Unavailable implements tool.SearchProvider.
var _ tool.SearchProvider = Unavailable{}
