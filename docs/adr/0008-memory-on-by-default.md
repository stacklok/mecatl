# ADR 0008 — Memory on by default in the embedded mecatui server

- Status: Accepted
- Date: 2026
- Scope: `cmd/mecatui` composition root — XDG memory-dir resolution, embedded-server config, and `--memory-dir`/`--no-memory` flags.

## Context

The embedded mecatui server never wired a memory directory into its configuration, so the Remember/Recall/SearchMemory tools were silently absent. A "store this in memory" prompt produced a chat reply with no persistence and no feedback. The session had the tools in its catalog only when the shared engine was not used; the per-session path had no dir to open.

## Decision

Compute a per-project default memory directory under XDG data (`~/.local/share/mecatui/memory/<path-slug>`, using the full absolute workspace path as the leaf so each checkout is collision-free), wire it into the embedded config at composition time, and add `--memory-dir` and `--no-memory` flags. Consolidation stays off by default to avoid background LLM spend the user has not opted into. The directory is treated as opaque and store-owned so the on-disk format can evolve without touching this wiring.

## Consequences

Memory tools are available in every mecatui session by default without user configuration. The XDG data base is resolved via `adrg/xdg` (promoted to a direct dependency) rather than a hand-rolled env dance, consolidating two previously inconsistent XDG lookups. The store creates the directory; `cmd/mecatui` only computes the path string, keeping the wiring forward-compatible with tiering changes. Current behaviour: docs/architecture.md. Shipped/deferred state: [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

---

## Summary

`mecatui`'s embedded server never set `app.Config.MemoryDir`
(`cmd/mecatui/main.go` `embeddedConfig`), so `internal/app/build.go`'s registration
gate took the disabled branch and the `Remember`/`Recall`/`SearchMemory` tools were
absent — a "store this in
memory" prompt silently produced a chat reply with no persistence and no feedback.
This change computes a **per-project default memory directory** under XDG **data**,
wires it into `embeddedConfig`, and adds two opt-out/override flags
(`--memory-dir`, `--no-memory`) mirroring `mecated`'s existing `--memory-dir`. All
changes stay in the `cmd/mecatui` composition root (`config.go` + `main.go`); the
`ui`/`theme`/`client` packages are untouched. Consolidation stays **off by
default** (token cost). The directory is treated as an opaque store-owned location
so Task 2's tiering rework can change the on-disk format without touching this
wiring.

---

## The default-dir strategy

### Concrete path

```
$XDG_DATA_HOME/mecatui/memory/<path-slug>
```

falling back, when `XDG_DATA_HOME` is unset or empty, to:

```
~/.local/share/mecatui/memory/<path-slug>
```

**Why DATA, not STATE/config/cache.** Memory is **curated user data** worth backing
up and migrating — the model persists durable facts the user wants to survive. That
fails `XDG_STATE_HOME`'s "data that should persist but is not important or portable
enough" test, and it is obviously not config (the user never hand-edits it) nor
cache (cache dirs are routinely wiped; memory must survive that). `XDG_DATA_HOME`
(`~/.local/share`) is the correct base.

**Base resolution uses `github.com/adrg/xdg`**, not a hand-rolled
`os.Getenv("XDG_DATA_HOME")` / `os.UserHomeDir` dance: `xdg.DataHome` resolves
`$XDG_DATA_HOME` when set, else `~/.local/share`, with the library's proper
fallbacks. The same change also consolidated the OTHER hand-rolled XDG spot in
`main.go` — `themeDirs`'s first entry now uses `xdg.ConfigHome` (`$XDG_CONFIG_HOME`
→ `~/.config`) instead of its own env/home dance — so both XDG lookups go through
one maintained library rather than leaving one hand-rolled and one not. `adrg/xdg`
(MIT, maintained, widely adopted) was already an indirect dependency; importing it
in our own code promotes it to a direct `require` (via `go mod tidy`).

**The global read is isolated to the composition boundary.** `defaultMemoryDir`
takes the base as a parameter (`dataHome string`) and reads no globals — it is a
pure function of its arguments. The single read of the `xdg.DataHome` package-global
happens in `resolveMemoryDir` (main is the composition root, so the one global read
belongs there), which calls `defaultMemoryDir(xdg.DataHome, cfg.workspace)`. This
keeps the unit under test pure: its table test passes literal `dataHome` strings, so
it needs no env manipulation, no `xdg.Reload()`, and is parallel-safe; the
empty-base degraded branch is reachable simply by passing `""`.

