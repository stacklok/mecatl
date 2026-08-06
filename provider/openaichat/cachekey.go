package openaichat

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

// prefixMemo caches the last-seen (StablePrefix, its hex sha256) pair so the
// common steady-state call — StablePrefix is byte-identical across every
// turn of one Engine.Run — pays only a string-equality memcmp, never a
// re-hash of 10-30KB of text.
//
// Duplicated from provider/openai/cachekey.go — separate Go modules, same
// discipline as isContextOverflowMessage; keep the two in lockstep by hand.
type prefixMemo struct {
	prefix string
	hash   string
}

// promptCacheKey derives the prompt_cache_key (ADR 0100):
//
//	"mecatl-" + hex(sha256(stablePrefix))[:12] + "-" + hex(sha256(anchorText))[:8]
//
// See provider/openai/cachekey.go's promptCacheKey for the full rationale
// (prefix half buckets by system/tools prefix; anchor half makes the key
// PER-CONVERSATION so concurrent Subagent children sharing the same prefix
// don't collide on one routing lane).
func (p *Provider) promptCacheKey(stablePrefix string, msgs []session.Message) string {
	prefixHash := p.prefixHash(stablePrefix)
	anchorSum := sha256.Sum256([]byte(anchorText(msgs)))
	return "mecatl-" + prefixHash[:12] + "-" + hex.EncodeToString(anchorSum[:])[:8]
}

// prefixHash returns the hex sha256 of prefix, memoised on the Provider via a
// single atomic.Pointer — compared by string equality (a memcmp, far cheaper
// than re-hashing) rather than an unbounded map keyed on a 10-30KB string.
func (p *Provider) prefixHash(prefix string) string {
	if m := p.cacheMemo.Load(); m != nil && m.prefix == prefix {
		return m.hash
	}
	sum := sha256.Sum256([]byte(prefix))
	hash := hex.EncodeToString(sum[:])
	p.cacheMemo.Store(&prefixMemo{prefix: prefix, hash: hash})
	return hash
}

// anchorText returns the Text of the first message in msgs that is NOT a
// leading harness-injected turn-0 context fragment (RoleUser messages
// matching prompt.IsInjectedTurn0Fragment) — the genuine first user
// instruction, or "" when msgs is empty or every message is a leading
// fragment.
//
// Duplicated from provider/openai/cachekey.go (not exported from
// engine/prompt) — separate Go modules, same discipline as
// isContextOverflowMessage; keep the two in lockstep by hand.
func anchorText(msgs []session.Message) string {
	for _, m := range msgs {
		if m.Role == session.RoleUser && prompt.IsInjectedTurn0Fragment(m.Text) {
			continue
		}
		return m.Text
	}
	return ""
}
