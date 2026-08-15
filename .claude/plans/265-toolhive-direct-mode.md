# Issue #265 — ToolHive LLM DIRECT mode (in-process OIDC)

## Goal + non-goals

**Goal.** mecatl imports ToolHive as a Go library (`pkg/llm`, `pkg/auth/secrets`,
`pkg/secrets`, `pkg/config`) and builds an in-process OIDC token source — the SAME
`llm.NewTokenSource` that `thv llm token` and the ToolHive proxy use — so the `toolhive`
LLM provider can talk DIRECTLY to the real `gateway_url` (no local proxy hop), with the
token obtained and refreshed in-process, never through a subprocess.

**Non-goals (explicitly out of scope for v1):**

- No subprocess credential helper (`thv llm token`). The operator rejected it.
- No Claude-Desktop config patching (the `configured_tools` write path).
- No toolhive-core graduation — the import stays in ONE file in `internal/adapter/toolhivellm`.
- No `tls_skip_verify` support in direct mode (upstream gap; documented, not fixed).
- No interactive login in mecated (headless by definition). Login lives in mecatui only.
- No new `port.LLMRequest` field — the token rides an HTTP transport, never the request struct.

---

## The chosen answers to the 4 open questions

### 1. Token-injection seam: `openai.WithHTTPClient` + a bearer RoundTripper

**Decision:** inject the token via a custom `http.RoundTripper` inside the `*http.Client`
passed to `openai.WithHTTPClient` — NOT a `port.LLMProvider` decorator.

**Justification.** `newOpenAICompatEntry` (internal/app/registry.go:638) already closes
over `extra ...openai.Option` and appends them to EVERY `construct()` mint (line 673),
which is the single construction path for the default provider AND every per-session
`remintEntry` re-mint (reasoning-effort/capability). A `WithHTTPClient` option passed
to `newGatewayEntry` (line 1034) therefore rides every re-mint for free — zero drift.
A `port.LLMProvider` decorator would sit OUTSIDE this closure: it would wrap the
resilience-wrapped adapter at `.provider`, but `remintEntry` re-mints the INNER adapter
(the `construct` closure returns `llmresilience.Wrap(openai.New(...))`, line 674-687),
so the decorator would either (a) be lost on re-mint, or (b) need a second wrapping point
inside `construct`, duplicating the injection logic. The SDK header/timing chain is:
`option.WithAPIKey` sets `Authorization: Bearer <placeholder>` on the request, then the
`*http.Client`'s `Transport` (our RoundTripper) fires and REWRITES that header before the
wire. Confirmed by reading the openai-go SDK: `SetAPIKey` sets `authHeaderOverride=false`
and `authPreference=bearer` (internal/requestconfig/requestconfig.go:664-668), and the
Authorization header is applied by the SDK's internal transport before the RoundTripper
sees the request. The RoundTripper strips the placeholder and sets the real token —
mirroring exactly what the ToolHive proxy does in its `Rewrite` func (pkg/llm/proxy/proxy.go:144-150:
`pr.Out.Header.Del("Authorization")` then `pr.Out.Header.Set("Authorization", "Bearer "+tok)`).
The token never enters an env var (it lives in the OS keyring, accessed in-process), and
envscrub's `*_TOKEN`/`*_KEY` denylist (internal/adapter/envscrub/envscrub.go:45-64) is a
second-layer defense that would catch it even if it leaked. Logs: the RoundTripper receives
the request object; it must never log `req.Header.Get("Authorization")`. The existing
`llmresilience` wrapper (registry.go:675) logs only provider-id/model/base_url — never
the Authorization header — so the token stays out of logs by construction.

### 2. Config/mode surface: `--toolhive-llm-mode auto|proxy|direct`, default `auto`

**Decision:** add a `--toolhive-llm-mode` flag with three values, defaulting to `auto`.
- `auto`: if ToolHive config has `llm:` `IsConfigured()` (gateway_url + issuer + client_id),
  use DIRECT mode; if only `llm:` exists but `IsConfigured()` is false, fall back to PROXY
  mode (today's behavior). If no `llm:` block at all, no registration (today's behavior).
- `proxy`: force today's proxy behavior regardless of OIDC config.
- `direct`: force direct mode; fail Build if `IsConfigured()` is false.

