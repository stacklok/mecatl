package openai

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
type prefixMemo struct {
	prefix string
	hash   string
}

// promptCacheKey derives the prompt_cache_key (ADR 0100):
//
//	"mecatl-" + hex(sha256(stablePrefix))[:12] + "-" + hex(sha256(anchorText))[:8]
//
// The prefix half buckets requests sharing the same system/tools prefix (the
// routing-lane hint OpenAI's ~15-req/min-per-key guidance wants); the anchor
// half — anchorText(msgs), the Text of the first message that is NOT a
// leading turn-0 fragment — makes the key PER-CONVERSATION. A prefix-only key
// would put the parent plus up to 8 concurrent Subagent children (which all
// share the explorer prefix) on one routing lane, where they would also evict
// each other from the shared 50-breakpoint window; per-conversation keys fall
// out for free because each child carries its own task prompt as anchor. The
// anchor is stable for the same reason compaction pins the first genuine user
// turn; if a degraded compaction ever drops it the key changes once and the
// cache rebuilds — fail-soft, no error path.
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
// fragment. The leading-run rule (stop at the first non-match) mirrors
// compaction's preservedHead discipline, the SAME rule the anthropic
// adapter's leadingFragmentEnd applies to derive its slot-2 breakpoint.
//
// Duplicated (not exported from engine/prompt) — separate Go modules, same
// discipline as isContextOverflowMessage; keep the two in lockstep by hand.
func anchorText(msgs []session.Message) string {
	for _, m := range msgs {
		if m.Role == session.RoleUser && prompt.IsInjectedTurn0Fragment(m.Text) {
			continue
		}
		return m.Text
	}
	return ""
}
