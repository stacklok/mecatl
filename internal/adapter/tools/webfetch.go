package tools

import webfetch "github.com/stacklok/mecatl/engine/adapter/webfetch"

// WebFetchTool is the importable engine WebFetch implementation re-exported for
// existing root-module consumers.
type WebFetchTool = webfetch.Tool

// NewWebFetchTool constructs WebFetch with its production network policy.
func NewWebFetchTool() webfetch.Tool {
	return webfetch.New()
}