**Justification.** `resolveToolhiveIntent` (registry.go:979) already returns
`(baseURL, gatewayURL, explicit, ok)` — the `gatewayURL` field is captured but unused for
routing (it is diagnostic-only per ADR 0064 D5). Direct mode is the first consumer of
`gateway_url` as a REQUEST URL. The `auto` default preserves existing proxy-mode behavior
byte-for-byte when no OIDC config exists (a proxy-only user sees zero change), and upgrades
to direct when the OIDC trio is present (the operator did `thv llm setup` and has a cached
credential). The base URL for direct mode is derived, never hand-set: `gateway_url + "/v1"`
— confirmed by pkg/llm/proxy/proxy.go:144 (`pr.SetURL(p.gatewayURL)`) which forwards every
path verbatim, so the gateway serves `/v1/models` at `gateway_url + /v1/models`. A
`--toolhive-llm-direct-base-url` flag would duplicate `--toolhive-llm-base-url`'s
security-sensitive surface for zero gain — the config file's `gateway_url` IS the source
of truth, and the operator already set it via `thv llm config set`. The `proxy` value is
the escape hatch for a misconfigured OIDC block; `direct` is the explicit opt-in for CI.

### 3. tls_skip_verify gap: documented v1 limitation, not an upstream patch

**Decision:** v1 documents the limitation: direct mode does NOT honor `tls_skip_verify`
from the ToolHive config. A self-signed gateway cert requires the proxy mode (which DOES
honor it, via `WithTLSSkipVerify` in pkg/llm/proxy/proxy.go:53-66).

**Justification.** The gap is real: `pkg/auth/oauth/oidc.go` builds its own `http.Client`
for OIDC discovery (line 97) with no `InsecureSkipVerify` plumbing, and `pkg/llm`'s token
source inherits that client — there is no config knob to thread TLS skip through. Fixing
it requires an upstream ToolHive change (adding a TLS option to the token source or the
OIDC client builder), which is outside this issue's scope and would delay the feature by
a ToolHive release cycle. The proxy mode already handles this case correctly. The
documentation (docs/usage.md + the new ADR) states: "if your gateway uses a self-signed
certificate, use `--toolhive-llm-mode proxy`" — a one-line remediation, not a silent
failure. A future ToolHive bump that closes the gap removes the limitation with a
one-line code change and a docs edit.

### 4. Login UX: fail-with-instructions in mecated, interactive login in mecatui

**Decision.** When direct mode is active but `Token(ctx)` returns `llm.ErrTokenRequired`
(no cached credential, non-interactive), the provider surfaces a terminal error with an
actionable hint. In mecated (headless), that hint is "run `thv llm setup` to log in, or
use `--toolhive-llm-mode proxy`". In mecatui (interactive), the same error surfaces in
the TUI, and a NEW `mecatui login` subcommand runs the interactive OIDC flow in-process
(calling `llm.NewTokenSource` with `interactive=true`), so the operator never leaves the
terminal.

**Justification.** mecated runs `--headless` (AGENTS.md: `Config.Headless` is explicit
deployment identity) — a browser popup from a daemon is wrong. The error message must
name the exact remediation, mirroring the existing `errToolhiveNoModels` pattern
(registry.go:1088-1089). mecatui is the interactive surface: it hosts an in-process
mecated over a UNIX socket and CAN launch a browser. The `login` subcommand is a thin
wrapper around `llm.NewTokenSource(interactive=true).Token(ctx)` — the SAME library call
the direct-mode provider makes non-interactively, so there is one code path, not two.
The `TokenRefUpdater` callback persists the new `CachedRefreshTokenRef`/`CachedTokenExpiry`
via `config.UpdateConfig` (pkg/config/config.go:333), so a subsequent non-interactive
`Token()` call finds the credential. No new proto, no new wire message — the login is a
CLI operation, not a session operation.

---

## Task breakdown

