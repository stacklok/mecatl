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
		c.setSubagentStart("call-sub", "dig into the code")
	}},
	{"addSubagentTool", func(c *conversation) { c.addSubagentTool("call-sub", "Grep", false, 1) }},
	{"setSubagentEnd", func(c *conversation) {
		c.setSubagentEnd("call-sub", client.Usage{InputTokens: 1200, OutputTokens: 340}, 3, "end_turn", 4200)
	}},
	// The fleet accumulators mutate conversation state OFF the blocks (footer /
	// ctrl+a roster); they must leave the block render untouched.
	{"fleetStart", func(c *conversation) { c.fleetStart("child-1", "dig into the code", false) }},
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
		c.parallelBranchStart("call-par", 0, "parallel-call-par-0", "fast", "try the fast path")
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
