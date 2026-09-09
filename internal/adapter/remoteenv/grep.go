package remoteenv

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/tool"
)

// grepInMemory scans the namespace's files for a regex, optionally filtered by
// a path glob. It mirrors the memfs in-memory grep so the contract proof shows
// the fake's Grep observes the same namespace as Read/Write/Shell.
func grepInMemory(ctx context.Context, n *namespace, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("remoteenv: invalid grep pattern: %w", err)
	}
	n.mu.RLock()
	keys := make([]string, 0, len(n.files))
	if pathGlob == "" {
		for k := range n.files {
			keys = append(keys, k)
		}
	} else {
		pat := normalizeGlobPattern(pathGlob)
		for k := range n.files {
			if pat == "" || globMatch(pat, k) {
				keys = append(keys, k)
			}
		}
	}
	n.mu.RUnlock()
	sort.Strings(keys)

	var matches []tool.GrepMatch
	for _, rel := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, _, rerr := n.read(rel)
		if rerr != nil {
			continue
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			continue // binary
		}
		lineNo := 0
		for _, line := range strings.Split(string(data), "\n") {
			lineNo++
			if re.MatchString(line) {
				matches = append(matches, tool.GrepMatch{Path: rel, Line: lineNo, Text: line})
			}
		}
	}
	return matches, nil
}