| # | Task | Size | Complexity | Deps | Pass |
|---|------|------|------------|------|------|
| 1 | **Bump toolhive dependency** — `go.mod` from v0.31.0 to a version with `pkg/llm.NewTokenSource` (≥v0.42.0). Verify `pkg/llm/tokensource.go` exists at the pinned version. Run `go mod tidy` + `go work sync`. Assess the k8s/controller-runtime drag — if the bump is too heavy, pin to the EARLIEST version with the token source. | S | mechanical | none | single |
| 2 | **New file: `internal/adapter/toolhivellm/tokensource.go`** — the single ToolHive-import file. Exports one func: `DirectTokenSource(configPath string) (TokenSourceFunc, error)` where `TokenSourceFunc = func(ctx context.Context) (string, error)`. Internally: reads ToolHive config via `config.LoadOrCreateConfigWithPath` (or the same hardened read as detect.go, then construct `llm.Config` from it), gets `secrets.GetSystemSecretsProvider()` → `secrets.NewScopedProvider(p, secrets.ScopeLLM)`, builds `llm.NewTokenSource(&llmCfg, scoped, false, false, configPersister)`. The `configPersister` is `config.UpdateConfig` wired to update `LLM.OIDC.CachedRefreshTokenRef`/`CachedTokenExpiry`. Returns `llm.SanitizeTokenError`-wrapped errors. This file is the ONLY file allowed to import `github.com/stacklok/toolhive/pkg/llm`/`pkg/secrets`/`pkg/config`/`pkg/auth/secrets`. Update the package doc-comment (detect.go:1-46) to remove the "NO ToolHive Go import" invariant and replace it with "the ONLY ToolHive Go import in the tree is tokensource.go". | M | moderate | task 1 | single |
| 3 | **Direct-mode registry entry** — `internal/app/registry.go`. Extend `resolveToolhiveIntent` to return a mode discriminator (proxy/direct). When mode=direct: derive base URL as `gateway_url + "/v1"`; build the entry via `newOpenAICompatEntry` with a placeholder key AND a `WithHTTPClient` wrapping the token-source RoundTripper (task 2's output). The RoundTripper: on each `RoundTrip`, call `tokenSource(ctx)`, strip any existing `Authorization` header, set `Bearer <token>`; on error, return a terminal error (no retry — the token source itself handles refresh internally). The direct-mode entry ALSO gets `openaicompat.RefuseRedirects` (compose both into one `http.Client`). The `intentGatewayURL` field already exists for diagnostics; reuse it. The `intentDriven` flag stays true. | M | moderate | tasks 1, 2 | single |
| 4 | **Mode flag surface** — `internal/cliconfig/cliconfig.go`: add `--toolhive-llm-mode` (string, default `"auto"`, validated to `auto\|proxy\|direct`) to `RegisterToolhiveLLMFlags`/`ToolhiveLLMFlags`/`Apply`. Add `ToolhiveLLMMode string` to `app.Config` (build.go). Wire through all four mains: `cmd/mecated/main.go`, `cmd/mecatui/config.go`, `cmd/mecatequi/flags.go`, `cmd/mecak8s/main.go`. Validation: `direct` mode + `!IsConfigured()` = Build-fail with actionable error. | S | mechanical | task 3 | single |
| 5 | **mecatui login subcommand** — `cmd/mecatui/main.go`: add `mecatui login` subcommand that runs the interactive token source (`interactive=true`, `skipBrowser=false`) and prints the result. This is a CLI-only operation — it does NOT start a session or connect to a server. Reuse task 2's construction with `interactive=true`. | S | moderate | task 2 | single |
| 6 | **Tests** — (a) `internal/adapter/toolhivellm/tokensource_test.go`: unit test the RoundTripper with a fake token source (no real keyring), assert Authorization header rewrite, assert error propagation, assert no token in error strings. (b) `internal/app/registry_toolhive_test.go`: extend with direct-mode registration test — config fixture with `oidc{issuer,client_id}` present, assert direct mode selected, base URL = gateway_url + /v1. (c) `internal/app/gateway_redirect_test.go`: extend to assert direct-mode entry also refuses redirects. (d) E2E offline: a test through the real `app.Build` composition path with a `toolhiveConfigPath` fixture pointing at a config with OIDC fields, asserting the registry entry is direct-mode and the provider is constructed without error (mock the token source via a composition seam — the `providerConstructor` test seam at registry.go:644 already exists). | M | moderate | tasks 2-4 | single |
| 7 | **Docs** — (a) New ADR `docs/adr/0102-toolhive-direct-mode.md` (copy template.md): the mode flag, the token-injection seam, the tls_skip_verify limitation, the login UX. (b) `docs/design/IMPLEMENTATION-NOTES.md`: add the direct-mode token-source section. (c) `docs/architecture/providers.md`: add the direct-mode row. (d) `docs/usage.md`: extend the ToolHive LLM gateway section with the mode flag table, the login instructions, the tls_skip_verify note. (e) `AGENTS.md`: update the `internal/adapter/` adapter-list line for `toolhivellm` (remove "NO ToolHive Go import", add the tokensource.go exception). (f) `user-docs/`: extend the ToolHive gateway page (or add one) with the direct-mode + login instructions. (g) Run `task generate` to refresh llms.txt. | M | mechanical | tasks 2-5 | single |
| 8 | **Engine-module hygiene proof** — `cd engine && go test ./...` (GOWORK=off standalone build must pass — the engine module must NOT import toolhive). The depguard allowlist in `.golangci.yml` must be updated to allow `github.com/stacklok/toolhive/*` ONLY in `internal/adapter/toolhivellm/tokensource.go`. | S | mechanical | tasks 1-4 | single |

**Ordering:** 1 → 2 → 3 → 4 (parallel with 5) → 6 → 7 → 8. Tasks 4 and 5 are
independent of each other but both depend on 2-3. Task 6 depends on all of 2-4.
Tasks 7-8 are terminal.

---

## Acceptance criteria

1. **Direct mode registers when OIDC is configured.** Given a ToolHive config with
   `llm:{gateway_url, oidc:{issuer, client_id}}`, Build registers a `toolhive` provider
   whose base URL is `gateway_url + "/v1"` (not the loopback proxy).
   verify: `go test ./internal/app/ -run TestToolhiveDirectMode` — a composition test
   with a `toolhiveConfigPath` fixture containing the OIDC trio asserts the entry's
   `baseURL` field equals `gatewayURL + "/v1"`.

2. **Proxy mode is the fallback when OIDC is absent.** Given a ToolHive config with
   `llm:{gateway_url}` but NO `oidc` block (or `IsConfigured() == false`), Build registers
   the same proxy-mode entry as before (base URL = `http://127.0.0.1:<port>/v1`).
   verify: `go test ./internal/app/ -run TestToolhiveProxyFallback` — existing behavior
   is byte-identical.

3. **The token rides every request, never logged.** The direct-mode provider's HTTP
   transport rewrites `Authorization: Bearer <placeholder>` to `Bearer <real-token>` on
   every request. No log line, no diagnostic, no error message contains the token value.
   verify: `go test ./internal/adapter/toolhivellm/ -run TestTokenRoundTripper` — unit
   test asserts header rewrite with a fake token source; a second test asserts the error
   path produces a sanitized error string (via `llm.SanitizeTokenError`) with no bearer
   material.

4. **Direct mode survives per-session re-minting.** A session that triggers `remintEntry`
   (different reasoning effort or capability intersection) gets a provider whose HTTP
   client STILL injects the token.
   verify: `go test ./internal/app/ -run TestToolhiveDirectRemint` — assert the
   re-minted provider's transport is the token-injecting one (inspect via the
   `providerConstructor` test seam or a type-assertion on the `*http.Client`'s Transport).

5. **`--toolhive-llm-mode proxy` forces proxy even with OIDC configured.**
   verify: `go test ./internal/app/ -run TestToolhiveModeFlagProxyOverride` — config
   fixture with OIDC trio + `ToolhiveLLMMode: "proxy"` asserts loopback base URL.

6. **`--toolhive-llm-mode direct` fails fast when OIDC is absent.**
   verify: `go test ./internal/app/ -run TestToolhiveModeFlagDirectRequiresOIDC` —
   config fixture with NO `oidc` block + `ToolhiveLLMMode: "direct"` asserts Build
   returns an error naming the missing fields.

7. **mecated headless login failure is actionable.** When direct mode is active and
   `Token(ctx)` returns `ErrTokenRequired`, the provider surfaces a terminal error
   containing "run `thv llm setup`" or "use `--toolhive-llm-mode proxy`".
   verify: `go test ./internal/adapter/toolhivellm/ -run TestTokenRequiredHint` —
   fake token source returns `ErrTokenRequired`, assert the wrapped error string.

8. **`mecatui login` runs the interactive flow.**
   verify: manual — run `mecatui login` in a terminal with a browser; assert the OIDC
   flow completes and `thv llm token` (or a subsequent `mecatui` session) works without
   re-login. Automated: `go test ./cmd/mecatui/ -run TestLoginCommandExists` (smoke —
   asserts the subcommand parses).

9. **Engine module stays clean.** `cd engine && go test ./...` (GOWORK=off) passes;
   no engine file imports `github.com/stacklok/toolhive`.
   verify: `task test` (includes the engine-standalone hygiene proof).

10. **llms.txt is fresh.**
    verify: `task generate` produces no diff in `llms.txt`.

---

## Risks

1. **Version-bump blast radius.** Bumping toolhive from v0.31.0 to ≥v0.42.0 drags
   k8s.io/controller-runtime and the AWS SDK forward. The `internal/adapter/mcp/source/toolhive.go`
   single-import file already compiles these, so the risk is version skew, not new
   imports. Mitigation: pin to the EARLIEST version with `pkg/llm.NewTokenSource`
   (check `git log --oneline -- pkg/llm/tokensource.go` in the toolhive repo); run
   `task test` + `task lint` immediately after the bump to catch breakage.

2. **tls_skip_verify gap.** A self-signed gateway cert fails in direct mode with an
   opaque TLS error. Mitigation: documented in the ADR + usage.md with the proxy-mode
   remediation. Not a silent failure — the error surfaces at the first request.

3. **Secret handling.** The token lives in the OS keyring, accessed in-process via
   `pkg/secrets`. It never enters an env var (envscrub's `*_TOKEN` denylist is a
   second-layer defense). The RoundTripper must never log the Authorization header —
   enforced by the unit test (criterion 3). The `tokenRefUpdater` callback persists
   only the REFERENCE (`CachedRefreshTokenRef`), never the token value, via
   `config.UpdateConfig`.

4. **Engine-module hygiene.** The engine module (`engine/`) must NOT import toolhive.
   The depguard allowlist in `.golangci.yml` scopes the toolhive import to
   `internal/adapter/toolhivellm/tokensource.go` only. The GOWORK=off standalone
   build (`task test:engine-standalone`) is the CI gate.
