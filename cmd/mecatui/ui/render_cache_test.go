package ui

// Tests for the per-BLOCK render cache (renderer.blockCache): settled blocks
// join the conversation string from cache and only blocks whose rev/width/expand
// changed re-render. The conventions follow coalesce_test.go: fully offline,
// flushes driven by explicit renderTickMsg, no output polling — and pure
// renderer/conversation-level tests are preferred over whole-program teatest
// (which would have to sequence on FinalModel(), never poll tm.Output()).
//
// The centrepiece is the cache-equivalence ORACLE: after every conversation
// mutation, the cached renderer's output must be byte-identical to a fresh
// renderer's. It is the drift tripwire for the block.rev discipline — a mutator
// that bypasses a rev bump renders stale and fails here.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// newCacheRenderer builds a renderer at a fixed width, mirroring the model's
// post-WindowSizeMsg state, for the pure renderer-level tests.
func newCacheRenderer() *renderer {
	r := newRenderer(theme.New("aztec", theme.AztecPalette()))
	r.setWidth(100)
	return r
}

// assertCacheMatchesFresh asserts a persistent cached renderer's
// renderConversation output at ONE fixed expand state is byte-identical to a
// brand-new renderer's (same theme, same width) — the cache must be
// output-invisible. The expand state is FIXED per cached renderer on purpose:
// blockEntry keys on expand, so alternating expand states on a single renderer
// would overwrite every entry at the opposite state each render, making every
// render a fresh MISS — the oracle would then never exercise a cache HIT, and a
// deleted rev bump would pass (this exact vacuity was caught by mutation
// testing). The caller holds one cached renderer per expand state across the
// whole step sequence, so entries genuinely survive — and can go stale —
// between steps.
func assertCacheMatchesFresh(t *testing.T, step string, cached *renderer, c *conversation, expand bool) {
	t.Helper()
	got := cached.renderConversation(c, expand)
	fresh := newRenderer(cached.th)
	fresh.setWidth(cached.width)
	want := fresh.renderConversation(c, expand)
	if got != want {
		t.Fatalf("step %q (expand=%v): cached render diverged from fresh render (stale cache entry — missing rev bump?)\n got: %q\nwant: %q",
			step, expand, stripANSIstr(got), stripANSIstr(want))
	}
}

// oracleSteps is the scripted sequence exercising EVERY conversation mutator.
// Each step's name is the conversation method it exercises (the reflection
// tripwire below checks the method set against these names); a step may call
// other already-covered methods as setup (e.g. addTool before setSubagentStart).
var oracleSteps = []struct {
	name string
	fn   func(c *conversation)
}{
	{"addUser", func(c *conversation) { c.addUser("hello there") }},
	{"addUserWithMedia", func(c *conversation) {
		c.addUserWithMedia("look at this", []string{"image/png (inline)"})
	}},
	{"startAssistant", func(c *conversation) { c.startAssistant() }},
	{"appendReasoning", func(c *conversation) { c.appendReasoning("weighing the options\n") }},
	{"appendAssistant", func(c *conversation) { c.appendAssistant("Here is **the** answer.\n") }},
	// reviseAssistant REPLACES the live block's body (vs appendAssistant's grow); it
	// rides the same currentAssistant() rev-bump gateway, so the oracle must see its
	// render stay cache-equivalent after the replace. Open a fresh assistant block
	// first so it targets a live block rather than opening one itself.
	{"reviseAssistant", func(c *conversation) {
		c.startAssistant()
		c.reviseAssistant("Revised **answer** body.\n")
	}},
	{"endReasoningStream", func(c *conversation) { c.endReasoningStream() }},
	{"addTurnStat", func(c *conversation) { c.addTurnStat("turn 1 · ↑1.2k ↓300 · 2.1s") }},
	{"addTool", func(c *conversation) { c.addTool("call-1", "Read", `{"path":"main.go"}`) }},
	{"addNotice", func(c *conversation) { c.addNotice("context compacted") }},
	// resolveTool covers BOTH shapes: the NON-TAIL resolve of call-1 (the notice
	// above sits after it) and a fresh tail resolve.
	{"resolveTool", func(c *conversation) {
		c.resolveTool("call-1", "package main\n", false) // NON-tail
		c.addTool("call-2", "Bash", `{"command":"go vet ./..."}`)
		c.resolveTool("call-2", "ok", false) // tail
	}},
	{"setSubagentStart", func(c *conversation) {
		c.addTool("call-sub", "Subagent", `{"goal":"dig"}`)
		c.setSubagentStart("call-sub", "dig into the code", "", "", "")
	}},
	{"addSubagentTool", func(c *conversation) { c.addSubagentTool("call-sub", "Grep", false, 1) }},
	{"setSubagentEnd", func(c *conversation) {
		c.setSubagentEnd("call-sub", client.Usage{InputTokens: 1200, OutputTokens: 340}, 3, "end_turn", 4200)
	}},
	// The fleet accumulators mutate conversation state OFF the blocks (footer /
	// ctrl+a roster); they must leave the block render untouched.
	{"fleetStart", func(c *conversation) { c.fleetStart("child-1", "dig into the code", "", "", "", false) }},
	{"fleetTool", func(c *conversation) { c.fleetTool("child-1", "Grep", false, 1) }},
	{"fleetEnd", func(c *conversation) {
		c.fleetEnd("child-1", client.Usage{InputTokens: 1200, OutputTokens: 340}, 3, "end_turn", 4200)
	}},
	{"setTeamStart", func(c *conversation) {
		c.addTool("call-team", "Team", `{"goal":"review"}`)
		c.setTeamStart("call-team", "team-p1", []client.TeamMemberSpec{
			{Name: "alpha", Role: "researcher", Lead: true},
			{Name: "beta", Role: "reviewer", Mutating: true},
		})
	}},
	// addTeamMember: all five InnerKinds routed to the lanes.
	{"addTeamMember", func(c *conversation) {
		base := client.TeamMsg{ParentCallID: "call-team", TeamID: "team-p1", Member: "alpha"}
		msg := base
		msg.InnerKind = "message.delta"
		msg.Text = "scanning the diff"
		c.addTeamMember(msg)
		msg = base
		msg.InnerKind = "tool.call"
		msg.ToolName = "Grep"
		msg.Detail = "pattern: foo"
		c.addTeamMember(msg)
		msg = base
		msg.InnerKind = "tool.result"
		msg.ToolName = "Grep"
		msg.Detail = "3 matches"
		c.addTeamMember(msg)
		msg = base
		msg.InnerKind = "turn.end"
		msg.Usage = client.Usage{InputTokens: 800, OutputTokens: 120}
		msg.ContextWindow = 200000
		c.addTeamMember(msg)
		msg = base
		msg.InnerKind = "result"
		msg.Text = "round done"
		msg.Usage = client.Usage{InputTokens: 100, OutputTokens: 40}
		c.addTeamMember(msg)
	}},
	{"setTeamTasks", func(c *conversation) {
		c.setTeamTasks("call-team", []client.TeamTask{
			{ID: "t1", Description: "scan", State: "done", Assignee: "alpha", Deps: []string{"t0"}},
		})
	}},
	{"setTeamFindings", func(c *conversation) {
		c.setTeamFindings("call-team", []client.TeamFinding{{Member: "alpha", Body: "found it"}})
	}},
	{"setTeamEnd", func(c *conversation) {
		c.setTeamEnd("call-team", "team-p1", 2, "end_turn",
			client.Usage{InputTokens: 2000, OutputTokens: 600},
			[]client.TeamMemberDisposition{{Name: "beta", Stopped: true, Reason: "budget"}})
	}},
	// The parallel accumulators likewise live off the blocks (ctrl+a Parallel tab).
	{"parallelStart", func(c *conversation) { c.parallelStart("call-par", "first", 2) }},
	{"parallelBranchStart", func(c *conversation) {
		c.parallelBranchStart("call-par", 0, "parallel-call-par-0", "fast", "try the fast path", "", "", "")
	}},
	{"parallelBranchTool", func(c *conversation) { c.parallelBranchTool("call-par", 0, "Bash", false, 1) }},
	{"parallelBranchEnd", func(c *conversation) {
		c.parallelBranchEnd("call-par", 0, "parallel-call-par-0",
			client.Usage{InputTokens: 500, OutputTokens: 90}, 2, "end_turn", false, "/forks/fork-0", 900)
	}},
	{"parallelEnd", func(c *conversation) { c.parallelEnd("call-par", "first", 2, 0, "/forks/fork-0", "end_turn") }},
	{"addHook", func(c *conversation) { c.addHook("blocked by PreToolUse hook", "PreToolUse", "Bash", "blocked") }},
	{"addError", func(c *conversation) { c.addError("stream failed: boom") }},
}

