# Mecatui submitted-prompt history — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this adds a substantive client-local prompt-editor interaction contract and one configurable key action without changing server persistence, protocols, exported APIs, or architecture boundaries.
**Decision record:** None — prompt history remains ephemeral package-private `cmd/mecatui/ui` state, so its traversal and editor-ownership choices belong in this plan rather than an ADR.
**Phase:** submitted-prompt history
**Status:** in-progress, 2026-09-24. The directing operator explicitly waived the plan-merge checkpoint and authorized stacked implementation on the local plan commit.
**Delivery:** Split. Arrow ownership, queued edit-back compatibility, eligible-submission semantics, and draft restoration need human interface review before implementation changes the prompt editor and public keymap contract.
**Expected tasks:** 2
**Issue:** None — requested directly by the operator.
**Plan PR:** absent until opened
**Approved baseline:** absent until the Plan / Interface PR merges

Mecatui will let the operator use Up and Down at the prompt boundary to revisit text from recent model-facing prompts accepted during the current client attachment to a session. The interaction is client-local and bounded: it does not create a second durable history store, replay media, or change the server transcript. Existing surface ownership, multiline editing, queued edit-back, and selection behavior remain authoritative.

The implementation stays within the client boundary described by [the architecture overview](../architecture.md) and extends the live keybinding contract documented in [`docs/tui.md`](../tui.md). It follows the minimum-change and offline-test requirements in [`AGENTS.md`](../../AGENTS.md).

## Human decisions

- [x] History scope — Decision: retain at most the 100 most recent non-empty model-facing text messages committed during the current mecatui attachment to one logical conversation; do not hydrate history from an earlier process or persist a new history store.
- [x] Eligible submissions — Decision: record an ordinary prompt when its prepared send begins, one merged queued prompt only when the queue drains through that send path, and a steer only on its authoritative echo; exclude local built-ins, rejected or empty input, retracted queue/steer text, and TUI-generated continuation text. A transport or model failure after an ordinary prepared send begins does not remove that entry.
- [x] Text and media — Decision: retain the normalized text that passed prompt preparation; never retain or silently replay media bytes, URLs, staged attachment state, or stale editor markers. Skip a media-only submission because it has no recallable text, and suspend history navigation whenever the current unsent draft owns staged media, staged paste state, or pending prompt media so arrows cannot detach text from that state.
- [x] Editor boundary — Decision: unmodified Previous on the first visual row and Next on the last visual row browse history; interior multiline and soft-wrapped rows retain textarea cursor movement. Shift-modified arrows, active selection, palettes, mentions, approvals, help, and overlays retain their existing owners.
- [x] Traversal and drafts — Decision: first Previous snapshots the current text-only draft, traversal clamps without wrapping, and Next past the newest entry restores that exact draft. Any content mutation other than history traversal—including insertion, deletion, newline, paste, completion, or clear—exits browsing and keeps the resulting buffer as the current draft; cursor and selection movement alone do not detach. Duplicate submissions remain distinct chronological entries.
- [x] Queue compatibility — Decision: the existing `EditBack` action remains the previous-history binding and keeps its default `up`; when an editable queued follow-up or pending steer exists it retrieves that state before history browsing. Add `HistoryNext`, default `down`, for forward traversal.
- [x] Session lifecycle — Decision: history belongs to the active logical conversation attachment: `/clear`, startup replacement, and `/sessions` adoption clear it; a successful model, effort, or worktree handoff that carries the conversation preserves it; failed, cancelled, or stale handoffs retain the still-active source history; reconnect retains the list without rebuilding it from delivery replay.
- [x] Collision migration — Decision: startup compares every explicit global override with all other effective global bindings after defaults are applied and fails closed on overlap, so an existing `ScrollU: up` or `ScrollD: down` override must be removed or rebound before prompt history can start; default-only contextual bindings remain valid.

## Interface contract

