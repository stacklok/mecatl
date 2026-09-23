# ADR 0010 — Semantic memory recall: BM25 shipped, semantic deferred

- Status: Accepted
- Date: 2026
- Scope: `internal/adapter/memory` (SearchMemory tool, BM25 ranking), `engine/tool` (MemoryStore.Search), assessment of semantic/embedding path.

## Context

The tier-0 index caps at 200 entries and trims oldest-first. With consolidation off by default, a long-lived project can exceed the cap and leave the model unable to discover trimmed entries by meaning. Semantic/embedding recall was evaluated but requires a cloud embeddings call, a heavy in-process model, or a local server — none of which satisfy the local-first, no-external-by-default posture. The keyword gap is a retrieval-surface problem, not a scale problem.

## Decision

Ship a pure-Go BM25 lexical SearchMemory tool as the blind-spot backstop: it searches all entries (including trimmed ones) by keyword across key, description, and value, ranks them, and returns key-description lines so the model then calls Recall for full values. SearchMemory is implemented as a store-owned `Search` method (ranking under the existing shared flock for atomicity) rather than a tool-over-List shape. Semantic/embedding recall is explicitly deferred; the buildable design (port.Embedder, OpenAI embedder adapter, cosine scan, lazy backfill, opt-in wiring) is retained in this record as the future path.

## Consequences

