# Playbook: pprof cookbook + worked examples

Read this when you need the exact profiling commands or want to see the discipline
applied end to end. Two worked examples below: one real **win**, one principled
**NO-GO** — the NO-GO matters as much as the win.

## pprof flag cookbook

```sh
# Capture (allocation profile — the usual one for this codebase, alloc-first):
go test -run='^$' -bench=BenchmarkX -benchmem -memprofile=.scratch/x.mprof -count=3 ./pkg/

# Capture CPU instead, when ns/op is the question (advisory only on shared HW):
go test -run='^$' -bench=BenchmarkX -cpuprofile=.scratch/x.cpu -count=3 ./pkg/

# Read — bytes allocated (alloc_space) vs object count (alloc_objects):
go tool pprof -alloc_space   -top -nodecount=25 .scratch/x.mprof
go tool pprof -alloc_objects -top -nodecount=25 .scratch/x.mprof

# Drill to a function, line by line (the money command — shows file:line + bytes):
go tool pprof -list=renderConversation .scratch/x.mprof
go tool pprof -list='Build$'           .scratch/x.mprof

# Cumulative vs flat: -cum sorts by cumulative (callers); default is flat (self).
go tool pprof -alloc_space -cum -top .scratch/x.mprof
```

Notes:
- `-alloc_space` answers "where do the BYTES go", `-alloc_objects` answers "where do
  the ALLOCATIONS (allocs/op) go". Gate on objects; both inform the fix.
- A site at 90%+ cumulative with a small flat is a **caller** — follow it down with
  `-list` to the real allocating line.
- Profiles are sampled. Use `-count=3`+ so the hot site is unambiguous.

## benchstat

```sh
go install golang.org/x/perf/cmd/benchstat@latest
go test -run='^$' -bench=BenchmarkX -benchmem -count=10 ./pkg/ > .scratch/before.txt
# apply change
go test -run='^$' -bench=BenchmarkX -benchmem -count=10 ./pkg/ > .scratch/after.txt
benchstat .scratch/before.txt .scratch/after.txt
```

Read the `vs base` column + the `±` (p-value/CI). A drop inside the noise band is not
a win. `count=10` is the floor for a credible delta.

---

## Worked example 1 — a real WIN: the TUI scrollback join cache

**Symptom.** `tui_scrollback_view` scenario: ~33 MB and ~6,400 allocs to render 400
blocks once. The bench comment *assumed* the cost was `vp.SetContent` (the
"O(scrollback)" line-split).

**Profile (followed, not guessed).**
```
go tool pprof -alloc_space -top  ->  strings.(*Builder).WriteString = 90.91%
go tool pprof -list=renderConversation  ->  b.WriteString(r.renderBlock(...)) = 4.80GB cum
```
The 91% was the per-frame `strings.Builder` **join** re-copying all already-cached
block strings into a fresh string every frame. `SetContent` was only ~15 MB —
**the hypothesis was wrong.** The per-block cache already worked (only the 1 live
block re-rendered, ~8.6%).

**Fix.** Cache the joined frame and reuse it on any frame where no block
re-rendered. The reuse signal already existed: `blockRenders` increments only on
a per-block cache miss, and every render-visible mutation bumps a block `rev` →
miss → `blockRenders++`. Unchanged `blockRenders` is the load-bearing guard;
layout keys (block count, width) are belt-and-suspenders.

**Proof.** The *streaming* bench mutates the live block every op → all-miss → flat
(it's the worst-case floor, correctly unchanged). A *steady-frame* bench
(`BenchmarkScrollbackViewSteady`, the cursor-move/scroll/`renderInput` path) showed
the win: **B/op −99.6% (32.5 MB → 137 KB), allocs/op −46%.** Golden suite: zero
diffs (byte-identical). Mutation test: forcing unconditional reuse of the cached join
turned 10 tests red.

**Lessons.**
1. The profile overruled the hypothesis — always `-list` to the real line.
2. A streaming/all-miss benchmark hides a cache win; add a steady-state bench.
3. Mutation-test the cache guard, or the green tests prove nothing.

---

## Worked example 2 — a principled NO-GO: prompt-inventory memoization

**Symptom.** `BenchmarkBuildLargeCatalog` = 224 allocs / 31 KB, and `prompt.Build`
runs every turn. Tempting to memoize the rendered tool inventory across turns.

**Profile.** Confirmed the 224 allocs are the per-tool `fmt.Fprintf` + `firstLine`
split in `toolInventory` — exactly as hypothesized this time.

**Why it was rejected anyway.**
- `prompt.Build` is a pure free function; `cfg.Tools` is rebuilt every turn, so there
  is no cheap *stable key* — computing a correct memo key costs about as much as the
  render it would save.
- Hanging a memo on the `Engine` adds **stateful invalidation surface** against the
  **byte-stable prompt prefix** invariant — and a prefix bug silently tanks the
  provider cache-hit rate, the single most expensive regression in an LLM harness.
- The win is microseconds-per-turn — **invisible next to the network round-trip** the
  turn already pays.

**Verdict.** NO-GO on the memo. The only endorsed change was a stateless,
byte-identical micro-reduction (`fmt.Fprintf` → `WriteString`, `strings.Split` →
`IndexByte`/`Cut`), and even that is optional/marginal.

**Lesson.** "It allocates" is not "optimize it." Weigh the win against the real cost
and the risk to invariants. **The wrong abstraction is worse than the allocation** —
a clean profile-confirmed hotspot can still be a correct skip.

---

[← back to the skill](../SKILL.md)