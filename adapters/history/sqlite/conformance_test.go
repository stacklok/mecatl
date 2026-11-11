package sqlite

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/conformance"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func factory(t *testing.T) history.Backend {
	t.Helper()
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	return b
}
func TestConformance(t *testing.T) { conformance.Run(t, factory) }
func TestAllocation(t *testing.T)  { conformance.RunAllocationChecks(t, factory) }