- **gRPC / protobuf:** None — prompt history is not sent through a new RPC or field and is never hydrated from or written to the server transcript.
- **Exported Go APIs / interfaces:** None — state and any prompt-editor boundary helpers remain package-private or under the import-restricted `cmd/mecatui/ui/prompttextarea` package.
- **Tool schemas:** None — no model-visible tool or argument changes.
- **CLI / config:** Add exact rebindable action `HistoryNext` with default chord `down`. Existing `EditBack` keeps its name, `up` default, override precedence, and queued-edit behavior while also selecting the previous eligible history entry when no editable queue/steer takes precedence. Existing `ScrollU`/`ScrollD`, overlay `Up`/`Down`, and one-launch `--keymap` precedence remain unchanged. Composition checks each explicit global override against every other action's effective default-or-overridden chords and fails startup on overlap.
- **Events / persistence:** None — retain at most 100 prepared text strings, a cursor, and one saved text-only draft in client memory for the current logical-conversation attachment; create no event, settings value, transcript field, media copy, or disk record.
- **Security / authority:** None — recall exposes only operator-submitted text already held by this client attachment and grants no server, filesystem, media, network, tool, or model authority. Recalled media is never automatically reattached, and a draft carrying separate staged media/paste state cannot enter history browsing.
- **Compatibility / migration:** Default prompt behavior changes at the first/last visual row from cursor clamping to history traversal when eligible history exists; interior multiline editing and all higher-priority surface owners remain compatible. Existing `EditBack` overrides become the previous-history chord. An explicit override that overlaps another action's effective binding—including `ScrollU: up` or `ScrollD: down` against the new history defaults—now fails startup with a named collision instead of relying on routing order; operators must remove or rebind it.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — boundary-aware backward and forward traversal

The prompt editor adds narrow first/last visual-row queries while retaining ownership of ordinary cursor movement and selection. Model-owned history navigation snapshots and restores the current draft without modifying stored entries. This preserves the existing private textarea boundary in [`cmd/mecatui/ui/prompttextarea/prompttextarea.go`](../../cmd/mecatui/ui/prompttextarea/prompttextarea.go) and the client-local dependency direction in [the architecture overview](../architecture.md).

**Acceptance:**
- AC1.1: With eligible history, Previous on an empty or first-visual-row prompt selects successively older entries, clamps at the oldest entry, and never wraps.
  - verify: `TestMecatuiPromptHistory_Scenario1_PreviousTraversesAndClamps`
- AC1.2: Next selects successively newer entries and, after the newest entry, restores byte-exactly the text-only draft captured before browsing; it never wraps past that draft, and history browsing remains suspended while the current draft owns staged media, staged paste state, or pending prompt media.
  - verify: `TestMecatuiPromptHistory_Scenario1_NextRestoresSavedDraft`
- AC1.3: Up/Down on interior logical or soft-wrapped rows, Shift+Up/Down, active prompt selection, and `ctrl+p`/`ctrl+n` retain the existing textarea movement or selection behavior.
  - verify: `TestMecatuiPromptHistory_Scenario1_MultilineAndSelectionStayEditorOwned`
- AC1.4: Insertion, deletion, newline, paste, completion, and clear after recall exit browsing and preserve their resulting buffer; cursor or selection movement alone stays attached, and detached content cannot later be replaced by the pre-browse draft.
  - verify: `TestMecatuiPromptHistory_Scenario1_EditDetachesWithoutLosingDraft`

### Scenario 2 — accepted text enters bounded session-local history

History records prepared text at the established prompt, queue, and steer acceptance seams rather than scanning rendered conversation blocks, whose user role can also contain TUI-generated continuation messages. The client keeps the current submission and queue distinctions described in [`cmd/mecatui/ui/update.go`](../../cmd/mecatui/ui/update.go) while following the minimum-state discipline in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC2.1: Ordinary prepared sends enter history when their send attempt begins, merged queued text enters once when it drains through that send path, and steers enter once on authoritative echo; chronological duplicates remain distinct and only the oldest entry is evicted after the 100-entry bound.
  - verify: `TestMecatuiPromptHistory_Scenario2_CommittedMessagesEnterOnceAndBound`
