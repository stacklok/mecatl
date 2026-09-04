package main

import (
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mockscript"
)

// loadMockScript retains mecated's command-local seam while the bounded parser
// and deterministic provider are shared with mecak8s.
func loadMockScript(path string) (port.LLMProvider, error) {
	return mockscript.Load(path)
}
