// Package memconformance provides the shared conformance suite for the mandatory
// versioned tool.MemoryStore contract.
package memconformance

import (
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// Run executes the full MemoryStore conformance suite.
func Run(t *testing.T, newStore func(t *testing.T) tool.MemoryStore) {
	t.Helper()
	RunLifecycle(t, newStore)
}