Caveat that survives only at the integration layer: `adrg/xdg` snapshots the
environment at package init, so `xdg.DataHome`/`xdg.ConfigHome` do NOT reflect a
later `t.Setenv` until `xdg.Reload()` re-reads the env. That dance is now confined to
the ONE test that exercises the real `resolveMemoryDir(cfg)` global-reading path
(`TestResolveMemoryDirPrecedence`, via a `setDataHome` helper). `xdg.DataHome` is
practically never `""` (it falls back to `~/.local/share`, and to a root-anchored
`/.local/share` even with no `HOME`), so the empty-`dataHome` guard in
`defaultMemoryDir` is a defensive belt; the live degraded path is an empty
workspace.

### Workspace → leaf mapping (full path-slug, NO hash)

The store is scoped **per-project** (`engine/tool/tool.go`: "one store instance
per workspace/project directory ... NOT shared across unrelated projects"). The
embedded server already resolves the workspace to an absolute path
(`config.go` `resolveWorkspace`, called in `parseFlags`). The leaf is that absolute
path with the OS path separator replaced by `-`, **preserving the leading separator
as a leading `-`**:

```
/var/home/jaosorior/Development/stacklok/mecatl
  → -var-home-jaosorior-Development-stacklok-mecatl
```

i.e. `strings.ReplaceAll(workspace, string(filepath.Separator), "-")`.

**Why the full path-slug and not slug+hash:**

- Encoding the **full** absolute path makes the leaf inherently collision-free:
  two same-named checkouts (`/a/proj` vs `/b/proj`) get distinct leaves
  (`-a-proj` vs `-b-proj`) with no hash needed.
- It is **deterministic** (same workspace → same leaf, every run) and
  **human-legible**: a user inspecting `~/.local/share/mecatui/memory/` reads the
  originating path directly and can `rm` the right one.
- It is **exactly the convention the user's own Claude memory already uses**
  (`~/.claude/projects/-var-home-...`), so it is familiar and consistent.

No `sha256`, no `<slug>-<hash8>`. The earlier draft of this doc proposed a STATE
base and a slug+hash leaf; that was superseded by the DATA + path-slug decision
recorded here.

### Who creates it

**Nobody in `main` `MkdirAll`s.** `memory.New(dir)` creates the dir and parents
(`internal/adapter/memory/store.go` `New`), and `app.Build` calls
`memory.New(cfg.MemoryDir)` in `build.go`'s registration gate. So
`embeddedConfig` only **computes the path string** and assigns it to
`app.Config.MemoryDir`; the store owns creation. This keeps it forward-compatible
(see Task 2 notes): `main` never touches the filesystem layout.

### Permissions

`memory.New` now `MkdirAll`s the store dir at **`0o700`** (was `0o755`). Memory can
hold sensitive curated facts; the per-project store has no reason to be
group/other-readable, and tightening it in the adapter benefits `mecated`
identically. No test asserts the old `0o755`.

---

## Flag surface

Two flags on `cmd/mecatui`, both "embedded server only" (like `--mock`,
`--no-bash`), added to the `config` struct in `config.go` and the flag set in
`parseFlags`:

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--memory-dir` | string | `""` | Override the per-project default memory directory. Empty = use the computed default. |
| `--no-memory` | bool | `false` | Disable memory entirely (no Remember/Recall/SearchMemory tools). |

### `config` struct additions (`config.go`)

```go
// Embedded-server memory config (used only when hosting an in-process
// server). An empty memoryDir means "compute the per-project default under
// $XDG_DATA_HOME/mecatui/memory"; an explicit path overrides it. noMemory
// disables cross-session memory (Remember/Recall) entirely and wins over
// both (the resolved MemoryDir becomes ""). Precedence is applied in
// embeddedConfig (resolveMemoryDir), not here.
memoryDir string
noMemory  bool
```

### Flag registration (`parseFlags`, alongside `--no-bash`)

```go
fs.StringVar(&cfg.memoryDir, "memory-dir", "",
    "embedded server only: per-project memory store directory "+
        "(empty = a per-project default under $XDG_DATA_HOME/mecatui/memory)")
fs.BoolVar(&cfg.noMemory, "no-memory", false,
    "embedded server only: disable cross-session memory (Remember/Recall) entirely")
```

### Precedence (resolved in `embeddedConfig`, not `parseFlags`)

```
--no-memory       → MemoryDir = ""              (wins over everything)
--memory-dir=X    → MemoryDir = X               (explicit override)
neither           → MemoryDir = defaultMemoryDir(cfg.workspace)
```

`defaultMemoryDir` returns `""` if `xdg.DataHome` is empty (defensive — see the
caveat above) or the workspace is empty — in that degraded case memory is simply off
rather than anchoring a store at a bogus path. The split is kept: `parseFlags` is
argv→struct, `embeddedConfig` is struct→`app.Config`.

### Mapping onto `app.Config`

`embeddedConfig` gains one assignment:

```go
return app.Config{
    // ... existing fields ...
    MemoryDir: resolveMemoryDir(cfg),
    // MemoryConsolidateInterval intentionally left 0 — see below.
}
```

### Helpers (`main.go`, near `themeDirs`)

```go
func resolveMemoryDir(cfg config) string {
    if cfg.noMemory {
        return ""
    }
    if cfg.memoryDir != "" {
        return cfg.memoryDir
    }
    // The single place that reads the xdg.DataHome global; defaultMemoryDir
    // stays pure (the base is injected).
    return defaultMemoryDir(xdg.DataHome, cfg.workspace)
}

func defaultMemoryDir(dataHome, workspace string) string {
    if dataHome == "" || workspace == "" {
        return ""
    }
    leaf := strings.ReplaceAll(workspace, string(filepath.Separator), "-")
    return filepath.Join(dataHome, "mecatui", "memory", leaf)
}
```

The bespoke `dataBase` helper (env + `os.UserHomeDir` dance) is gone — `xdg.DataHome`
does that, read once in `resolveMemoryDir` and injected into the pure
`defaultMemoryDir`. Imports: `github.com/adrg/xdg` plus stdlib `path/filepath`,
`strings` (`fmt`, `os`, `time` already imported). No `crypto/sha256`.

`themeDirs` keeps its precedence order unchanged (XDG config → workspace
`.mecatui/themes` → cwd `.mecatui/themes` → `--theme-dir`); only its first entry
switched from a hand-rolled `XDG_CONFIG_HOME`/`~/.config` lookup to
`filepath.Join(xdg.ConfigHome, "mecatui", "themes")`.

---

## Consolidation-interval default

**Leave it OFF (`MemoryConsolidateInterval = 0`).**

- `startMemoryConsolidation` (`build.go`) spawns a **background goroutine that
  calls the LLM provider** on every tick. On the embedded server that provider is
  the user's real OpenAI key — a default-on interval would silently spend tokens on
  a TUI the user may have left open. Surprising default for a single-user tool.
- `mecated` itself defaults it to `0` (`cmd/mecated/main.go`). Diverging the
  embedded server would gratuitously break from the shared build contract.
- Memory still works fully without consolidation: Remember/Recall persist and
  survive restarts. Consolidation is an optimization (distill/dedup), not a
  correctness requirement.

No `--memory-consolidate-interval` flag is added now — no caller asks for it, and an
unused flag is surface to maintain. A future opt-in can mirror `mecated`.

---

## File-by-file change list

### 1. `cmd/mecatui/config.go`

- Add `memoryDir string` and `noMemory bool` to the `config` struct (after
  `noBash`), with the doc comment above.
- Register the two flags in `parseFlags` after the `--no-bash` line.
- **No change to `validate()`** — memory has no validation invariant (an
  unresolvable default just disables it; an explicit `--memory-dir` is taken
  verbatim, and `memory.New` surfaces a bad path as a logged warning that disables
  the tools — non-fatal, matching mecated).

### 2. `cmd/mecatui/main.go`

- In `embeddedConfig`, add `MemoryDir: resolveMemoryDir(cfg),` and a comment noting
  consolidation is deliberately left at 0.
- Update the `embeddedConfig` doc comment: memory moves from the "left off" list to
  "enabled by default (per-project)".
- Add the two unexported helpers `resolveMemoryDir(cfg) string` (reads the
  `xdg.DataHome` global once) and `defaultMemoryDir(dataHome, workspace string)
  string` (pure — the base is injected; no bespoke `dataBase`).
- Consolidate `themeDirs`'s first entry onto `xdg.ConfigHome` (was a hand-rolled
  `XDG_CONFIG_HOME`/`~/.config` lookup); precedence order unchanged.
- New imports: `github.com/adrg/xdg`, `strings` (`fmt`, `os`, `path/filepath`,
  `time` already imported).

### 3. `internal/adapter/memory/store.go`

- `New` `MkdirAll` tightened from `0o755` to `0o700`.

### 4. `internal/app/build.go`

**No change.** The registration gate (`MemoryDir != ""` — with the later
`MemoryStoreURL` remote-driver branch taking precedence when set), `memory.New`, and
`startMemoryConsolidation` already do the right thing for a non-empty `MemoryDir`
and a zero interval. This change only feeds the gate a non-empty dir.

### 5. `go.mod`

- `github.com/adrg/xdg v0.5.3` promoted from the `// indirect` block to the direct
  `require` block (via `go mod tidy`, now that our own code imports it). No version
  change; `go.sum` untouched.

---

## Test plan

All offline (no provider/network), consistent with `config_test.go` and the
`UseMock` pattern in `embed_test.go`.

### `cmd/mecatui/config_test.go`

- **`TestParseFlagsMemoryDefaults`** — `parseFlags(nil)` leaves
  `cfg.memoryDir == ""` and `cfg.noMemory == false`.
- **`TestParseFlagsMemoryFlags`** — `-memory-dir /tmp/mem` sets `memoryDir`;
  `-no-memory` sets `noMemory`.
- **`TestDefaultMemoryDir`** — a single PURE table test of
  `defaultMemoryDir(dataHome, workspace)`. Because the base is injected, every row
  passes literal strings (no env, no `xdg.Reload`) and the test is `t.Parallel()`.
  Rows cover: the full path-slug encoding (`/` → `-`, leading `-` preserved) under a
  given base; a `~/.local/share`-shaped base (the caller's fallback) flowing
  through unchanged; two same-basename workspaces (`/a/proj`, `/b/proj`) →
  **distinct** leaves; both degraded guards reachable directly — empty `dataHome` →
  `""`, empty workspace → `""`; plus a determinism check (same inputs → same path).
- **`TestResolveMemoryDirPrecedence`** — the ONE integration test that drives the
  real `resolveMemoryDir(cfg)` (which reads the `xdg.DataHome` global): `--no-memory`
  beats an explicit `--memory-dir`; `--memory-dir` alone is taken verbatim; neither
  → the computed default. It uses `setDataHome(t, "/xdg/data")` — a helper that
  `t.Setenv`s `XDG_DATA_HOME` + calls `xdg.Reload()` (and re-reloads on cleanup),
  because `adrg/xdg` snapshots the env at init. This is now the only place the
  Reload dance is needed.

### `cmd/mecatui/embed/embed_test.go`

- **`TestStartWithMemoryDirServes`** — `embed.Start` with
  `app.Config{..., MemoryDir: t.TempDir()}` (mock provider) builds and serves, and
  `CreateSession` succeeds over the socket — proving the `build.go` memory gate
  (`memory.New` + `memory.Register`) runs end-to-end without network. There is no
  list-tools RPC, so this is the lightest honest wire-level proof the gate fires;
  the catalog-population assertion proper lives in `internal/adapter/memory`'s
  `tools_test.go` (which already tests `memory.Register`).

No consolidation test — the default leaves it off and no flag is added.

> **Issue #42 note:** the memory tools ride EVERY engine the memory prompt blocks
> ride. Per-session/selector engines (`sessionEngineFactory`) assemble their catalog
> through the same `assembleCatalog` as the shared engine, with the
> `tool.MemoryStore` pair threaded via `catalogAssets` (the flocked stores are
> opened once in `buildCatalog`/`buildUserModelStore` — the sole construction
> sites; never a second store on the same dir). So a `/models`
> pick can no longer produce a session whose turn-0 `<memory-index>` advertises
> memory its catalog cannot Recall. Guarded by
> `TestSelectorSessionMemoryPromptHasMatchingTools` and
> `TestPerSessionCatalogMatchesSharedCatalog` (`internal/app`).

---

## Doc updates (`docs/tui.md`)

- Flags table gains `--memory-dir` and `--no-memory` rows (after `--no-bash`).
- The "embedded keeps opt-ins off" paragraph drops `memory` from the off-by-default
  list and gains a paragraph: memory is **ON by default**, per-project under
  `~/.local/share/mecatui/memory/<path-slug>/`, with `--no-memory` to disable and
  `--memory-dir` to relocate; consolidation stays off.

---

## Forward-compatibility notes for Task 2 (true tiering)

Task 2 will replace the flat-KV store internals, likely the on-disk format, and may
inject a tier-0 index into the prompt. This Task-1 design keeps that free by
treating the directory as **opaque and store-owned**:

- **`main`/`embeddedConfig` compute only a *directory path string*** and hand it to
  `app.Config.MemoryDir`. They never reference `memory.json`, never read or write
  the layout, never `MkdirAll` the leaf (the store does). The single file
  `memory.json` lives entirely inside the adapter; Task 2 can change it to shards,
  a SQLite file, whatever — no change to mecatui wiring.
- **The flag contract is `--memory-dir` = "where the store lives", not "the memory
  file".** Same semantics `mecated` already documents. Task 2 inherits it unchanged.
- **Do NOT hardcode in this change:** the `memory.json` filename, any single-file
  assumption, any parse of the store contents in `main`, or any tier/index notion.
  Everything stays behind the `tool.MemoryStore` interface and `memory.New(dir)`.
- **The enabled/disabled signal for Task 3's honest empty-states** should travel via
  the existing capabilities/event path (the server already advertises provider
  capabilities at `handleInitialize`). When Task 3 needs the UI to know memory is
  on, expose it as a server-advertised capability the `client` relays as a proto
  field — **never** by having `ui`/`client` import `internal/...` or learn the
  memory dir. Reserve the channel; do not implement it now.

---

## Risks / open questions

1. **`adrg/xdg` almost never yields an empty base.** With no `XDG_DATA_HOME` and no
   `HOME`, `xdg.DataHome` resolves to a root-anchored `/.local/share` rather than
   `""`, so the `xdg.DataHome == ""` guard in `defaultMemoryDir` is a defensive belt
   that is effectively unreachable in practice; the live degraded path is an empty
   workspace. We accept the library's behavior here rather than re-deriving a base
   ourselves (the whole point of adopting `adrg/xdg`). A pathological no-HOME no-XDG
   environment would get a `/.local/share/...` store; that is an exotic, broken host
   and not worth a bespoke guard that re-introduces the hand-rolled dance.

2. **Per-worktree memory.** The leaf is the absolute workspace path, so each git
   worktree of the same repo gets its own memory store. That is the correct
   per-*directory* scoping per the contract, but a user who thinks "my mecatl
   memory" may be surprised that a worktree starts empty. Acceptable and consistent
   with the documented scoping. No action.

3. **Layering is now machine-enforced** (depguard allowlist in `.golangci.yml` +
   the DAG test in `engine/arch/layering_test.go`, both under `task lint`/`task
   test`). The helpers in `main.go` live in `cmd/` (a composition root, exempt from
   the core rules) and import only stdlib (`os`, `path/filepath`, `strings`);
   nothing new leaks into `ui`/`theme`/`client`.

4. **Default-on changes disk footprint.** Every project opened in mecatui now gets
   a (small, lazily-created) directory under `~/.local/share`. The store file is
   written only on the first `Remember`, so an unused project leaves at most an
   empty `0o700` dir. Acceptable.


---

*Part of the [design docs](../design/README.md). Read in order: MEMORY-DEFAULTS → [Genuine tiered memory (closing the tier-0 gap)](0009-tiered-memory.md) → [Tier-2 / semantic memory recall — assessment + buildable design](0010-semantic-memory-recall.md). Related: [Spike: A "soul" for mecatl — persistent identity + cross-session user-model](0011-soul-and-user-model.md).*
