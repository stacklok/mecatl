package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

// keyFrom marshals a request through the live buildParams path and extracts the
// emitted prompt_cache_key, so these tests pin the value that actually reaches
// the wire rather than the helper in isolation.
func keyFrom(t *testing.T, p *Provider, req port.LLMRequest) string {
	t.Helper()
	raw := marshalParams(t, p, req)
	const marker = `"prompt_cache_key":"`
	i := strings.Index(raw, marker)
	if i < 0 {
		t.Fatalf("no prompt_cache_key in params: %s", raw)
	}
	rest := raw[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated prompt_cache_key: %s", raw)
	}
	return rest[:j]
}

// TestADR_0343_CacheKeyUnsaltedWithoutOption pins AC3.4: a consumer that passes
// no WithCacheKeySalt Option keeps ADR 0100's exact derivation, so the module's
// zero value stays byte-identical and the change classifies as Added, not
// Changed (engine/COMPATIBILITY.md, ADR 0093).
func TestADR_0343_CacheKeyUnsaltedWithoutOption(t *testing.T) {
	req := cacheReq("gpt-5.2")
	p := New(WithCacheDialect(CacheDialectOpenAI))

	// Recompute ADR 0100's derivation independently rather than pinning a magic
	// string, so this fails on an algorithm change, not on a fixture edit.
	prefixSum := sha256.Sum256([]byte(req.System.StablePrefix))
	anchorSum := sha256.Sum256([]byte(anchorText(req.Messages)))
	want := "mecatl-" + hex.EncodeToString(prefixSum[:])[:12] + "-" + hex.EncodeToString(anchorSum[:])[:8]

	if got := keyFrom(t, p, req); got != want {
		t.Errorf("unsalted prompt_cache_key = %q, want the ADR 0100 derivation %q", got, want)
	}
}

// TestADR_0343_CacheKeySaltChangesKey is the adapter half of AC3.1: a distinct
// salt must produce a distinct key, which is the property that removes
// cross-principal correlation. The composition half (two app.Build instances
// differing without any config change) lives in internal/app.
func TestADR_0343_CacheKeySaltChangesKey(t *testing.T) {
	req := cacheReq("gpt-5.2")
	a := keyFrom(t, New(WithCacheDialect(CacheDialectOpenAI), WithCacheKeySalt("salt-a")), req)
	b := keyFrom(t, New(WithCacheDialect(CacheDialectOpenAI), WithCacheKeySalt("salt-b")), req)
	unsalted := keyFrom(t, New(WithCacheDialect(CacheDialectOpenAI)), req)

	if a == b {
		t.Errorf("two salts produced the same key %q — the correlation property is not removed", a)
	}
	if a == unsalted || b == unsalted {
		t.Errorf("a salted key equals the unsalted one (salted=%q unsalted=%q)", a, unsalted)
	}
	if !strings.HasPrefix(a, "mecatl-") {
		t.Errorf("salting must not change the key SHAPE, got %q", a)
	}
	// The salt must never appear in the emitted request.
	if strings.Contains(marshalParams(t, New(WithCacheDialect(CacheDialectOpenAI), WithCacheKeySalt("salt-a")), req), "salt-a") {
		t.Error("the salt itself must never reach the wire")
	}
}

// TestADR_0343_CacheKeySaltDomainSeparated pins the NUL domain separator: without
// it, (salt+prefixHead, prefixTail) and (salt, prefix) would collide, letting a
// chosen prefix impersonate a different installation's salt.
func TestADR_0343_CacheKeySaltDomainSeparated(t *testing.T) {
	base := cacheReq("gpt-5.2")
	shifted := cacheReq("gpt-5.2")
	shifted.System = prompt.Layered{StablePrefix: "X" + base.System.StablePrefix, VolatileSuffix: base.System.VolatileSuffix}

	a := keyFrom(t, New(WithCacheDialect(CacheDialectOpenAI), WithCacheKeySalt("sX")), base)
	b := keyFrom(t, New(WithCacheDialect(CacheDialectOpenAI), WithCacheKeySalt("s")), shifted)
	if a == b {
		t.Error("salt‖prefix is ambiguous: a shifted split collided, so the domain separator is missing")
	}
}

// TestUnifiedPromptCache_Scenario3_KeyStableAcrossTurnsAndCompaction pins AC3.2:
// salting must not disturb the two properties ADR 0100 relies on — byte-stability
// within a run (the memo path included) and per-conversation separation, which is
// what keeps concurrent Subagent children off one routing lane.
func TestUnifiedPromptCache_Scenario3_KeyStableAcrossTurnsAndCompaction(t *testing.T) {
	p := New(WithCacheDialect(CacheDialectOpenAI), WithCacheKeySalt("fixed-salt"))
	req := cacheReq("gpt-5.2")

	first := keyFrom(t, p, req)
	// A later turn appends messages; the prefix and the anchor are unchanged, so
	// the key must not move. This also exercises the memo hit.
	grown := cacheReq("gpt-5.2")
	grown.Messages = append(grown.Messages,
		session.NewUserMessage("and now the next step"),
		session.NewAssistantMessage("on it", "", nil),
	)
	if got := keyFrom(t, p, grown); got != first {
		t.Errorf("key moved across turns: %q then %q", first, got)
	}

	// Compaction rewrites the tail but preserves the first genuine user turn, so
	// the anchor — and therefore the key — survives.
	compacted := cacheReq("gpt-5.2")
	compacted.Messages = []session.Message{
		session.NewUserMessage("Project instructions (AGENTS.md):\nfollow the layering rules"),
		session.NewUserMessage("Please summarise the architecture."),
		session.NewAssistantMessage("a summary of everything so far", "", nil),
	}
	if got := keyFrom(t, p, compacted); got != first {
		t.Errorf("key moved across compaction: %q then %q", first, got)
	}

	// A different conversation on the same prefix must still separate.
	child := cacheReq("gpt-5.2")
	child.Messages = []session.Message{session.NewUserMessage("a totally different child task")}
	if got := keyFrom(t, p, child); got == first {
		t.Error("a different conversation shares a key — per-conversation separation lost")
	}
}