// oracleNonMutators are the *conversation methods the oracle does not drive as
// steps: pure reads, plus the rev-bump gateways and the lazy lane/group
// accessors, which are exercised INSIDE the mutator steps (currentAssistant via
// appendAssistant/appendReasoning/endReasoningStream, subagentBlock via the
// setSubagent* trio, teamBlock via the five setTeam*/addTeamMember mutators,
// fleetLane via fleet*, parallelGroupFor via parallel*). A NEW conversation
// method fails TestConversationMutatorsCoveredByOracle until it is either added
// as an oracle step or consciously listed here.
var oracleNonMutators = map[string]string{
	"isEmpty":             "pure read",
	"currentAssistant":    "rev-bump gateway, driven via appendAssistant/appendReasoning/endReasoningStream",
	"subagentBlock":       "rev-bump gateway, driven via the setSubagent* steps",
	"teamBlock":           "rev-bump gateway, driven via the setTeam*/addTeamMember steps",
	"fleetLane":           "lazy accessor, driven via the fleet* steps",
	"parallelGroupFor":    "lazy accessor, driven via the parallel* steps",
	"subagentFleetCounts": "pure read",
	"hasSubagents":        "pure read",
	"parallelGroupCounts": "pure read",
	"hasParallel":         "pure read",
	"liveParallel":        "pure read",
	"latestTeamBlock":     "overlay READ path (never bumps rev)",
	"liveTeamBlock":       "overlay READ path (never bumps rev)",
}

// TestBlockCacheOutputMatchesFreshRender is THE ORACLE: after EVERY conversation
// mutator the cached renderer's full conversation render must be byte-identical
// to a fresh renderer's, at both expand states. Any mutation path that changes a
// render-visible block field without bumping block.rev leaves a stale cache
// entry and fails here.
func TestBlockCacheOutputMatchesFreshRender(t *testing.T) {
	c := &conversation{}
	// TWO persistent cached renderers, one per expand state, held across the whole
	// step sequence — each is only ever rendered at ITS expand state, so its cache
	// entries survive between steps and a settled block genuinely HITS. (A single
	// renderer alternating expand states would overwrite every entry per render —
	// all misses, a vacuous oracle; see assertCacheMatchesFresh.)
	cachedCollapsed := newCacheRenderer()
	cachedExpanded := newCacheRenderer()
	check := func(step string) {
		t.Helper()
		assertCacheMatchesFresh(t, step, cachedCollapsed, c, false)
		assertCacheMatchesFresh(t, step, cachedExpanded, c, true)
	}
	check("(empty)")
	for _, step := range oracleSteps {
		step.fn(c)
		check(step.name)
	}
}

// TestConversationMutatorsCoveredByOracle is the drift tripwire: it enumerates
// *conversation's method set from the source (the methods are unexported, so
// reflection can't see them — go/ast can) and asserts each one is either an
// oracle step or consciously listed in oracleNonMutators. A future mutator
// fails this test until it is added to the oracle. It scans EVERY non-test .go
// file in this package directory (not just conversation.go), so a conversation
// method added in another file can't evade it.
//
// What it still can't see: free functions taking a *conversation, methods on
// *block / *teamLane / etc. that mutate render-visible fields, and writes
// through pointers RETURNED by a method (e.g. the overlay holding a *block).
// The guard for those is the documented rule on block.rev — mutate blocks only
// through conversation methods — plus the rev-bump gateway doc comments; the
// equivalence oracle above catches the resulting staleness when such a path is
// exercised through a covered mutator.
func TestConversationMutatorsCoveredByOracle(t *testing.T) {
	covered := map[string]bool{}
	for _, s := range oracleSteps {
		covered[s.name] = true
	}
	for name := range oracleNonMutators {
		covered[name] = true
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			if receiverTypeName(fd.Recv.List[0].Type) != "conversation" {
				continue
			}
			found++
			if !covered[fd.Name.Name] {
				t.Errorf("conversation method %q (%s) is not covered by the render-cache oracle: add it to oracleSteps (mutator) or oracleNonMutators (read path) in render_cache_test.go", fd.Name.Name, name)
			}
		}
	}
	if found < 20 {
		t.Fatalf("enumerated only %d conversation methods — the go/ast tripwire is likely broken", found)
	}
}