The model can find trimmed or forgotten-key entries by keyword with no external dependency, no new Go module, and no network egress. The three-step loop becomes index (see recent) → SearchMemory (find by keyword) → Recall (load full value). Semantic recall remains off; the trigger to revisit is a real store observed near the cap with consolidation already enabled. The buildable semantic design is preserved here for future reference. Current behaviour: docs/architecture.md. Shipped/deferred state: [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

---

---

## DECISION — local BM25 lexical search first; semantic deferred

> **BM25 lexical search shipped** ([#12](https://github.com/stacklok/mecatl/issues/12),
> commit `ea1dd69`). The `SearchMemory` tool is implemented and on by default
> whenever memory is enabled. Semantic / embedding recall remains **deferred** —
> the buildable design below is retained as the future path. One divergence from
> the increment design in this doc: ranking is **store-owned** (a `Search` method
> on `tool.MemoryStore`, ranking under the existing shared flock), not the
> tool-over-`List` shape the "Recommended increment" section first proposed — see
> the updated note in that section for why the "Future option" was taken from the
> start.

The decision evolved past the embeddings path. The maintainer's binding
constraint is **no extra API / lighter / local**, and neural/embedding semantic
recall meets none of those: it needs a cloud embeddings call, a heavy in-process
model (CGo/ONNX — the class of dependency that burned this repo with
tree-sitter-WASM), or a local server (Ollama, still an out-of-process API). So the
shipped near-term path is a **pure-Go BM25 / TF-IDF lexical search** — no API, no
model, no server, no new dependency, instant at this scale, fully offline-testable.
Its accepted tradeoff is lexical-not-semantic: it ranks by term relevance, not
meaning (no synonym matching), which is sufficient for a curated set of the user's
own one-line facts.

The semantic / embedding design below is **retained as the deferred future path**,
revisited only if lexical search proves insufficient (the model searching by
*meaning* and missing synonym hits); at that point the endpoint choice (local
Ollama vs cloud) is part of the decision. The original assessment (which also
recommended the cheaper path) is retained below as rationale of record. The
semantic design was scoped to be as clean and low-footprint as possible — it
neutralises the downsides the assessment raised:

- **"new dependency"** → neutralised: NO new dependency. The embedder reuses the
  already-vendored `github.com/openai/openai-go/v3` SDK (`go.mod:16`) — same
  client, base URL, and key as the LLM adapter; embeddings are a different
  endpoint on the *same* client (`client.Embeddings`, not a new import). Cosine
  is stdlib `math` only; we explicitly REJECT an ANN/vector-DB lib (§2).
- **"hard offline-test story"** → bounded and made honest: a deterministic
  hashed-bag-of-words mock embedder makes cosine reflect *token overlap*, so a
  test asserts real ranking ("test runner" ranks nearer "gotestsum preference"
  than "deploy gate"). The doc states plainly this validates mechanics, not
  semantic quality (§"Mock embedder").
- **"new on-disk format / migration"** → minimised: vectors are an *additive*
  field on the existing `record` (the same no-op-additive trick Task 2 used for
  `description`), written under the existing flock; missing/stale-model vectors
  are recomputed lazily.
- **"not free / cost-bearing"** → respected: semantic recall is **OFF by
  default / opt-in**, gated on an embedder being configured, consistent with the
  established posture that external/cost-bearing features (MCP) stay opt-in (§6).

## BIG DECISIONS (historical — for the deferred semantic path)

These knobs apply only if the deferred semantic path is ever taken up (the shipped
Task-3 answer is the BM25 `SearchMemory` — see DECISION above). Each has a
recommendation:

- **D-T3.1 — embed-on-write vs embed-lazily.** Recommendation: **embed-lazily**
  (compute missing vectors in one batch on the first `SemanticRecall`, persist
  them). Keeps `Remember` network-free and fast; confines the API call to the
  opt-in read path. (§4.)
- **D-T3.2 — vectors inline in `memory.json` vs a sidecar file.** Recommendation:
  **inline, additive `record.Embedding`** — but read into a *separate in-memory
  index* so the hot `Index`/`List`/`Recall` reads don't drag vectors. Sidecar is
  the fallback if value+vector in one document proves too heavy. (§4.)
- **D-T3.3 — default embedding model + dimensions.** Recommendation:
  **`text-embedding-3-small`** (cheapest OpenAI embedding, 1536-dim), with
  `Dimensions` left at the model default and BOTH model id and dim recorded per
  vector so a model/dim change invalidates and triggers re-embed. (§3, §4.)
- **D-T3.4 — does the store gain a vector method, or does the tool own cosine?**
  Recommendation: **store owns persistence AND cosine** via a new
  `SearchSemantic(ctx, queryVec, k)` method; the *tool* owns the embedder and
  embeds the query, then hands the store a query vector. This keeps the embedder
  dependency out of the store (store stays embedder-free) and the vectors inside
  the store (their owner). (§5.)
- **D-T3.5 — advertise semantic recall on the capabilities channel?**
  Recommendation: **yes** — add a `SemanticRecall` cap mirroring `Memory`
  (§6/§7), so the TUI can show the affordance honestly. Cheap and consistent.

---

## Assessment (the honest part)

### The stated gap

The tier-0 index (`engine/prompt/memoryindex.go:30-33`) caps at **200
entries / 8 KB**, trims **oldest-first** (`keepNewest`,
`memoryindex.go:162-175`), and prints a footer:
`...(N older entries not shown; Recall a key or prefix to load them)`
(`memoryindex.go:154`). Past the cap, trimmed entries vanish from the
always-in-context routing table. The model can still reach them — but only via
`Recall(exactKey)` or `Recall(keyPrefix)` (`tools.go:213-249`,
`store.go:262-280`). It cannot *see that they exist*, and it cannot search them
*by meaning or by any token that isn't in the key*. That is the blind spot.

This was already foreseen. `MEMORY-TIERING.md:544-548` (R3) accepted it:
*"hitting 200 entries signals the consolidator should be on… the cap is the
honest bound and the footer is the escape hatch."* So the design's own bet was:
**the consolidator keeps the set under the cap, so the blind spot is rarely
reached.** The assessment below tests that bet.

### How likely is a real project to exceed 200 entries?

Two forces push opposite directions:

- **Down:** the `dream` consolidator (`internal/adapter/dream/dream.go`) merges
  near-duplicates and prunes stale/low-value entries conservatively
  (`dream.go:188-251`). When ON, it keeps the set tight — exactly the
  pattern-4 garbage collection the corpus describes (02 §3, §4). It runs at
  `MaxEntries` default 200 (`dream.go:72`), so it actively works to hold the
  store at/under the tier-0 cap.
- **Up:** the Remember tool's description steers hard toward conservatism
  (`tools.go:36-69`: "be conservative", "the dominant memory failure mode"),
  which keeps writes rare. A curated per-project store of *durable user
  preferences and cross-cutting facts* genuinely struggles to reach 200
  legitimate entries — the corpus filter (07 §326-334) is narrow.

**The load-bearing fact: consolidation is OFF by default on BOTH surfaces.**

- `mecated`: `--memory-consolidate-interval` defaults to **0 = disabled**
  (`cmd/mecated/main.go:483`).
- `mecatui` (the default, embedded, "just works" surface): leaves
  `MemoryConsolidateInterval` **unset = 0** (`cmd/mecatui/main.go:190` comment
  confirms it is deliberately left default), and `startMemoryConsolidation`
  no-ops on interval ≤ 0 (`internal/app/build.go:957-959`).

So on the out-of-the-box experience, **the down-force is not running.** Memory
ON by default (Task 1) + consolidation OFF by default = a set that only grows.
Over-eager-memory steering slows growth but does not bound it. A long-lived
project that the model writes to occasionally *can* drift past 200 with the GC
switched off. The design's bet ("the consolidator keeps it tight") is not being
honoured by the default configuration.

That materially changes the verdict. If consolidation were ON by default, I
would say **defer** without hesitation — a tight curated set is enumerable by
construction and the corpus is explicit that you add semantic/vector retrieval
only "when memory exceeds what's enumerable" (07 §317-318), which a
consolidated curated set does not. With consolidation OFF, the cap *can* be hit
in normal use, so doing *something* now is defensible.

### Is a capped curated index "enumerable"?

Yes — by construction, that is exactly what tier-0 *is*: a complete, one-line
enumeration of the keys. The corpus reserves vector stores for "when memory
exceeds what's enumerable, or when retrieval needs to be semantic"
(07 §316-318). Neither condition holds for hundreds of one-line curated facts:

- **Enumerability:** `store.List(ctx, "")` already returns **every** entry
  (`store.go:260-280`). The store can be fully walked in one cheap call. The
  problem is not that the data isn't enumerable — it is that the *trimmed* part
  isn't *in the prompt*. That is a retrieval-surface problem, not a
  scale-exceeds-enumeration problem.
- **Semantic need:** these entries are short, namespaced, English one-liners
  (`pref/test-runner`, `project/deploy-gate`). A substring/keyword match over
  key + description + value finds them well. The corpus's anti-RAG argument
  (07 §282-297) is about *code* at hundreds-of-MB scale; it does not apply to a
  small prose store, but its conclusion ("greppable exploration won") points
  the same way: search the text you have before reaching for embeddings.

**Verdict: the blind spot is real but small, and it is a retrieval-surface gap,
not a scale gap.** The right response is the cheapest tool that lets the model
search the entries it already has — not a new tier and not semantic
infrastructure.

### Verdict

**Build now — the keyword `SearchMemory` tool (option 1).** It is the smallest
thing that genuinely closes the blind spot. Separately and with higher leverage
on the *root cause*, **consider turning consolidation on by default** with a
conservative interval — that attacks why the cap gets hit at all. I explicitly
recommend **against** tier-2 cold storage (option 3) and semantic/embedding
recall (option 4) now; both are scoped below so the choice is eyes-open.

If you would rather **defer entirely**, the defensible defer position is:
*"Ship nothing here; turn consolidation on by default; revisit search only when
a real store is observed past ~150 entries with consolidation already on."* The
trigger to flip from defer to build would be **a real user store observed near
the cap with consolidation enabled** — that is the signal that the curated set
genuinely exceeds what the index can enumerate, and only then. I do not
recommend the full defer, because the keyword tool is cheap enough (one
read-only tool, zero new interface methods, reuses `List`) that shipping it now
is lower total cost than tracking a trigger.

---

## Ranking the four options (cheapest first)

| # | Option | New dep? | New port? | Interface change | On-disk change | Offline-testable | Verdict |
|---|---|---|---|---|---|---|---|
| 1 | **Keyword `SearchMemory` tool** | no | no | **none** (reuses `List`) | none | trivially | **RECOMMENDED** |
| 2 | Prompt/description tweak only ("you can List by prefix") | no | no | none | none | trivial | **Partial — fold into #1** |
| 3 | Tier-2 cold storage (archive trimmed) | no | no | yes (archive/restore) | yes (2nd store) | yes | **Reject now** |
| 4 | Semantic / embedding recall | **yes** | **yes** | yes | yes (vectors) | **hard** | **Reject now** |

### Why #1 wins over #2 (tweak only)

Option 2 asks: is the gap just that the model isn't *told* it can `List` by
prefix? Partly. The footer already says "Recall a key or **prefix**"
(`memoryindex.go:154`) and `Recall` already falls back to a key-prefix listing
(`tools.go:233-248`). **But prefix-only search has a hard limit: it only
matches the *start of the key*.** A trimmed entry under `project/legacy-auth`
is invisible to a model that searches for "auth" unless it guesses the exact
prefix `project/`. The model cannot search by a word in the *description* or
*value* at all. So the description tweak is necessary but not sufficient — it
should be **folded into option 1** (tell the model the new search tool exists),
not shipped alone.

### Why #1 wins over #3 (tier-2 cold storage)

Tier-2 cold storage means: when an entry is trimmed from the index, move it to a
separate archive store, and add archive/restore plumbing. This is the wrong
abstraction:

- The entries are **already all in one `memory.json`** and already fully
  enumerable via `List` (`store.go:260`). "Cold storage" would *split* a store
  that doesn't need splitting, introducing a second file, a migration, and
  index/archive drift to reconcile in the consolidator — the exact overhead
  `MEMORY-TIERING.md:58-71` (D1) rejected for the per-fact-files idea, for the
  same reasons.
- It buys nothing over searching the single store. The blind spot is "the model
  can't reach trimmed entries by meaning"; a search tool over the *one* store
  closes that without moving any bytes. Cold storage adds cost and closes
  nothing extra.
- Sandi Metz applies: duplication (here, a second store mirroring the first) is
  far cheaper than the wrong abstraction, but *no second store at all* is
  cheaper still.

Reject. The single store + a search tool dominates it on every axis.

### Why #1 wins over #4 (semantic) — see full scoping below

Semantic recall is the heaviest option by an order of magnitude and does not
close a gap that keyword search leaves open at this scale. Full cost in
"If you really want semantic."

---

## Recommended increment: `SearchMemory` (read-only keyword search)

A new read-only tool that matches a query against **keys + descriptions +
values across ALL entries**, ranked, returning the matches as
`key — description` lines (NOT full values) so the model then `Recall`s the ones
it wants. It is the search counterpart to the always-in-context index: the index
shows the newest 200; `SearchMemory` reaches the rest by meaning-bearing tokens.

### Interface impact — one store method (as shipped)

> **AS SHIPPED (#12):** the "Future option" below was taken from the start —
> `Search(ctx, query, k)` is a method on `tool.MemoryStore`, implemented by the
> memory adapter, ranking under the existing **shared flock**. The reason the
> as-built code diverged from the "NONE" design here: ranking server-side keeps
> the read atomic against concurrent writers (subagents / cross-process), instead
> of `List`-then-rank-in-the-tool which reads outside the lock. BM25 itself lives
> in a pure adapter helper (`internal/adapter/memory/search.go`); the domain
> interface gains exactly one method and no algorithm leaks across the seam.

The original design (retained for the record): no new `tool.MemoryStore` method —
`SearchMemory` implemented entirely in the tool over the **existing**
`List(ctx, "")` (`store.go:260-280`), which already returns every entry with
key + description + value, with the tool filtering and ranking in memory.

> Future option (TAKEN — see the "AS SHIPPED" note above): if
> `List(ctx,"")`-then-filter is ever shown to be too costly (it will not be at
> hundreds of entries — it is already what `dream` and `Index` do every run), add
> `Search(ctx, query, limit)` to the store so the filter can run under the read
> lock without materialising all values.

### Tool surface

A new read-only tool (`ReadOnly() == true`, so it dispatches in parallel with
other reads, like `Recall` — `tools.go:211`), registered alongside Recall/Remember in
`memory.Tools` (`tools.go:257-262`). Name: **`SearchMemory`** (verb-first,
distinct from the noun-y `Recall`; reads as "search my memory").

```go
// searchArgs is the JSON argument shape for the SearchMemory tool.
type searchArgs struct {
    Query string `json:"query"`
    Limit int    `json:"limit"` // optional; default + hard cap applied
}
```

Behaviour (illustrative — DESIGN ONLY):

1. `List(ctx, "")` → all entries.
2. Case-insensitive match of `query` tokens against `Key`, `Description`
   (derived if empty, via the same `descriptionOrFirstLine` rule the index
   uses — `store.go:320`), and `Value`.
3. Rank: key match > description match > value match; ties by `UpdatedAt`
   desc then key. Keep a small ranking, do not over-engineer (a score = sum of
   field weights is plenty).
4. Return up to `Limit` (default ~20, hard cap ~50) lines of
   `key — description` — **values omitted**, matching tier-0's shape, so the
   output stays small and the model `Recall`s the full value of the ones it
   wants. A zero-match query is a clear non-error result (mirroring
   `tools.go:238-240`).

This deliberately mirrors the index's `key — description` shape so the model
sees a familiar routing-table row and already knows the next move is `Recall`.

### Prompt / description changes

- **New `searchDescription`** steering the model: use `SearchMemory` to find
  saved facts by keyword when the key/topic isn't visible in the index — e.g.
  after the index footer says "N older entries not shown." Reuse the existing
  conservatism framing: it searches **only saved memory**, not the codebase
  (Read/Grep/Glob for code — same boundary line as `recallDescription`,
  `tools.go:83-86`).
- **Index footer tweak** (`memoryindex.go:154`): change
  `...(N older entries not shown; Recall a key or prefix to load them)` →
  `...(N older entries not shown; use SearchMemory to find them by keyword, or
  Recall a key/prefix)`. This is the option-2 "tell the model" fix, folded in.
- **`recallDescription`** (`tools.go:72-92`): add one line cross-referencing
  `SearchMemory` for discovery-by-keyword vs Recall's load-by-exact-key. Keep
  Recall as the loader; SearchMemory as the finder.

The complete loop becomes: **index (see the recent) → SearchMemory (find the
trimmed/forgotten-key) → Recall (load the full value)**. Three crisp roles, one
new tool.

### Layering proof (no new edges)

`SearchMemory` lives in `internal/adapter/memory/tools.go` next to
Recall/Remember. It depends only on `tool.MemoryStore` (already imported,
`tools.go:11`) and uses `List`. No domain package learns about it; no new port;
`prompt` is untouched except the footer string (still a pure-domain string, no
import change). `app` registers it automatically via `memory.Tools`
(`tools.go:257`) — no `build.go` change beyond what already wires the memory
tools. **Dependency direction is unchanged: adapter → domain interface, nothing
points outward.**

### Offline test plan (all offline: fake/in-memory store, `t.TempDir()`)

- `TestSearchMemoryMatchesKeyDescriptionValue` — seed entries where the query
  hits each field in turn; assert each is returned.
- `TestSearchMemoryRanksKeyOverValue` — an entry matching in the key outranks
  one matching only in the value.
- `TestSearchMemoryOmitsValues` — results are `key — description` lines, no full
  values (keeps output small; model must Recall).
- `TestSearchMemoryRespectsLimit` — more matches than `Limit` → truncated to
  `Limit`, hard cap enforced when `Limit` is huge/zero.
- `TestSearchMemoryNoMatchIsNonError` — zero matches → clear "no memory found"
  result, not an error (mirrors `tools.go:238`).
- `TestSearchMemoryReadOnly` — `ReadOnly() == true` (parallel-dispatch safety).
- Registration: extend the existing `memory.Tools`/`Register` test to assert
  three tools register without collision.

Keyword search is **deterministic**, so unlike semantic recall it tests cleanly
offline with no model and no fixtures — a major reason to prefer it.

### Migration

**None.** Same single `memory.json`, same `record` shape, no interface change.
The tool is purely additive read surface over the store that already exists.

---

## The real root cause: consolidation OFF by default (separate recommendation)

The blind spot is *reachable in normal use only because the GC is off.* The
single highest-leverage change is orthogonal to search: **default
`MemoryConsolidateInterval` to a conservative non-zero value** (e.g. hourly, or
once per N sessions) on the embedded `mecatui` surface, and document a sane
default for `mecated`. With consolidation on:

- the set is held tight (merges + prunes, `dream.go:188-251`), so the 200-cap is
  rarely approached;
- the corpus's "enumerable" condition (07 §317) genuinely holds, and semantic
  recall stays unwarranted for the foreseeable future.

This is a `cmd/` composition-root config change, not a memory-architecture
change, so it is out of *this* doc's design scope — but it belongs in the same
decision, because **if you ship consolidation-on-by-default, the urgency of
`SearchMemory` drops** (it becomes a nice-to-have for the rare large store
rather than a fix for a default-config blind spot). My recommendation stands at
**both**: turn on consolidation (root cause) *and* ship `SearchMemory` (cheap
backstop for stores that grow anyway, e.g. consolidation-disabled deployments).
If you want to ship only one now, **consolidation-on is the higher-leverage
half** — flagged for your call.

> Open question for you: is consolidation OFF by default deliberate (cost/
> latency of a background LLM call on the "just works" embedded surface) or
> incidental? If deliberate, `SearchMemory` is the right fix and this section is
> moot. If incidental, fix the default first.

---

## Why semantic is the heavy option (the assessment of record)

> Retained as rationale: this is *why* the keyword MVP was recommended and what
> the cheaper paths were. The semantic path was ultimately DEFERRED (see
> DECISION at the top — the keyword MVP is what shipped). The buildable design
> in the next section is retained for if/when semantic is revisited, and
> neutralises each cost below where it can.

1. **New port — embeddings.** `port.LLMProvider` exposes only `Stream` +
   `Capabilities` (`port/llm.go:98-101`); there is no `Embed`. Semantic recall
   needs a new `port.Embedder`. *Neutralised:* it is a tiny stdlib-only port met
   only in `app` (§1, §7).
2. **Vector storage + dimensionality coupling.** Every entry needs a stored
   vector; a model change staleness-invalidates them. *Neutralised:* additive
   `record.Embedding` + recorded model/dim, lazy recompute (§4).
3. **A new dependency — REJECTED.** Brute-force cosine over a `[]float32` is
   stdlib `math` only; no ANN/vector-DB lib (§2). The repo's tree-sitter-WASM
   burn and the ACP "hand-rolled, no Go library" stance make a new vector dep a
   hard sell, and at this scale it buys nothing.
4. **Offline-test honesty.** A deterministic mock embedder tests ranking
   *mechanics*, not embedding *quality*. Stated plainly (§"Mock embedder").
5. **Doesn't beat keyword at small scale** (corpus 07 §282-297). True, and
   accepted: semantic is opt-in (§6), so the cost lands only where chosen.

---

## BUILDABLE DESIGN — semantic recall

### 1. `port.Embedder` (new domain port, stdlib only)

New file `engine/port/embedder.go`. `port` already imports only domain +
stdlib (`port/llm.go:1-14`); `Embedder` adds NO import (it uses `context` +
`[]float32`), so the package stays clean.

```go
package port

import "context"

// Embedder turns text into dense vectors for semantic memory recall. It is the
// provider-agnostic seam the semantic-recall tool consumes; the concrete OpenAI
// embedder adapter (provider/openai) meets it ONLY in internal/app.
//
// Embed is BATCH by contract: it returns one vector per input text, in input
// order, len(out) == len(texts). Batching is not an optimisation here, it is the
// natural shape — the lazy-backfill path embeds many entry texts in one call
// (the OpenAI embeddings endpoint takes an array input natively, see the adapter)
// and the single-query path simply passes a one-element slice. A single-vector
// Embed1 would force the backfill to loop N HTTP calls; batch collapses that to
// one. Callers that want one vector call Embed(ctx, []string{q}) and take [0].
//
// DIMENSIONALITY is provider/model-determined, NOT fixed by this interface: the
// returned vectors all share whatever dimension the configured model emits
// (e.g. 1536 for text-embedding-3-small). The interface makes no dimension
// promise; the STORE records the model id (and dimension) alongside each saved
// vector so a model change is detectable and triggers re-embedding. Mixing
// vectors of different dimensions in one cosine is the store's job to prevent
// (it re-embeds stale-model entries first).
//
// It returns an error if the provider cannot be reached or the batch fails; on
// error the caller (the read-only SemanticRecall tool) degrades gracefully —
// memory is best-effort context, never correctness, so a failed embed yields a
// clear non-error tool result, not an aborted run.
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

**Why `[]float32` not `[]float64`:** half the memory/disk per vector; cosine
precision is unaffected at retrieval scale. The OpenAI SDK returns `[]float64`
(`openai-go/v3@v3.37.0/embedding.go:101`); the adapter narrows to `float32` at
the boundary (one cast loop), so the domain carries the compact type.

**Why batch (`texts []string`) over single:** the SDK input is natively an array
(`EmbeddingNewParamsInputUnion.OfArrayOfStrings []string`,
`embedding.go:173-179`), so batch is a 1:1 map to the endpoint. The lazy
backfill (§4) embeds *all* missing-vector entries in one request; a single-vector
port would turn that into N round-trips. The query path passes a 1-element slice.

### 2. Brute-force cosine — NO vector-DB dependency

Cosine similarity over a `[]float32` is stdlib `math` only:

```go
// internal/adapter/memory — unexported helper.
func cosine(a, b []float32) float32 {
    // dot / (‖a‖·‖b‖); guard zero norms → 0. len(a)==len(b) is the caller's
    // invariant (store re-embeds stale-dim entries first, §4).
}
```

`SearchSemantic` (§5) computes `cosine(query, e.Embedding)` for every entry, then
partial-sorts the top-K. At hundreds–low-thousands of entries each ≤1536 dims,
this is sub-millisecond and allocation-light — it is the same full-scan shape
`Index`/`List`/`dream` already do every run (`store.go:286-306`).

**Explicitly REJECTED: an ANN/vector-DB lib** (HNSW, FAISS bindings, sqlite-vss,
etc.). Justification, on the record:

- The repo has been **burned by a heavyweight native dep** (the tree-sitter WASM
  binding: ~23 MB/session leak + freeze, since **removed entirely** — see
  `docs/adr/0029-repomap-tree-sitter.md`). A CGO vector lib is exactly that risk class.
- The ACP layer was deliberately **hand-rolled rather than take a Go library**
  that failed the governance+maturity screen. A vector-DB dep faces the same
  screen and fails it here for want of a forcing function.
- Brute-force is *correct* (exact top-K, no recall/precision tuning) and fast
  enough by three orders of magnitude at this scale. ANN trades exactness for
  speed you do not need.

**Revisit trigger (documented).** Brute-force stops being fine when a single
project's memory holds **tens of thousands of entries** (≳50k × 1536 float32 ≈
300 MB of vectors, and the per-call scan creeps toward tens of ms). That is far
beyond a curated per-project fact store (Task-1 steering keeps it to hundreds),
and consolidation (`dream`) actively prunes toward the 200 tier-0 cap. If a real
store is ever observed past ~10k entries, revisit with: (a) cap the semantic
search to the most-recent N vectors, or (b) only then evaluate an ANN dep against
the governance screen. Until then, stdlib cosine is the answer.

### 3. OpenAI embedder adapter (reuses the existing client — NO new dep)

The LLM adapter builds an SDK client from key + base URL + request options and
keeps only `client.Responses` (`openai/openai.go:62-78`). The **same**
`oai.NewClient(reqOpts...)` exposes `client.Embeddings` (an `EmbeddingService`,
`embedding.go:41`) — a different endpoint on the identical client, auth, and base
URL. So the embedder is a thin sibling of `Provider`, not a new SDK:

```go
// provider/openai/embedder.go  (same package, same SDK already imported)
type Embedder struct {
    client responses // actually embeddings.EmbeddingService
    model  string    // default text-embedding-3-small (D-T3.3)
}

func NewEmbedder(opts ...Option) *Embedder { /* same opts as New: key/baseURL/extra */ }

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    resp, err := e.client.New(ctx, openai.EmbeddingNewParams{
        Model: e.model,                                  // EmbeddingModelTextEmbedding3Small
        Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
    })
    // narrow each resp.Data[i].Embedding ([]float64) → []float32, in order.
}

// Model reports the configured embedding model id, so the store can stamp each
// vector with the model that produced it (staleness detection, §4).
func (e *Embedder) Model() string { return e.model }
```

- **Default model (D-T3.3): `text-embedding-3-small`**
  (`EmbeddingModelTextEmbedding3Small`, `embedding.go:126`) — cheapest OpenAI
  embedding, 1536-dim. Configurable via a new `Option` (`WithEmbeddingModel`)
  mirroring `WithAPIKey`/`WithBaseURL` (`openai.go:42-58`). `Dimensions`
  (`embedding.go:149`) left at model default for now; exposing it is a trivial
  later option.
- **Reuses `Option`/`config`** (`openai.go:34-58`) verbatim — same `WithAPIKey`,
  `WithBaseURL`, `WithRequestOption`. So an OpenAI-compatible base URL that
  serves embeddings (vLLM, LiteLLM) works with zero extra surface.
- **No resilience wrapper needed initially.** The LLM path wraps in
  `llmresilience` (`build.go:360-367`); the embedder is read-path, best-effort,
  and the tool degrades on error, so a retry decorator is optional. If desired,
  the same decorator pattern applies — out of scope for v1.

This is the crux of "no new dependency": embeddings ride the SDK already in
`go.mod` (`go.mod:16`, `openai-go/v3 v3.37.0`).

### 4. Vector storage + embed timing

**Embed timing (D-T3.1): embed-LAZILY, persist the result.** On the first
`SemanticRecall` of a session, the store finds entries that have no vector (or a
stale-model vector), the tool embeds those texts in ONE batch (§1), and the store
persists them under the existing exclusive flock. Rationale:

- **`Remember` stays network-free and fast.** Embed-on-write would add an HTTP
  round-trip + token cost to *every* `Remember` (`tools.go:144-169`), on a write
  path that today is a local atomic file write. Lazy confines the cost to the
  opt-in read path the user explicitly invoked.
- **Self-healing.** Entries written while semantic was disabled (or by an older
  build) simply get embedded on first semantic use. No backfill migration step.
- **Batched.** All missing vectors embed in one call, amortising latency.

The text embedded per entry is **`key + "\n" + description-or-first-line`** (the
same `descriptionOrFirstLine` derivation the index uses, `store.go:320`) — NOT
the full value, so the vector reflects the routing-table identity of the fact and
stays cheap. (Open knob: include a value prefix; deferred — start with
key+description.)

**Persistence (D-T3.2): additive field on the existing `record`, inline in
`memory.json`.** Mirrors exactly how Task 2 added `description`
(`store.go:135-139`):

```go
type record struct {
    Value          string    `json:"value"`
    Description    string    `json:"description,omitempty"`
    UpdatedAt      time.Time `json:"updated_at"`
    Embedding      []float32 `json:"embedding,omitempty"`       // NEW, additive
    EmbeddingModel string    `json:"embedding_model,omitempty"` // NEW: model id that produced it
}
```

- **Hot reads don't drag vectors into the model's context.** `Index`
  (`store.go:286-306`), `List` (`store.go:262-280`), and `Recall`
  (`store.go:238-258`) already build `tool.MemoryEntry` values that DO NOT
  include an embedding field — `MemoryEntry` (`tool.go:171-185`) gains no vector
  field. So the index/recall payloads the model sees are byte-for-byte unchanged;
  vectors live only on the on-disk `record` and in the store's in-memory scan for
  `SearchSemantic`. (This is the answer to the assessment's "every read drags
  vectors" worry: the vectors never enter `MemoryEntry`, only `record`.)
- **Cross-process safe by reuse.** Lazy backfill writes vectors via the SAME
  `withExclusiveLock` read-modify-write (`store.go:144-165`) that `RememberEntry`
  uses; `SearchSemantic`'s scan takes the SHARED lock like `Index`
  (`store.go:170-188`). No new locking. The one-`*Store`-per-dir invariant
  (`store.go:80-93`) is untouched.

**Staleness / model change.** Each vector is stamped with `EmbeddingModel`. On a
`SearchSemantic`, an entry whose `EmbeddingModel != embedder.Model()` (or whose
vector dim ≠ the query's) is treated as missing and re-embedded in the same batch
before cosine. So switching `text-embedding-3-small` → `-3-large` self-heals
lazily; no manual migration.

**Migration: none / no-op-additive.** A Task-1/Task-2 `memory.json` has no
`embedding` key → `omitempty` + zero-value decode loads it cleanly with
`Embedding == nil` (the same trick that made `description` a no-op read,
`MEMORY-TIERING.md:318-326`). First semantic use backfills. No version bump.

**Sidecar fallback (if inline proves heavy).** 1536 × float32 ≈ 6 KB JSON/entry;
at hundreds of entries `memory.json` grows to a few MB, re-marshalled on every
write. If that write amplification ever bites, move vectors to a sibling
`memory.vectors.json` keyed by entry key, written under the same flock. The
`record` field and the store method stay the same shape; only the on-disk
location moves. Start inline (one file, one atomic write, no drift); switch only
on evidence. (D-T3.2.)

### 5. The `SemanticRecall` tool + the store method

**Store gains ONE method (D-T3.4) — and owns cosine, not the embedder:**

```go
// engine/tool/tool.go — add to MemoryStore (alongside Index, store.go owner).
//
// SearchSemantic ranks stored entries by cosine similarity to queryVec and
// returns the top k as MemoryEntry values with the VALUE OMITTED (like Index) —
// key + description + updated-at only, so the model then Recalls the full value.
// The store owns the vectors (it persists them) and the cosine scan; it does NOT
// know about embeddings-as-a-service. The CALLER supplies the query vector,
// keeping the Embedder dependency in the tool/app layer, never in the store.
//
// missing reports the keys whose stored vector was absent or produced by a
// different model than modelID, so the caller can embed+backfill them and retry.
// On the first call the store backfills via BackfillEmbeddings (below) and
// returns no missing; the two-method shape keeps the embedder out of the store.
SearchSemantic(ctx context.Context, queryVec []float32, modelID string, k int) (hits []MemoryEntry, missing []string, err error)

// BackfillEmbeddings stores the given key→vector map (each stamped with modelID)
// under the exclusive lock, for entries the caller embedded after SearchSemantic
// reported them missing/stale. It is the only embedder-driven write; Remember
// stays vector-free.
BackfillEmbeddings(ctx context.Context, vectors map[string]EmbeddedVector) error
```

where `EmbeddedVector{ Vec []float32; Model string }` is a small value type added
near `MemoryEntry` (`tool.go:171`). `MemoryEntry` itself gains NO vector field
(§4) — vectors never reach the model-facing entry type.

**Tool control flow (`internal/adapter/memory/semantic.go`, new file, same
package as `tools.go`):** the tool holds `tool.MemoryStore` + `port.Embedder`.

1. Embed the query: `vecs, err := emb.Embed(ctx, []string{query})` → `queryVec =
   vecs[0]`. On error → clear non-error result (best-effort).
2. `hits, missing, err := store.SearchSemantic(ctx, queryVec, emb.Model(), k)`.
3. If `missing` non-empty: `Embed` those entry texts in one batch,
   `BackfillEmbeddings`, then re-run `SearchSemantic` once. (Bounded: at most one
   backfill+retry per call.)
4. Render `hits` as `key — description` lines (values omitted), identical shape to
   the tier-0 index, so the model's next move is the familiar `Recall(key)`. Zero
   hits → clear "no semantically-similar memory found" non-error result (mirrors
   `tools.go:238-240`).

This keeps the **embedder dependency in the tool** (and thus wired in `app`),
the **vectors in the store** (their persistent owner), and **cosine in the store**
(it has the vectors). No layer holds something it shouldn't.

**Tool surface (`ReadOnly() == true`, parallel-dispatch like Recall,
`tools.go:211`):**

```go
const SemanticRecallToolName = "SemanticRecall"

type semanticArgs struct {
    Query string `json:"query"`
    K     int    `json:"k"` // optional; default ~10, hard cap ~25
}
```

**Model-facing description** — positions it against the index, keyword reality,
and Recall:

```
Find saved memory entries by MEANING, when you don't know the exact key.

Your memory INDEX (key + one-line description) is shown at session start, but it
is capped — older entries are trimmed out of it (you'll see a "...N older
entries not shown" footer). SemanticRecall searches ALL saved entries, including
trimmed ones, by semantic similarity to your query, and returns the closest
key + description lines. Then use Recall on a key to load its full value.

When to use:
- You recall saving something about a topic but don't see its key in the index.
- You want facts RELATED to a topic, not an exact-key lookup.

When NOT to use:
- You already see the key in your index, or know it → use Recall directly.
- To discover facts about the CODE → use Read/Grep/Glob. Memory holds only what
  was deliberately saved with Remember.

Arguments:
- query (required): a natural-language description of what you're looking for.
- k (optional): how many matches to return (default 10).
```

The loop becomes: **index (see recent) → SemanticRecall (find by meaning,
incl. trimmed) → Recall (load full value)**. `recallDescription`
(`tools.go:72-92`) gains one cross-ref line; the index footer
(`memoryindex.go:154`) can mention SemanticRecall as the find-by-meaning path.

### 6. Default posture — OFF by default / opt-in

Semantic recall is **network + token + latency-bearing**, unlike memory itself
(local file) or commands/skills (local). It therefore follows the established
opt-in posture for cost-bearing/external features (MCP is opt-in;
`MEMORY-DEFAULTS.md` made *local* memory on-by-default precisely because it is
free+local). **Semantic recall is OFF by default, gated on an embedder being
configured.**

- **New `Config` field** (`build.go:95-184`, in the Memory block ~`:117-121`):
  `EmbedderModel string` (empty = semantic recall disabled) — or a boolean
  `EnableSemanticRecall bool` paired with reusing the existing OpenAI key/base
  URL. **Recommendation:** a single `SemanticRecall bool` flag; when true AND an
  OpenAI key is present, build the embedder from the SAME key/base URL the LLM
  provider uses (`cfg.OpenAIKey` and the effective `ProviderOverrides["openai"]`
  endpoint, `build.go`). This
  avoids a second credential surface.
- **Wiring (`buildCatalog`, `build.go:759-778`):** inside the existing
  `if cfg.MemoryDir != ""` block (semantic recall requires a store), after
  `memStore = store`:

  ```go
  if cfg.SemanticRecall && cfg.OpenAIKey != "" {
      emb := openai.NewEmbedder(
          openai.WithAPIKey(cfg.OpenAIKey),
          openai.WithBaseURL(cfg.ProviderOverrides["openai"].BaseURL), // empty = default host
          openai.WithEmbeddingModel(cfg.EmbedderModel),  // empty = default small
      )
      cat.MustRegister(memory.NewSemanticRecallTool(store, emb))
      slog.Info("semantic recall ENABLED", "model", emb.Model())
  }
  ```

  **Embedder is nil/absent → the tool is never registered → the catalog has no
  `SemanticRecall` → capabilities reports it false** (the `has(name)` check is
  catalog-driven, `service.go:294-306`). No nil-guards leak into the tool: it is
  simply not built. This mirrors how Fork/Team/Memory are conditionally
  registered (`build.go:710-778`).
- **CLI flags.** `cmd/mecated`: a `--enable-semantic-recall` bool (+ optional
  `--embedding-model`) next to `--memory-consolidate-interval`
  (`cmd/mecated/main.go:483`). `cmd/mecatui`: same, next to `--memory-dir`
  (`cmd/mecatui/config.go:107`). Both default OFF.
- **Capabilities advertisement (D-T3.5).** Add a `SemanticRecall` field to
  `mecatlv1.ServerCapabilities` (proto, regenerate via `task generate`) and set
  it in `Service.capabilities()` (`service.go:294-306`):
  `SemanticRecall: has(memory.SemanticRecallToolName)`. The TUI then advertises
  the affordance honestly, exactly like `Memory: has(memory.RememberToolName)`
  (`service.go:302`).

### 7. Layering proof (no domain→adapter edge)

| Thing | Package | Layer | Depends on |
|---|---|---|---|
| `port.Embedder` | `engine/port` | domain port | `context` + `[]float32` only — no new import (`port/llm.go:1-14`) |
| `tool.MemoryStore.SearchSemantic`/`BackfillEmbeddings`, `EmbeddedVector` | `engine/tool` | domain | stdlib + `MemoryEntry` (already there, `tool.go:171`) |
| OpenAI `Embedder` | `provider/openai` | adapter | the SDK already in `go.mod`; meets `port.Embedder` |
| vector persistence + cosine | `internal/adapter/memory` | adapter | `tool.MemoryStore` (implements it) |
| `SemanticRecall` tool | `internal/adapter/memory` | adapter | `tool.MemoryStore` + `port.Embedder` (both domain interfaces) |
| wiring | `internal/app` | composition | imports `openai`, `memory`, `tool`, `port` — all already imported |

- **`port` → adapter: NONE.** `Embedder` is a domain interface; the OpenAI
  adapter satisfies it, meeting it only in `app` (`build.go`), exactly where
  adapters meet ports (CLAUDE.md layering rule).
- **`tool` → adapter: NONE.** The store *implements* the new `MemoryStore`
  methods; the interface stays in `tool`.
- **`memory` (adapter) → `port`:** the `SemanticRecall` tool imports `port` for
  the `Embedder` type. Adapters may import `port` (it is inward). No cycle:
  `port` does not import `memory`.
- **The store never imports `port` or the embedder.** It takes a query vector and
  a model id (plain `[]float32` + `string`); the embedder lives one layer out, in
  the tool. This is the deliberate split (D-T3.4) that keeps the store
  embedder-free while owning the vectors.

Dependency direction is inward everywhere; no new edge points outward.

### 8. Mock embedder & offline test plan

**Deterministic mock embedder (the honest core).** A `mockembed` test double
implementing `port.Embedder` whose vectors make **cosine reflect token overlap**,
so ranking is *meaningfully* testable offline:

- Tokenise each text (lowercase, split on non-alphanumerics).
- Project each token onto a fixed dimension by a stable hash: `dim = fnv(token)
  % D` (e.g. `D = 256`), accumulate `vec[dim] += 1` (a hashed bag-of-words).
- L2-normalise. Two texts sharing tokens get overlapping nonzero dims → high
  cosine; disjoint texts → near-zero cosine.
- `Model()` returns a fixed id like `"mock-bow-256"`.

This gives a real, deterministic ranking signal:

```
TestSemanticRankingReflectsTokenOverlap:
  store: "pref/test-runner" = "Run tests with gotestsum --format dots"
         "project/deploy-gate" = "staging deploy gated by manual CI approval"
  query: "preferred test runner"
  assert: cosine(query, test-runner-entry) > cosine(query, deploy-gate-entry)
  → SemanticRecall returns pref/test-runner ABOVE project/deploy-gate.
```

**Honest statement of what this validates (in the doc and the test file):** the
mock validates the **mechanics** — query embedding, the cosine math, top-K
ranking, lazy backfill, staleness/model-mismatch re-embed, value-omission, the
nil-embedder-disables-tool wiring. It does **NOT** validate real embedding
**quality** (semantic relatedness of paraphrases that share no tokens, e.g.
"how do I run the suite" vs "gotestsum"). Real quality is only verifiable against
a live model and is out of CI scope — the same honest position the assessment
raised, now explicitly bounded rather than hidden. A bag-of-words mock will, by
construction, behave like keyword overlap; that is the point (it tests ranking
plumbing), and the doc says so.

**Offline test plan (all offline: mock embedder + in-memory/`t.TempDir()` store):**

- `provider/openai` (`embedder_test.go`): translation test — a recorded
  embeddings JSON fixture → `[][]float32` of the right shape/order; `float64→32`
  narrowing; `Model()` returns the configured id. (Mirrors the existing fixture
  approach for the SSE translate path, `openai.go:11-13`.) No network.
- `internal/adapter/memory` (`semantic_test.go`):
  - `TestSemanticRankingReflectsTokenOverlap` (above) — the meaningful ranking
    assertion.
  - `TestSemanticRecallOmitsValues` — hits are `key — description`, no values.
  - `TestSemanticRecallRespectsK` — top-K truncation + hard cap.
  - `TestSemanticRecallNoHitsIsNonError` — empty store / no similarity → clear
    non-error result.
  - `TestEmbedderErrorDegrades` — embedder returns error → clear non-error tool
    result, no panic (best-effort).
  - `TestReadOnly` — `ReadOnly() == true`.
- `internal/adapter/memory` (`store_test.go` additions):
  - `TestLazyBackfillPersistsVectors` — first `SearchSemantic` reports `missing`;
    after `BackfillEmbeddings`, vectors are on disk and a fresh `*Store` over the
    dir finds them (no re-embed).
  - `TestStaleModelTriggersReembed` — an entry stamped with model "old" is
    reported missing when `modelID="new"`.
  - `TestMigrationNoEmbeddingField` — a literal Task-2 `memory.json` (no
    `embedding` key) loads with `Embedding==nil` and backfills on first semantic
    use (proves the additive no-op migration).
  - `TestIndexRecallPayloadsUnchanged` — `Index`/`Recall`/`List` results carry NO
    embedding (vectors stay on `record`, never on `MemoryEntry`).
  - `TestVectorWritesUseExclusiveLock` — concurrent backfills over fresh `*Store`s
    on one dir don't lose updates (extends the existing
    `TestCrossProcessRememberNoLostUpdates` pattern, `MEMORY-TIERING.md:360`).
- `internal/app` / `internal/adapter/server`: extend the capabilities table test
  (`capabilities_test.go:130`) with a `SemanticRecall: true` row when the tool is
  registered, `false` when the embedder is absent.

`go run ./cmd/mecademo` stays fully offline and unaffected (semantic recall is
off by default and the demo configures no embedder).

### 9. Phasing (independently-reviewable, independently-green commits)

**Phase 1 — the seam + adapter + mock + wiring, NO tool yet.** Ships green and
inert (nothing registers the tool):

- `engine/port/embedder.go`: `port.Embedder` (§1).
- `provider/openai/embedder.go`: the OpenAI `Embedder` + `Model()` +
  `WithEmbeddingModel` option (§3); `embedder_test.go` fixture translation test.
- `internal/adapter/memory/mockembed_test.go` (or a small `mockembed` test
  package): the deterministic hashed-bag-of-words mock (§8).
- `Config.SemanticRecall` (+ `EmbedderModel`) field and the CLI flags
  (`mecated`, `mecatui`), defaulting OFF (§6). No catalog change yet.
- Green: `task lint && task test`; nothing behavioural changes (no tool, caps
  unchanged).

**Phase 2 — vector storage + the tool + cosine + caps.** Builds on Phase 1:

- `engine/tool/tool.go`: `EmbeddedVector`, `MemoryStore.SearchSemantic` +
  `BackfillEmbeddings` (§5).
- `internal/adapter/memory/store.go`: `record.Embedding`/`EmbeddingModel`,
  `SearchSemantic` (cosine scan + missing/stale detection),
  `BackfillEmbeddings` (exclusive-lock write), `cosine` helper (§2,§4); store
  tests (§8).
- `internal/adapter/memory/semantic.go`: `SemanticRecall` tool +
  `NewSemanticRecallTool` + description (§5); semantic tests (§8).
- `internal/app/build.go`: register the tool when `SemanticRecall && OpenAIKey`
  (§6).
- `contracts/proto` + `Service.capabilities()`: `SemanticRecall` cap (§6/D-T3.5);
  `task generate`; caps test row (§8).
- Green: `task lint && task test`; `go run ./cmd/mecademo` still a full offline
  session.

Optional **Phase 3 (follow-up, not required):** sidecar vector file if inline
write amplification is measured to bite (§4); embedder resilience decorator;
`Dimensions` option; include-value-prefix in the embedded text.

### 10. File-by-file change list

**Domain — `engine/port/embedder.go`** (NEW): `Embedder` interface (§1).

**Domain — `engine/tool/tool.go`**: add `EmbeddedVector` type and
`MemoryStore.SearchSemantic` + `BackfillEmbeddings` (near `MemoryEntry`/
`MemoryStore`, `tool.go:171-226`). `MemoryEntry` UNCHANGED (no vector field).

**Adapter — `provider/openai/embedder.go`** (NEW): `Embedder`,
`NewEmbedder`, `Model()`, `WithEmbeddingModel`; reuses `Option`/`config`
(`openai.go:34-58`) and the SDK client (`openai.go:62-78` pattern). Compile-time
`var _ port.Embedder`.

**Adapter — `internal/adapter/memory/store.go`**: `record` gains `Embedding`
`[]float32` + `EmbeddingModel` `string` (`store.go:135-139`); implement
`SearchSemantic` (shared-lock scan + cosine + missing/stale) and
`BackfillEmbeddings` (exclusive-lock write); add `cosine`. `_ tool.MemoryStore`
assertion (`store.go:96`) now covers the new methods.

**Adapter — `internal/adapter/memory/semantic.go`** (NEW): `SemanticRecallTool`,
`NewSemanticRecallTool(store, embedder)`, `SemanticRecallToolName`, the
description, `ReadOnly()==true`, embed→search→backfill→render flow (§5). Add it to
`memory.Tools`/`Register` ONLY when an embedder is supplied — likely a separate
`memory.RegisterSemantic(cat, store, emb)` so the base `Register`
(`tools.go:257-274`) stays embedder-free.

**Adapter — `internal/adapter/memory/tools.go`**: `recallDescription`
(`tools.go:72-92`) gains a one-line SemanticRecall cross-ref.

**Domain — `engine/prompt/memoryindex.go`**: footer (`memoryindex.go:154`)
mentions SemanticRecall as the find-by-meaning path (string only).

**Composition — `internal/app/build.go`**: `Config.SemanticRecall` +
`EmbedderModel` (`build.go:117-121`); register the tool in `buildCatalog`
(`build.go:759-778`) when enabled + keyed; build the embedder from the existing
OpenAI key/base URL.

**Adapter — `internal/adapter/server/service.go`**: `capabilities()`
(`service.go:294-306`) sets `SemanticRecall: has(memory.SemanticRecallToolName)`.

**Contract — `contracts/proto/mecatl/v1/`**: add `SemanticRecall bool` to
`ServerCapabilities`; `task generate`. `http.go` JSON mirror
(`http.go:84-98`) gains the field.

**Mains — `cmd/mecated/main.go`, `cmd/mecatui/config.go`+`main.go`**:
`--enable-semantic-recall` (+ `--embedding-model`) flags, default OFF, mapped
onto `Config` (`main.go:423` / `main.go:190` regions).

**No change:** the agent loop, session/governance domain, `dream` (consolidation
is orthogonal), the OpenAI Responses streaming path.

---

## Risks / open questions (semantic build)

- **R1 — Embedding quality is untestable in CI.** The mock embedder (§8) is
  bag-of-words, so it validates ranking *mechanics* but behaves like keyword
  overlap; true semantic relatedness (paraphrases sharing no tokens) is only
  verifiable against a live model. Accepted (per the DECISION). Mitigation: keep
  one *manual*/tagged live-model smoke test (skipped in CI without a key) so a
  human can spot-check real ranking before a release. Do NOT let it run in the
  offline suite.
- **R2 — Cost/latency on the opt-in read path.** Lazy backfill (§4) means the
  FIRST `SemanticRecall` after many writes embeds all missing vectors in one
  batch — a latency spike on that call (one HTTP round-trip, N texts). Bounded
  (one batch, key+description only, persisted so it never repeats) and on a path
  the user explicitly invoked. Accept; revisit only if the first-call latency is
  reported as jarring (then: backfill incrementally, or embed-on-write behind a
  flag).
- **R3 — `memory.json` write amplification from inline vectors.** Inline vectors
  (~6 KB/entry) re-marshal on every write. Trivial at hundreds of entries;
  flagged as the trigger to move to a sidecar (§4, D-T3.2). Watch the file size
  in dogfooding; switch on evidence, not on spec.
- **R4 — Two discovery tools may confuse the model.** Recall (load by key) and
  SemanticRecall (find by meaning) need crisp, non-overlapping descriptions or
  the model reaches for the wrong one. Mitigation: the stated loop is
  index → SemanticRecall → Recall, repeated in each description (§5). Watch for
  the model calling Recall with a full sentence (search intent) — if seen,
  tighten the Recall doc.
- **R5 — Embedding text choice.** v1 embeds `key + description` only, not the
  value (§4). If recall misses facts whose meaning lives in the value body,
  include a value prefix (Phase 3). Start narrow; widen on a real miss.
- **R6 — Model/dim drift across a shared store.** Two processes configured with
  different embedding models over one project dir will each see the other's
  vectors as stale and re-embed them, thrashing. Mitigation: document that a
  project's embedding model should be stable; the staleness check makes it
  *correct* (never mixes dims) but not *cheap* under misconfiguration. Acceptable
  for a single-user per-project store; revisit if shared/multi-config use appears.
- **Open — `SemanticRecall bool` vs `EmbedderModel string` as the enable knob.**
  Recommended a single bool reusing the LLM key/base URL (§6) to avoid a second
  credential surface. Confirm; if you foresee a *separate* embeddings endpoint
  (different host/key from the chat model), promote to explicit embedder
  key/base-URL config instead.
- **Open — brute-force revisit trigger.** Documented at ~10k entries/project
  (§2). Confirm that ceiling matches your expectations for the largest realistic
  per-project store; it is far above the consolidation-pruned curated set.


---

*Part of the [design docs](../design/README.md). Read in order: [Memory enabled by default on the embedded mecatui server](0008-memory-on-by-default.md) → [Genuine tiered memory (closing the tier-0 gap)](0009-tiered-memory.md) ← MEMORY-TIER2. Related: [Spike: A "soul" for mecatl — persistent identity + cross-session user-model](0011-soul-and-user-model.md).*