- AC2.2: Empty or rejected input, local built-ins, TUI-generated plan continuation, retracted queue/steer text, and media-only submissions do not enter history; a later transport or model failure does not remove an ordinary prepared send that already began.
  - verify: `TestMecatuiPromptHistory_Scenario2_NonOperatorAndNonTextInputExcluded`
- AC2.3: Recalled text is the prepared text that was committed, while media parts, staged bytes, URLs, attachment maps, and marker-only state are absent and are not restored or sent by navigation alone; an unsent draft's separate staged state remains untouched because browsing is suspended.
  - verify: `TestMecatuiPromptHistory_Scenario2_RecallNeverReattachesMedia`
- AC2.4: Clear, startup replacement, and `/sessions` adoption discard history; successful conversation-carrying model, effort, and worktree handoffs plus live reconnect retain it; failed, cancelled, and stale handoffs leave the source history unchanged; no path hydrates from transcript or delivery replay.
  - verify: `TestMecatuiPromptHistory_Scenario2_LifecycleFollowsLogicalConversation`

### Scenario 3 — key ownership and discoverability remain truthful

Prompt history composes with the existing priority ordering: palette and mention menus claim arrows before phase handling, queued edit-back precedes prior-history recall, and other visible owners keep their navigation. Live help and queue hints derive from the resolved keymap rather than hard-coded defaults, following the keybinding contract in `user-docs/mecatui/keybindings.md` and the client boundary in [the architecture overview](../architecture.md).

**Acceptance:**
- AC3.1: An editable queued follow-up or pending steer consumes `EditBack` before prompt history, while Next cannot discard or bypass that editable state.
  - verify: `TestMecatuiPromptHistory_Scenario3_EditBackPrecedesHistory`
- AC3.2: Slash palettes, mentions, help, approvals, and representative overlays retain Up/Down ownership and leave prompt-history cursor and saved draft unchanged.
  - verify: `TestMecatuiPromptHistory_Scenario3_VisibleOwnersConsumeArrows`
- AC3.3: `HistoryNext` participates in exact-name parsing, YAML and CLI override precedence, complete override-setter coverage, and live help rendering; `EditBack` remains backward compatible, while an explicit global override colliding with another action's effective default or override fails startup and names both actions and the chord.
  - verify: `TestMecatuiPromptHistory_Scenario3_KeymapAndHelpStayLive`
- AC3.4: Canonical public keybinding guidance explains boundary-aware Previous/Next behavior, draft restoration, queue precedence, current-attachment scope, and text-only media treatment without claiming durable shell history.
  - verify: inspection — update the owning keybinding and TUI reference sections using the `user-docs` and `tech-writer` skills, then pass `task docs` and `task site:build`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Durable prompt history across process restarts or unrelated sessions | Future persistence design | Requires an explicit retention, privacy, provenance, and migration contract rather than a client-local list. |
| Exact reconstruction of pre-expansion file mentions, large-paste placeholders, or media attachment bytes | Future draft persistence design | Prepared text is recallable; opaque attachment state is not retained or replayed. |
| Server transcript or protobuf provenance distinguishing operator-authored from synthetic user-role messages | Future Architectural work | This slice records local acceptance seams and introduces no protocol field. |
| Searching, deleting, exporting, deduplicating, or synchronizing history | Later feature with a concrete use case | Navigation is bounded previous/next traversal only. |
| Changing conversation viewport, mouse scrolling, overlay navigation, or queue merge semantics | Existing owners | Prompt history composes with those behaviors and does not redesign them. |

## Definition of done

1. Applicable `task lint`, `task test:race`, `task docs`, `task site:build`, and `task api:check` gates pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Private reducer names and state layout remain implementation details; one owner must handle entry recording, traversal, edit detachment, and lifecycle reset rather than duplicating cursor policy across idle and running handlers.
- Prompt preparation currently serves several send modes. Implementation must prove each committed message is recorded exactly once and must not derive history from rendered `blockUser` values.
- The 100-entry bound limits added retention but does not shrink the existing conversation projection; history strings should reuse committed immutable text where practical rather than introducing attachment or transcript copies.
