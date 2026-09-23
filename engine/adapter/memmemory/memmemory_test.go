package memmemory_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memconformance"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestMemoryStoreConformance(t *testing.T) {
	memconformance.Run(t, func(*testing.T) tool.MemoryStore { return memmemory.New() })
}

// These assertions are compiled from an external package using only the engine
// module's public imports.
var (
	_ tool.MemoryStore             = memmemory.New()
	_ prompt.OperatorProfileSource = memmemory.New()
)
