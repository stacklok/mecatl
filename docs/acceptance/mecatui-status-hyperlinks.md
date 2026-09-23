# Mecatui StatusML terminal hyperlinks — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — emits terminal-standard hyperlink controls for an existing, validated local status-line markup feature without changing durable trust, persistence, server, or public API boundaries.
**Decision record:** None — this is a focused presentation behavior: existing StatusML link destinations remain the only source of hyperlink targets and existing validation remains the trust boundary.
**Phase:** capability — local TUI presentation extension
**Status:** in-progress, 2026-09-23. Implementation started after the merged Plan / Interface PR approved the native OSC 8 scope.
**Delivery:** Split. This user-visible output and safety-boundary contract merits review before implementation; the Plan / Interface PR is the approval gate.
**Expected tasks:** 1
**Plan PR:** [#1830](https://github.com/stacklok/mecatl/pull/1830)
**Approved baseline:** `9b6caf96555460530ad191eefadf0e414f45e2b0`

StatusML already represents a link as display text plus a separately validated `Href`, but mecatui currently renders it only as themed, underlined text. This follow-up owns the terminal-hyperlink scope deferred by the existing [status-line plan](../acceptance/mecatui-status-line.md) and implements the future renderer behavior anticipated by [ADR 0247](../adr/0247-mecatui-status-line.md). It makes each retained StatusML link a native OSC 8 terminal hyperlink in custom header and footer surfaces, while keeping ordinary text and every rejected destination free of OSC 8 controls.

Activation remains terminal-owned. Terminals or multiplexers that support OSC 8 make the text followable; others retain the current visual treatment. Mecatui will not add application-owned hit testing, browser launching, shell execution, or a setting to enable the behavior.

## Human decisions

None — the directing operator chose native OSC 8 hyperlinks; the existing `http`/`https`, host-bearing, bounded, no-userinfo destination policy remains unchanged.

## Interface contract

- **gRPC / protobuf:** None — status customization is client-local and no protocol message changes.
- **Exported Go APIs / interfaces:** None — the package-private status renderer changes its terminal output only; no exported symbol or injected URL-opening capability is introduced.
- **Tool schemas:** None — no agent tool or tool schema participates in local status rendering.
- **CLI / config:** The existing client-only StatusML link element with an `href` attribute gains native-terminal rendering semantics; no flag, settings key, default, or precedence rule changes.
- **Events / persistence:** None — no event, stored state, migration, or session data changes.
- **Security / authority:** Existing StatusML validation is the sole destination boundary: only previously retained bounded `http`/`https`, host-bearing, userinfo-free URLs may be placed in an OSC 8 sequence. Sanitized display text cannot contribute controls or a destination. Mecatui does not pass a link to an OS opener, shell, or the MCP-only `OpenURL` dependency.
- **Compatibility / migration:** Compatible presentation enhancement for accepted custom StatusML links. Supporting terminals gain a native hyperlink; unsupported terminals preserve the current themed-underlined text. Existing markup and non-link spans render as before.

## In scope — 1 scenario, in implementation order

### Scenario 1 — Valid custom StatusML links become safe terminal hyperlinks

The status parser already separates `Href` from text and rejects unsafe destinations in [`cmd/mecatui/customization/statusml.go`](../../cmd/mecatui/customization/statusml.go). The UI currently styles an `Href` span but deliberately avoids OSC 8 in [`cmd/mecatui/ui/statusline.go`](../../cmd/mecatui/ui/statusline.go). The implementation changes only that rendering boundary, using the already-installed terminal styling library; visible-width layout continues to measure sanitized display text rather than terminal control bytes. It preserves the root [AGENTS.md](../../AGENTS.md) terminal-sanitization and no-shell-execution safety boundary.

**Acceptance:**
- AC1.1: A valid StatusML `<link>` span in either custom header or footer emits a correctly paired OSC 8 hyperlink around its sanitized visible text while retaining the active theme's link color and underline.
  - verify: `TestStatusHyperlinks_Scenario1_ValidLinkEmitsPairedOSC8`
- AC1.2: Adjacent linked and unlinked spans preserve their individual style and activation boundaries; a hyperlink reset follows each linked span so no later status text inherits its destination.
  - verify: `TestStatusHyperlinks_Scenario1_AdjacentSpansDoNotLeakHyperlink`
- AC1.3: Rejected schemes, userinfo, controls, malformed markup, and plain or semantic StatusML text emit no OSC 8 sequence; their existing safe display behavior remains intact.
  - verify: `TestStatusHyperlinks_Scenario1_RejectedOrNonLinkTextEmitsNoOSC8`
- AC1.4: Status-line width selection, header safety/navigation chrome, footer activity chrome, and inline/no-mouse terminal ownership remain unchanged; OSC 8 support does not introduce Mecatui mouse routing or browser-launch behavior.
  - verify: `TestStatusHyperlinks_Scenario1_HyperlinksPreserveStatusLayout`
- AC1.5: Terminal users can discover that StatusML links use native terminal hyperlink support and retain visual fallback where their terminal or multiplexer does not activate OSC 8.
  - verify: inspection — `user-docs/mecatui/status-line.md` documents the implemented behavior and terminal-dependent activation

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Mecatui-owned mouse hit regions for header/footer links | Separate bounded feature | It requires frame-scoped geometry, routing, stale-frame handling, and a distinct action contract. |
| Browser/OS URL launching | Separate authority design | Existing `OpenURL` is restricted to MCP authorization presentation URLs and must not be broadened implicitly. |
| Arbitrary URI schemes, raw ANSI/OSC markup, images, or interactive StatusML controls | Separate security and grammar design | StatusML retains its present bounded URL validation and semantic-text model. |
| Guaranteed hyperlink activation in every terminal, multiplexer, or remote session | Not actionable in Mecatui | OSC 8 interpretation is terminal-owned; the existing link styling is the fallback. |

## Definition of done

1. Focused offline status parser and UI renderer tests pass, followed by `task test` for the integrated change.
2. `task lint && task test:race` pass before the implementation PR is ready.
3. `task docs` and `task site:build` pass after the owning user guide is updated.
4. `go run ./cmd/mecademo` remains green and the offline demo still shows a tool call, permission ask/approval, and result.
5. `task ac-trace-strict` resolves every named proof when the implementation candidate proposes `landed`.
6. `/panel-review` reports no ship blocker or unwaived reviewer failure on the implementation candidate.
7. The implementation PR links this merged Plan / Interface PR and reports conformance with every interface-contract clause.

## Deferred decisions and known risks

- OSC 8 support and click gestures vary by terminal emulator and multiplexer. Tests prove emitted terminal bytes and safe fallback, not external browser activation.
- Mouse capture is deliberately separate from OSC 8 rendering. Some terminals may require a modifier key while an application captures mouse input; Mecatui will not synthesize an alternate click action.
- The implementation must preserve the existing sanitization and URL-validation path rather than constructing an OSC 8 destination from display text.
