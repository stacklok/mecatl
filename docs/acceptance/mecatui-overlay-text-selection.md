# Mecatui overlay text selection — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this adds user-visible selection and clipboard behavior across existing mecatui body surfaces, but changes no durable API, persistence, authority, or subsystem boundary.
**Decision record:** None — the behavior is local terminal-client interaction built from existing mouse-capture, selection, clipboard, and surface-ownership contracts.
**Phase:** focused capability — local mecatui selection UX
**Status:** proposed, 2026-09-25. Ready for Plan / Interface review.
**Delivery:** Split. The behavior crosses root input routing, legacy overlays, modal surfaces, tests, and public documentation, so the plan and implementation remain separate review layers.
**Expected tasks:** 1
**Plan PR:** [#1928](https://github.com/stacklok/mecatl/pull/1928)

When mecatui owns mouse input on the alternate screen, users can select and copy text from the visible body owner rather than only from the prompt or conversation. The root model captures the exact rendered body, owner identity, origin, and bounds so selection follows what the user can see without teaching each overlay a separate selection implementation.

The visible owner retains its existing input priority. Clickable controls run before text selection, the configured `CopySelection` action (default `ctrl+y`) recopies selected body text before falling back to an overlay-specific action, and hidden prompt or conversation state cannot receive mouse gestures. Inline and `--no-mouse` modes continue to leave selection and middle-click behavior to the terminal. The empty-session welcome remains a non-selectable body: it appears only when the conversation is empty and is not promoted into the overlay-owner lifecycle.

## Human decisions

None — the operator requested separate stacked design and implementation PRs, and the existing terminal posture, copy-on-select behavior, owner precedence, and native-selection escape hatches determine the remaining behavior.

## Interface contract

- **gRPC / protobuf:** None — selection remains entirely inside the local mecatui client and adds no wire messages or fields.
- **Exported Go APIs / interfaces:** None — all new owner and render-frame types and methods remain package-private under `cmd/mecatui/ui`.
- **Tool schemas:** None — no model-visible tool or input/output schema changes.
- **CLI / config:** None — existing alternate-screen and `--no-mouse` behavior gates in-app mouse capture, and the existing remappable `CopySelection` action retains its configured binding (`ctrl+y` by default); no flag, setting, key name, or default changes.
- **Events / persistence:** None — selection and render-frame identity are transient root-model state and are not emitted or persisted.
- **Security / authority:** None — the clipboard receives only text already rendered to the current terminal user; permission decisions, hidden content, credentials, and server authority are unchanged.
- **Compatibility / migration:** Additive client behavior under mouse capture. Inline mode and `--no-mouse` preserve terminal-native selection and middle-click behavior; existing prompt and conversation selection remain compatible.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — select and copy text from the visible body owner

A user opens an approval, full-screen argument or plan view, help, session details, an agent panel, an inventory or picker, authorization, connect, replay, or fatal view. Following mecatui's client-only boundary in [architecture](../architecture.md) and the owner-first surface model exemplified by [ADR 0222](../adr/0222-mecatui-ask-args-view.md), selection applies to the exact visible rendered body.

**Acceptance:**
- AC1.1: With alternate-screen mouse capture enabled, a left-button drag within the visible body's captured content bounds selects its rendered text, copies the non-empty selection on release, and leaves it highlighted for the configured `CopySelection` action or right-click to copy again.
  - verify: `TestBodySelectionIsRootOwnedForSurfaceAndLegacyOverlay`, `TestPermissionModalTextCanBeSelectedAndCopied`, `TestFullScreenApprovalTextCanBeSelectedAndCopied`
- AC1.2: The copied payload maps through the captured content origin, excludes ANSI styling and parent card/placement chrome, preserves selected visible glyphs in row order, and uses the existing clipboard plus primary-selection delivery path; an empty selection writes nothing and retains no highlight.
  - verify: `TestBodySelectionIsRootOwnedForSurfaceAndLegacyOverlay`, `TestSelectedTextUnchangedByEmptyLineFix`, `TestPressDragReleaseCopiesSelection`
- AC1.3: The same root-owned behavior covers fatal and stored-replay bodies without changing their content or server interaction.
  - verify: `TestBodySelectionFatalDebugHeaderAndReplay`
- AC1.4: Inline mode and `--no-mouse` create no in-app body selection, copy command, or highlight, leaving mouse behavior to the terminal.
  - verify: `TestBodySelectionFatalAndCaptureGates`, `TestBodySelectionCaptureGatesAcrossOwnerKinds`

### Scenario 2 — visible-owner precedence prevents stale or hidden interaction

The current body owner is resolved in the same precedence order used for rendering. This extends the existing ownership discipline without weakening approval ownership in [ADR 0222](../adr/0222-mecatui-ask-args-view.md) or the discoverable overlay model in [ADR 0025](../adr/0025-ux-discoverability.md).

**Acceptance:**
- AC2.1: Clickable modal controls take precedence over text selection; with an active body selection the configured `CopySelection` action recopies that text, and with no body selection an owner-specific use of the same action, such as MCP authorization Copy Link, retains its existing behavior.
  - verify: `TestApprovalButtonHitPrecedesBodySelection`, `TestBodySelectionAuthorizationCopyPrecedence`
- AC2.2: While a body owner is visible, left-button presses outside valid controls or selectable text, mouse motion, release, right-click, middle-click, and wheel input are consumed before they can mutate, copy, paste into, or scroll the hidden prompt or conversation; overlays that own wheel navigation continue to receive it.
  - verify: `TestBodySelectionIsRootOwnedForSurfaceAndLegacyOverlay`, `TestMecatuiBoundedScrollCursor_Scenario3_AgentsWheelSubviewMatrix`
- AC2.3: Escape clears a body selection before performing the owner's ordinary Escape action, and closing or replacing an owner clears its captured frame.
  - verify: `TestBodySelectionIsRootOwnedForSurfaceAndLegacyOverlay`, `TestPermissionModalTextCanBeSelectedAndCopied`, `TestBodySelectionInvalidatesOnFrameIdentity`
- AC2.4: A change to owner identity, rendered body, geometry, capture posture, or a stale pre-render owner invalidates selection before another copy can occur.
  - verify: `TestBodySelectionInvalidatesOnFrameIdentity`

### Scenario 3 — selection behavior is documented where users look

The key reference and terminal-client guide describe selection as a mouse-capture feature, consistent with the discoverability principle in [ADR 0025](../adr/0025-ux-discoverability.md).

**Acceptance:**
- AC3.1: `docs/tui.md` documents panel and overlay selection, copy-on-release, owner precedence, middle-click suppression while an owner is visible, selection-first Escape, and the inline/`--no-mouse` native fallback.
  - verify: inspection — this is developer reference prose whose semantic consistency requires review; `task docs` checks structure and links
- AC3.2: `user-docs/mecatui/keybindings.md` and `user-docs/mecatui/using-the-tui.md` explain how to select and recopy visible panel or overlay text, that the configured `CopySelection` action takes selection precedence, that Escape clears a selection before closing or acting on its owner, that in-app middle-click paste is suppressed while an owner is visible, and when the terminal retains native behavior.
  - verify: inspection — these are public workflow claims reviewed against the implemented input gates; `task site:build` validates the site

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| New key bindings, flags, or configuration | Future product design | Reuse `CopySelection`, alternate-screen, and mouse-capture contracts unchanged. |
| Keyboard selection inside read-only body owners | Future accessibility design | This slice extends the existing mouse selection model only. |
| Empty-session welcome selection | Future welcome-surface design | The welcome appears only with an empty conversation, remains outside the overlay-owner lifecycle, and starts no useful conversation selection. |
| Per-overlay selection implementations | Not planned | Root-owned rendered-frame capture prevents duplicate and drifting coordinate logic. |
| Conversation autoscroll or multi-click semantics | Existing conversation selection | Preserve current conversation behavior. |
| Server, engine, API, persistence, or authority changes | Not applicable | The capability is client-local presentation state. |

## Definition of done

1. The named body-owner, approval, fatal/replay, invalidation, precedence, and capture-gate proofs pass offline.
2. `task lint`, `task test:race`, `task docs`, and `task site:build` pass on the final candidate, aside from independently confirmed failures already present on the exact `origin/main` baseline.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green.
5. The implementation PR links this Plan / Interface PR and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Rendered-frame capture intentionally invalidates selection whenever owner text or geometry changes. This favors never copying stale or hidden text over retaining a selection through scrolling or dynamic updates.
- Terminal-native selection behavior varies by terminal; mecatui's contract is limited to not enabling in-app selection when mouse capture is disabled.