// receiverTypeName unwraps a method receiver's AST type to its bare identifier
// ("conversation" for both value and pointer receivers).
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// TestSettledBlocksRenderOnceDuringStreaming proves the headline win at the
// model level: with a scrollback of one settled block of each kind, streaming
// deltas with frame flushes re-render ONLY the live assistant block — each
// flush bumps blockRenders by exactly one, and no settled block ever
// re-renders.
func TestSettledBlocksRenderOnceDuringStreaming(t *testing.T) {
	m := newCoalesceModel(t)
	// One settled block of each kind (user+media, resolved tool, notice, hook,
	// turn-stat, error, prior assistant).
	m.conv.addUserWithMedia("look at this", []string{"image/png (inline)"})
	m.conv.addTool("call-1", "Read", `{"path":"main.go"}`)
	m.conv.resolveTool("call-1", "package main\n", false)
	m.conv.addNotice("context compacted")
	m.conv.addHook("hook note", "PreToolUse", "Bash", "info")
	m.conv.addTurnStat("turn 1 · 1.2s")
	m.conv.addError("transient error")
	m.conv.startAssistant()
	m.conv.appendAssistant("a prior, settled assistant turn")
	// The live assistant block the stream grows.
	m.conv.startAssistant()
	m.refreshView() // settle: every block renders once and populates the cache

	base := m.rend.blockRenders
	const flushes = 4
	frags := deltaTexts(flushes * 5)
	for i := 0; i < flushes; i++ {
		m = applyAll(m, frags[i*5:(i+1)*5]...)
		m = applyAll(m, renderTickMsg{}) // explicit flush, per coalesce_test conventions
	}
	if got := m.rend.blockRenders - base; got != flushes {
		t.Fatalf("expected exactly %d block renders across %d flushes (live block only; settled blocks must hit the cache), got %d",
			flushes, flushes, got)
	}

	// And the cache is output-invisible: the cached render matches a fresh one.
	got := m.rend.renderConversation(&m.conv, m.expandTools)
	fresh := newRenderer(m.rend.th)
	fresh.setWidth(m.rend.width)
	if want := fresh.renderConversation(&m.conv, m.expandTools); got != want {
		t.Errorf("cached render diverged from fresh render after streaming:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// cacheTestConversation builds a small mixed conversation for the invalidation
// tests: a user prompt, a notice, and a resolved tool card.
func cacheTestConversation() *conversation {
	c := &conversation{}
	c.addUser("hello")
	c.addNotice("a notice")
	c.addTool("call-1", "Read", `{"path":"main.go"}`)
	c.resolveTool("call-1", "package main\n", false)
	return c
}

// TestBlockCacheInvalidatesOnWidthChange: settled blocks re-render exactly once
// after a width change, and the result matches a fresh render at the new width.
func TestBlockCacheInvalidatesOnWidthChange(t *testing.T) {
	c := cacheTestConversation()
	r := newCacheRenderer()
	r.renderConversation(c, false)
	base := r.blockRenders

	// Same width again: all hits, no re-render.
	r.renderConversation(c, false)
	if r.blockRenders != base {
		t.Fatalf("unchanged re-render must be all cache hits, got %d extra renders", r.blockRenders-base)
	}

	r.setWidth(80)
	got := r.renderConversation(c, false)
	if n := r.blockRenders - base; n != len(c.blocks) {
		t.Errorf("width change should re-render every block exactly once: got %d renders, want %d", n, len(c.blocks))
	}
	fresh := newRenderer(r.th)
	fresh.setWidth(80)
	if want := fresh.renderConversation(c, false); got != want {
		t.Errorf("post-width-change render diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestBlockCacheInvalidatesOnExpandToggle: settled blocks re-render exactly once
// after the ctrl+t expand flip, and the result matches a fresh render.
func TestBlockCacheInvalidatesOnExpandToggle(t *testing.T) {
	c := cacheTestConversation()
	r := newCacheRenderer()
	r.renderConversation(c, false)
	base := r.blockRenders

	got := r.renderConversation(c, true)
	if n := r.blockRenders - base; n != len(c.blocks) {
		t.Errorf("expand flip should re-render every block exactly once: got %d renders, want %d", n, len(c.blocks))
	}
	fresh := newRenderer(r.th)
	fresh.setWidth(r.width)
	if want := fresh.renderConversation(c, true); got != want {
		t.Errorf("post-expand render diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestNonTailMutationInvalidatesOnlyThatBlock: resolving a tool whose block has
// later blocks appended after it (the backward-scan resolveTool case) re-renders
// ONLY that block, and the output matches a fresh render.
func TestNonTailMutationInvalidatesOnlyThatBlock(t *testing.T) {
	c := &conversation{}
	c.addTool("call-1", "Bash", `{"command":"go test ./..."}`)
	c.addUser("a later block")
	c.addNotice("an even later block")
	r := newCacheRenderer()
	r.renderConversation(c, false)
	base := r.blockRenders

	if !c.resolveTool("call-1", "ok\n", false) {
		t.Fatal("resolveTool failed to match call-1")
	}
	got := r.renderConversation(c, false)
	if n := r.blockRenders - base; n != 1 {
		t.Errorf("non-tail resolve should re-render exactly the mutated block: got %d renders, want 1", n)
	}
	fresh := newRenderer(r.th)
	fresh.setWidth(r.width)
	if want := fresh.renderConversation(c, false); got != want {
		t.Errorf("post-resolve render diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestNonTailResolveRendersThroughUpdateFlow is the model-level twin of the
// pure-renderer non-tail test, driven through the REAL Update flow
// (coalesce_test-style): a tool call lands, a LATER block is appended after it
// (so the eventual resolve hits a non-tail block via resolveTool's backward
// scan), the cache is warmed, and then the tool result + an explicit
// renderTickMsg flush arrive. The rendered conversation must show the resolved
// result — a missing rev bump would leave the warmed unresolved card cached.
// The View goldens can never cover this class: they never warm a cache on a
// block that is subsequently mutated non-tail.
func TestNonTailResolveRendersThroughUpdateFlow(t *testing.T) {
	m := newCoalesceModel(t)
	m = applyAll(m, client.ToolCallMsg{ID: "call-1", Name: "Bash", Args: `{"command":"go test ./..."}`})
	// A later block after the call (resolveTool will scan backwards past it).
	m.conv.addUser("a later prompt")
	m.refreshView() // warm the cache: unresolved card + the later block
	m = applyAll(m,
		client.ToolResultMsg{CallID: "call-1", Content: "ok: 12 passed", IsError: false},
		renderTickMsg{},
	)
	got := m.rend.renderConversation(&m.conv, m.expandTools)
	if !strings.Contains(stripANSIstr(got), "ok: 12 passed") {
		t.Errorf("non-tail resolve through Update must render the result (stale cached card?), got:\n%s", stripANSIstr(got))
	}
	fresh := newRenderer(m.rend.th)
	fresh.setWidth(m.rend.width)
	if want := fresh.renderConversation(&m.conv, m.expandTools); got != want {
		t.Errorf("cached render diverged from fresh after the non-tail resolve:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestResetSessionDropsRenderCaches: resetSession (/clear, the /models
// restart-now handoff) must empty BOTH per-block caches, because a rebuilt
// conversation reuses indices 0..n with rev starting at 0 again — a stale entry
// would alias an old block's render onto a different new block. The rebuild
// half exercises exactly that aliasing shape.
func TestResetSessionDropsRenderCaches(t *testing.T) {
	m := newCoalesceModel(t)
	m.conv.appendAssistant("the first transcript's answer")
	m.conv.addUser("the first transcript's prompt")
	m.refreshView()
	if len(m.rend.blockCache) == 0 || len(m.rend.blockMD) == 0 {
		t.Fatalf("precondition: both caches should be populated, got blockCache=%d blockMD=%d",
			len(m.rend.blockCache), len(m.rend.blockMD))
	}

	m = m.resetSession()
	if len(m.rend.blockCache) != 0 {
		t.Errorf("resetSession must empty blockCache, got %d entries", len(m.rend.blockCache))
	}
	if len(m.rend.blockMD) != 0 {
		t.Errorf("resetSession must empty blockMD, got %d entries", len(m.rend.blockMD))
	}

	// Rebuild a DIFFERENT conversation at the SAME indices (rev 0 again): without
	// the reset, index 0 (previously an assistant block at rev 0) would alias.
	m.conv.addUser("a completely different prompt")
	m.conv.addNotice("a different notice")
	m.refreshView()
	got := m.rend.renderConversation(&m.conv, m.expandTools)
	fresh := newRenderer(m.rend.th)
	fresh.setWidth(m.rend.width)
	if want := fresh.renderConversation(&m.conv, m.expandTools); got != want {
		t.Errorf("post-reset rebuild diverged from fresh render (index aliasing?):\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
	if !strings.Contains(stripANSIstr(got), "a completely different prompt") {
		t.Error("rebuilt conversation should render the NEW prompt, not an aliased stale block")
	}
}

// Tests for the whole-conversation JOIN cache (renderer.joinCache/joinValid/joinKey),
// the outer memo layer above blockCache: a frame that re-renders no block reuses the
// previous joined string verbatim instead of rebuilding the Builder, and any change
// (width, expand, block append, live-block mutation) invalidates it and produces a
// fresh, correct join. The byte-identical guarantee is the same as blockCache — the
// fresh-renderer ORACLE asserts it; these tests assert the cache is actually USED and
// correctly INVALIDATED.

// TestJoinCacheReusedWhenNothingChanged: a second render with no intervening
// conversation mutation must hit the join fast path — joinValid stays true, the
// signature is unchanged, and the result is byte-identical to the first render (and
// to a fresh renderer's).
func TestJoinCacheReusedWhenNothingChanged(t *testing.T) {
	c := cacheTestConversation()
	r := newCacheRenderer()
	first := r.renderConversation(c, false)
	if !r.joinValid {
		t.Fatal("joinValid should be set after the first render")
	}
	keyAfterFirst := r.joinKey
	base := r.blockRenders

	second := r.renderConversation(c, false)
	if r.blockRenders != base {
		t.Fatalf("unchanged re-render must re-render no block, got %d extra", r.blockRenders-base)
	}
	if r.joinKey != keyAfterFirst {
		t.Errorf("join key should be unchanged on a fast-path hit: was %+v, now %+v", keyAfterFirst, r.joinKey)
	}
	if second != first {
		t.Errorf("join fast path must return the identical string:\n got %q\nwant %q",
			stripANSIstr(second), stripANSIstr(first))
	}
	// And it equals a fresh renderer's (the byte-identical guarantee).
	fresh := newRenderer(r.th)
	fresh.setWidth(r.width)
	if want := fresh.renderConversation(c, false); second != want {
		t.Errorf("join-cached render diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(second), stripANSIstr(want))
	}
}

// TestJoinCacheReuseHasNoScrollbackAllocs proves the WIN at the cache level: a
// fast-path hit on a large settled scrollback performs near-zero allocations (it
// returns the cached string), whereas the building frame allocates the full join.
// We assert the hit allocates dramatically less than a fresh full join of the same
// conversation — the observable that a stale-serve mutation (always rebuilding, or
// never caching) would erase.
func TestJoinCacheReuseHasNoScrollbackAllocs(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 80; i++ {
		c.addUser("question " + strconv.Itoa(i))
		c.addNotice("notice " + strconv.Itoa(i))
	}
	r := newCacheRenderer()
	r.renderConversation(c, false) // warm both caches and the join

	// Reference: the SAME renderer forced to rebuild the join each iteration (the
	// block caches stay warm, so this isolates the join Builder cost). It must
	// allocate substantially more than the fast-path hit below.
	ref := newCacheRenderer()
	ref.renderConversation(c, false) // warm block caches
	fullJoin := testing.AllocsPerRun(20, func() {
		ref.joinValid = false // force the rebuild path; block caches still hit
		ref.renderConversation(c, false)
	})

	hit := testing.AllocsPerRun(20, func() { r.renderConversation(c, false) })
	if hit >= fullJoin {
		t.Fatalf("join fast-path hit (%v allocs) should allocate far less than a forced rebuild (%v allocs) — cache not used?", hit, fullJoin)
	}
	if hit > 4 {
		t.Errorf("join fast-path hit should allocate ~0 (returns the cached string), got %v allocs/op", hit)
	}
}

// TestJoinCacheInvalidatesOnWidthChange: a width change must rebuild the join and
// produce a fresh, correct result.
func TestJoinCacheInvalidatesOnWidthChange(t *testing.T) {
	c := cacheTestConversation()
	r := newCacheRenderer()
	r.renderConversation(c, false)

	r.setWidth(80)
	got := r.renderConversation(c, false)
	fresh := newRenderer(r.th)
	fresh.setWidth(80)
	if want := fresh.renderConversation(c, false); got != want {
		t.Errorf("post-width-change join diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestJoinCacheInvalidatesOnExpandToggle: flipping the expand toggle must rebuild
// the join (the key carries expand) and produce a fresh, correct result.
func TestJoinCacheInvalidatesOnExpandToggle(t *testing.T) {
	c := cacheTestConversation()
	r := newCacheRenderer()
	r.renderConversation(c, false)

	got := r.renderConversation(c, true)
	fresh := newRenderer(r.th)
	fresh.setWidth(r.width)
	if want := fresh.renderConversation(c, true); got != want {
		t.Errorf("post-expand-toggle join diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestJoinCacheInvalidatesOnBlockAppend: appending a block (nBlocks changes, and the
// new tail block re-renders bumping blockRenders) must rebuild the join with the new
// block included.
func TestJoinCacheInvalidatesOnBlockAppend(t *testing.T) {
	c := cacheTestConversation()
	r := newCacheRenderer()
	r.renderConversation(c, false)

	c.addUser("a freshly appended prompt")
	got := r.renderConversation(c, false)
	if !strings.Contains(stripANSIstr(got), "a freshly appended prompt") {
		t.Error("join after append must include the new block (stale join served?)")
	}
	fresh := newRenderer(r.th)
	fresh.setWidth(r.width)
	if want := fresh.renderConversation(c, false); got != want {
		t.Errorf("post-append join diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestJoinCacheInvalidatesOnLiveBlockMutation: mutating an EXISTING block (the live
// streaming case — block count unchanged, but a block.rev bump re-renders it and
// bumps blockRenders) must rebuild the join with the mutated content.
func TestJoinCacheInvalidatesOnLiveBlockMutation(t *testing.T) {
	c := &conversation{}
	c.addUser("a prompt")
	c.startAssistant()
	c.appendAssistant("first fragment")
	r := newCacheRenderer()
	r.renderConversation(c, false)

	c.appendAssistant(" — second fragment")
	got := r.renderConversation(c, false)
	if !strings.Contains(stripANSIstr(got), "second fragment") {
		t.Error("join after a live-block mutation must include the new content (stale join served?)")
	}
	fresh := newRenderer(r.th)
	fresh.setWidth(r.width)
	if want := fresh.renderConversation(c, false); got != want {
		t.Errorf("post-live-mutation join diverged from fresh:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// Tests for the INCREMENTAL-join line-slice path (renderer.renderConversationLines
// + joinPrefixLines/joinPrefixN/joinPrefixKey), the streaming fast path that reuses
// the cached prefix of settled blocks and rebuilds only the changed suffix. The
// byte-identical guarantee is the same fresh-renderer oracle as the whole-join memo:
// joinLines (the line slice rejoined with "\n") MUST equal a fresh renderer's
// renderConversation string. A stale prefix (the load-bearing failure mode) shows as
// a divergence from the oracle.

// joinLinesString runs the incremental line path on r and rejoins to a string,
// mirroring viewport.GetContent (strings.Join over the lines). The result must be
// byte-identical to renderConversation's monolithic join.
func joinLinesString(r *renderer, c *conversation, expand bool) string {
	return strings.Join(r.renderConversationLines(c, expand), "\n")
}

// freshConvString is the oracle: a brand-new renderer's renderConversation string
// at the given width/expand.
func freshConvString(th theme.Theme, width int, c *conversation, expand bool) string {
	fresh := newRenderer(th)
	fresh.setWidth(width)
	return fresh.renderConversation(c, expand)
}

// TestIncrementalJoinPrefixReused: a large settled scrollback with a live tail block
// mutated each frame. The incremental line join must equal the fresh-renderer oracle
// across several frames (the prefix is reused, the suffix rebuilt) — proving the
// reused prefix is never stale.
func TestIncrementalJoinPrefixReused(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 60; i++ {
		c.addUser("question " + strconv.Itoa(i))
		c.addNotice("notice " + strconv.Itoa(i))
	}
	c.startAssistant() // the live tail block
	r := newCacheRenderer()

	for frame := 0; frame < 5; frame++ {
		c.appendAssistant("tok" + strconv.Itoa(frame) + " ")
		got := joinLinesString(r, c, false)
		want := freshConvString(r.th, r.width, c, false)
		if got != want {
			t.Fatalf("frame %d: incremental line join diverged from fresh oracle\n got %q\nwant %q",
				frame, stripANSIstr(got), stripANSIstr(want))
		}
	}
}

// TestIncrementalJoinTailChangesMidScrollback (LOAD-BEARING): a NON-TAIL mutation —
// resolving a tool whose block has later blocks appended after it — must lower
// firstChanged to the mutated index so the cached prefix truncates BEFORE it. If the
// prefix were served stale (truncating only at the tail) the resolved card would
// still show as unresolved. Asserts byte-identical to the fresh oracle before AND
// after the non-tail resolve.
func TestIncrementalJoinTailChangesMidScrollback(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 30; i++ {
		c.addUser("early " + strconv.Itoa(i))
	}
	c.addTool("mid-call", "Bash", `{"command":"go test ./..."}`)
	// Later blocks AFTER the tool call, so resolveTool hits a NON-tail block.
	for i := 0; i < 10; i++ {
		c.addNotice("later " + strconv.Itoa(i))
	}
	r := newCacheRenderer()

	// Warm the prefix over the whole (unresolved) scrollback.
	if got, want := joinLinesString(r, c, false), freshConvString(r.th, r.width, c, false); got != want {
		t.Fatalf("pre-resolve incremental join diverged from fresh oracle\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}

	// Mutate a MIDDLE block: resolve the tool. firstChanged must lower to its index.
	if !c.resolveTool("mid-call", "ok: passed", false) {
		t.Fatal("resolveTool failed to match mid-call")
	}
	got := joinLinesString(r, c, false)
	want := freshConvString(r.th, r.width, c, false)
	if got != want {
		t.Fatalf("post-non-tail-resolve incremental join diverged from fresh oracle (stale prefix served past the changed index?)\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
	if !strings.Contains(stripANSIstr(got), "ok: passed") {
		t.Error("non-tail resolve must render the result through the incremental path (stale prefix?)")
	}
}

// TestIncrementalJoinWidthChangeDropsPrefix: a width change re-renders every block,
// so the cached prefix (keyed on width) must be dropped and rebuilt — the result
// stays byte-identical to a fresh renderer at the new width.
func TestIncrementalJoinWidthChangeDropsPrefix(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 20; i++ {
		c.addUser("a reasonably long prompt number " + strconv.Itoa(i) + " that will wrap differently at different widths")
	}
	r := newCacheRenderer()
	// Two renders at width 100 so the prefix is actually CACHED over the settled
	// blocks (frame 2 establishes it); the width change must then DROP it, not serve
	// it stale at the old width.
	joinLinesString(r, c, false)
	joinLinesString(r, c, false)
	if len(r.joinPrefixLines) == 0 {
		t.Fatal("precondition: the prefix should be cached at width 100 before the width change")
	}

	r.setWidth(60)
	got := joinLinesString(r, c, false)
	want := freshConvString(r.th, 60, c, false)
	if got != want {
		t.Fatalf("post-width-change incremental join diverged from fresh oracle (stale prefix at old width?)\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestIncrementalJoinExpandToggleDropsPrefix: flipping the expand toggle re-renders
// every block, so the cached prefix (keyed on expand) must be dropped and rebuilt.
func TestIncrementalJoinExpandToggleDropsPrefix(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 20; i++ {
		id := "c" + strconv.Itoa(i)
		c.addTool(id, "Read", `{"path":"f.go"}`)
		c.resolveTool(id, "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n", false)
	}
	r := newCacheRenderer()
	// Two renders at expand=false so the prefix is CACHED; the flip to expand=true must
	// DROP it, not serve the collapsed prefix stale.
	joinLinesString(r, c, false)
	joinLinesString(r, c, false)
	if len(r.joinPrefixLines) == 0 {
		t.Fatal("precondition: the prefix should be cached at expand=false before the toggle")
	}

	got := joinLinesString(r, c, true) // flip expand
	want := freshConvString(r.th, r.width, c, true)
	if got != want {
		t.Fatalf("post-expand-toggle incremental join diverged from fresh oracle (stale prefix at old expand?)\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestIncrementalJoinResetDropsPrefix (index-aliasing guard): resetBlockCaches must
// drop the incremental prefix too, because a rebuilt conversation reuses indices
// 0..n with fresh blocks — a retained prefix would alias old blocks' renders onto
// the new ones.
func TestIncrementalJoinResetDropsPrefix(t *testing.T) {
	c := &conversation{}
	c.addUser("the first transcript's prompt that is long enough to occupy a block")
	c.addNotice("the first transcript's notice")
	r := newCacheRenderer()
	// Render TWICE with the settled state stable: frame 1 is all-miss (cold cache,
	// firstChanged=0 → no prefix), frame 2 sees the settled blocks hit and caches the
	// prefix over them.
	joinLinesString(r, c, false)
	joinLinesString(r, c, false)
	if len(r.joinPrefixLines) == 0 {
		t.Fatal("precondition: the incremental prefix should be populated")
	}

	r.resetBlockCaches()
	if r.joinPrefixN != 0 || len(r.joinPrefixLines) != 0 {
		t.Fatalf("resetBlockCaches must drop the incremental prefix, got N=%d lines=%d",
			r.joinPrefixN, len(r.joinPrefixLines))
	}

	// Rebuild a DIFFERENT conversation at the SAME indices.
	c2 := &conversation{}
	c2.addUser("a completely different prompt")
	c2.addNotice("a different notice")
	got := joinLinesString(r, c2, false)
	want := freshConvString(r.th, r.width, c2, false)
	if got != want {
		t.Fatalf("post-reset rebuild diverged from fresh oracle (index aliasing in the prefix?)\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
	if !strings.Contains(stripANSIstr(got), "a completely different prompt") {
		t.Error("rebuilt conversation must render the NEW prompt, not an aliased stale prefix block")
	}
}

// TestRefreshViewLinesMatchString proves the refreshView line-slice fast path
// (SetContentLines) yields the SAME viewport content as the string SetContent path
// the gate falls back to. It drives the EXACT production gate
// (!m.sel.active && !m.expandTools) by actually setting m.sel.active and
// m.expandTools — the prior version of this test never set either, so it only ever
// drove the fast path and proved nothing about the fallback. This is the gate the
// path-switch stale-prefix bug exploits, so we must compare across it.
func TestRefreshViewLinesMatchString(t *testing.T) {
	build := func() Model {
		m := newCoalesceModel(t)
		m.conv.addUser("a user prompt")
		m.conv.addTool("call-1", "Read", `{"path":"main.go"}`)
		m.conv.resolveTool("call-1", "package main\n", false)
		m.conv.startAssistant()
		m.conv.appendAssistant("here is **the** answer with some length to it\n")
		return m
	}

	// Fast path (no selection, expand off): SetContentLines.
	mFast := build()
	mFast.refreshView()
	fastContent := mFast.vp.GetContent()
	if want := freshConvString(mFast.rend.th, mFast.rend.width, &mFast.conv, false); fastContent != want {
		t.Fatalf("SetContentLines fast path content diverged from the fresh oracle\n got %q\nwant %q",
			stripANSIstr(fastContent), stripANSIstr(want))
	}

	// Fallback path A — selection active. A COLLAPSED selection (anchor==head, no
	// span) routes refreshView through the string SetContent branch but applies no
	// styling, so GetContent must be byte-identical to the fast path.
	mSel := build()
	mSel.sel.active = true
	mSel.refreshView()
	if selContent := mSel.vp.GetContent(); selContent != fastContent {
		t.Fatalf("selection-active string path diverged from the line fast path\n sel %q\n fast %q",
			stripANSIstr(selContent), stripANSIstr(fastContent))
	}

	// Fallback path B — expandTools on. This re-renders every block at expand=true
	// (a DIFFERENT content from the collapsed fast path), so it compares against the
	// fresh oracle at expand=true. It exercises the OTHER half of the gate. We render
	// the line path at expand=true directly as well so both sides of the gate are
	// proven equal at expand=true.
	mExp := build()
	mExp.expandTools = true
	mExp.refreshView()
	expContent := mExp.vp.GetContent()
	if want := freshConvString(mExp.rend.th, mExp.rend.width, &mExp.conv, true); expContent != want {
		t.Fatalf("expandTools string path diverged from the fresh oracle at expand=true\n got %q\nwant %q",
			stripANSIstr(expContent), stripANSIstr(want))
	}

	// The string path (renderConversation → SetContent) over the same conversation
	// must produce the same GetContent as the fast path at expand=false.
	mStr := build()
	mStr.vp.SetContent(mStr.rend.renderConversation(&mStr.conv, false))
	if strContent := mStr.vp.GetContent(); strContent != fastContent {
		t.Fatalf("SetContentLines fast path diverged from the SetContent string path\n lines %q\n  str  %q",
			stripANSIstr(fastContent), stripANSIstr(strContent))
	}
}

// TestIncrementalJoinAllocatesOnlySuffix proves the WIN: over a LARGE settled
// scrollback with a live tail mutated each frame (the streaming shape), the
// incremental line path allocates dramatically FEWER BYTES than the old string-join
// path — because it reuses the cached prefix lines and never copies the whole
// scrollback into a fresh strings.Builder. The per-block caches stay warm in both,
// so the measurement isolates the join cost (the string Builder copy vs the
// prefix-reusing line assembly) — exactly the alloc half this change removes.
// Bytes, not allocs: the slices are buffer-reused, so the count of allocations is
// already low; the headline regression-observable is allocated BYTES.
func TestIncrementalJoinAllocatesOnlySuffix(t *testing.T) {
	build := func() (*renderer, *conversation) {
		c := &conversation{}
		for i := 0; i < 200; i++ {
			c.addUser("question " + strconv.Itoa(i))
			c.addNotice("notice " + strconv.Itoa(i))
		}
		r := newCacheRenderer()
		// Two stable renders: frame 1 all-miss (cold cache, no prefix), frame 2 caches
		// the full prefix over the settled blocks. No live block / no glamour churn, so
		// the per-frame cost below is purely the join (the cost this change targets).
		r.renderConversationLines(c, false)
		r.renderConversationLines(c, false)
		return r, c
	}

	bytesPerOp := func(fn func()) uint64 {
		res := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				fn()
			}
		})
		if res.N == 0 {
			return 0
		}
		return res.MemBytes / uint64(res.N)
	}

	// Reference: the OLD string-join path builds the whole scrollback into a fresh
	// strings.Builder every frame (the block caches stay warm, so this isolates the
	// Builder copy this change removes). joinValid is forced false so the whole-join
	// memo cannot short-circuit it.
	ref, refConv := build()
	full := bytesPerOp(func() {
		ref.joinValid = false
		ref.renderConversation(refConv, false)
	})

	// Measured: the incremental line path reuses the cached prefix verbatim — no
	// full-scrollback copy.
	r, c := build()
	incr := bytesPerOp(func() { r.renderConversationLines(c, false) })

	// The string path copies the entire scrollback into a Builder each frame; the line
	// path reuses the cached prefix slice. Require at least a 4x byte reduction — a
	// conservative tripwire that reverting to a full per-frame join would trip.
	if incr*4 >= full {
		t.Fatalf("incremental line path (%d B/op) should allocate far fewer bytes than a forced full string join (%d B/op) — prefix not reused / full copy restored?", incr, full)
	}
}

// TestPathSwitchStalePrefix (BLOCKER regression — cross-path prefix invalidation).
// The incremental-join prefix (joinPrefixLines) is maintained by the LINES path
// (renderConversationLines), but the STRING path (renderConversation, taken under
// selection/expand) re-renders the SAME blocks through the SHARED blockCache —
// without touching the prefix. So: lines path warms the prefix; a non-tail block
// mutates and a STRING-path frame re-renders it (blockCache updated, prefix left
// stale); the next lines-path frame HITS blockCache for that block (no blockRenders
// bump → firstChanged stays high → joinPrefixN matches), and the STALE prefix is
// served (e.g. a resolved "ok: passed" tool card still shown unresolved).
//
// The fix couples prefix invalidation to renderBlock — the single chokepoint BOTH
// paths funnel through — so a string-path re-render of a covered block drops the
// prefix before the next lines frame can serve it stale. This test reproduces the
// exact production sequence (select text → result lands → clear selection) and
// asserts byte-identical to the fresh oracle. It MUST fail before the fix.
func TestPathSwitchStalePrefix(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 20; i++ {
		c.addUser("early " + strconv.Itoa(i))
	}
	c.addTool("mid-call", "Bash", `{"command":"go test ./..."}`)
	for i := 0; i < 10; i++ {
		c.addNotice("later " + strconv.Itoa(i))
	}
	r := newCacheRenderer()

	// 1) LINES path: warm the prefix over the whole settled (unresolved) scrollback.
	//    Two frames so the settled blocks hit and the prefix actually caches them.
	r.renderConversationLines(c, false)
	r.renderConversationLines(c, false)
	if r.joinPrefixN == 0 {
		t.Fatal("precondition: the prefix must be cached over the settled scrollback")
	}

	// 2) A NON-TAIL block mutates (resolve a mid-scrollback tool)...
	if !c.resolveTool("mid-call", "ok: passed", false) {
		t.Fatal("resolveTool failed to match mid-call")
	}
	// ...and a STRING-path frame re-renders it (this is the selection/expand path).
	//    blockCache is now updated to the resolved card; WITHOUT the fix the prefix is
	//    left stale, still covering the resolved block at its OLD (unresolved) render.
	r.renderConversation(c, false)

	// 3) Back to the LINES path (selection cleared). The resolved block now HITS
	//    blockCache, so firstChanged stays high and the prefix would be served stale.
	got := joinLinesString(r, c, false)
	want := freshConvString(r.th, r.width, c, false)
	if got != want {
		t.Fatalf("path-switch served a STALE prefix: lines join diverged from fresh oracle after a string-path re-render of a non-tail block\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
	if !strings.Contains(stripANSIstr(got), "ok: passed") {
		t.Error("the resolved card must render through the line path after the path switch (stale prefix served?)")
	}
}

// TestIncrementalJoinMultiBlockOneFrame (firstChanged=MIN guard): in ONE frame BOTH
// a mid-scrollback block resolves AND the live tail appends. firstChanged must be
// the MINIMUM of the two changed indices (the mid block), so the prefix truncates
// before it; if firstChanged instead took the LAST/MAX changed index (the tail), the
// prefix would still cover the mutated mid block and serve it stale. Asserts
// byte-identical to the fresh oracle. MUST fail if walkBlocks takes max instead of
// min for firstChanged.
func TestIncrementalJoinMultiBlockOneFrame(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 20; i++ {
		c.addUser("early " + strconv.Itoa(i))
	}
	c.addTool("mid-call", "Read", `{"path":"f.go"}`)
	for i := 0; i < 8; i++ {
		c.addNotice("settled " + strconv.Itoa(i))
	}
	c.startAssistant() // the live tail block
	r := newCacheRenderer()

	// Warm a PARTIAL prefix: settled blocks + a live tail. Stream a couple of tokens
	// so the prefix caches the settled head and the tail is the only changing block.
	c.appendAssistant("warming ")
	r.renderConversationLines(c, false)
	c.appendAssistant("more ")
	r.renderConversationLines(c, false)
	if r.joinPrefixN == 0 {
		t.Fatal("precondition: a partial prefix over the settled head must be cached")
	}

	// ONE frame: BOTH resolve a mid block AND append to the live tail. firstChanged
	// must be the mid block's index (the MIN), not the tail's (the MAX).
	if !c.resolveTool("mid-call", "ok: passed", false) {
		t.Fatal("resolveTool failed to match mid-call")
	}
	c.appendAssistant("final ")

	got := joinLinesString(r, c, false)
	want := freshConvString(r.th, r.width, c, false)
	if got != want {
		t.Fatalf("multi-block frame: incremental join diverged from fresh oracle (firstChanged took max not min?)\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
	if !strings.Contains(stripANSIstr(got), "ok: passed") {
		t.Error("the mid-block resolve must render (firstChanged must be the MIN changed index)")
	}
}

// TestIncrementalJoinSteadyFrameAllocCeiling (steady-frame regression tripwire): the
// streaming-frame win (BenchmarkScrollbackView, ~−98% B/op) had a tripwire
// (TestIncrementalJoinAllocatesOnlySuffix) but the STEADY frame — the
// interaction-cadence re-render (keystroke / scroll / overlay toggle, where
// refreshView fires on viewDirty over an UNCHANGED conversation) — had none, so the
// +7% steady B/op was invisible to the gate. This asserts the steady-frame line path
// allocates a BOUNDED number of bytes per op: the prefix is fully cached (no block
// re-renders), so only the fresh per-frame line SLICE is allocated, never an
// O(scrollback) string copy. The ceiling scales with the line count, NOT a fixed
// constant, so it stays meaningful as the bench shape evolves.
func TestIncrementalJoinSteadyFrameAllocCeiling(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 200; i++ {
		c.addUser("question " + strconv.Itoa(i))
		c.addNotice("notice " + strconv.Itoa(i))
	}
	r := newCacheRenderer()
	// Two stable renders: frame 1 cold (no prefix), frame 2 caches the full prefix.
	r.renderConversationLines(c, false)
	r.renderConversationLines(c, false)
	if r.joinPrefixN != len(c.blocks) {
		t.Fatalf("precondition: the full prefix must be cached (joinPrefixN=%d, want %d)", r.joinPrefixN, len(c.blocks))
	}
	nLines := len(r.joinPrefixLines)

	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			// UNCHANGED conversation: the steady interaction-cadence frame. The prefix is
			// fully cached, so only the per-frame slice header + backing array allocates.
			r.renderConversationLines(c, false)
		}
	})
	if res.N == 0 {
		t.Skip("benchmark did not run")
	}
	perOp := res.MemBytes / uint64(res.N)

	// Ceiling: the fresh per-frame slice is one []string of ~nLines capacity (16 B per
	// element on 64-bit). Allow generous headroom (×4) so this is a regression tripwire
	// for "the steady frame copied the whole scrollback again", NOT a brittle exact
	// match. A full per-frame STRING join over 400 settled blocks allocates far more
	// than this bound.
	ceiling := uint64(nLines) * 16 * 4
	if perOp > ceiling {
		t.Fatalf("steady-frame line path allocates %d B/op, over the %d B/op ceiling (~%d lines) — full per-frame scrollback copy restored on the steady frame?",
			perOp, ceiling, nLines)
	}
}
