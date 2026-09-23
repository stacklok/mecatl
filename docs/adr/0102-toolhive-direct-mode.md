# ADR 0102 — ToolHive LLM gateway DIRECT mode (in-process OIDC token injection)

- Status: Accepted
- Date: 2026-08-11
- Scope: `internal/adapter/toolhivellm` (`tokensource.go`, new), `internal/app` (`registry.go` `newDirectGatewayEntry`/`bearerRoundTripper`/`resolveToolhiveIntent`/`directBaseURL`/`gatewayURLIsHTTPS`, `build.go` `validateToolhiveLLMMode`/`validToolhiveLLMMode`), `internal/cliconfig` (`--toolhive-llm-mode`), the four `cmd/` mains, `cmd/mecatui/login.go` (new), the `github.com/stacklok/toolhive` dependency (v0.31.0 → v0.40.0)
- Supersedes: none
- Superseded by: none

## Context

ADR 0064 shipped the ToolHive LLM gateway as an intent-driven provider that talks to a
**local reverse proxy** (`thv llm proxy`, loopback `127.0.0.1:<port>/v1`). That proxy holds the
operator's OIDC credential and forwards every request to the real `gateway_url` after stamping
`Authorization: Bearer <token>`. v1 was deliberately loopback-only: the probed/routed base URL was
hardcoded and the config-file `gateway_url` was diagnostic-only ("never touches request
construction"), and ADR 0064 D8 recorded "No ToolHive Go import, anywhere, ever" as a v1-permanent
invariant and D8-addendum sketched a future `thv llm token` subprocess credential-helper as the
favored off-host direction.

The operator requirement that came back (issue #265) was narrower and different from that sketch:
talk to the real `gateway_url` DIRECTLY from mecatl — no local proxy hop, no subprocess — by
importing ToolHive as a Go library and building the SAME in-process OIDC token source
(`llm.NewTokenSource`) that `thv llm token` and the ToolHive proxy already use. The subprocess
credential helper was explicitly rejected by the operator; the in-process library path was the
ask. This reopens two of ADR 0064's v1 cuts — the loopback-only routing invariant and the
"No ToolHive Go import" invariant — for this one feature, while leaving the rest of ADR 0064
(register-on-intent, the disclosure surface, the accepted sole+probe-down boot deviation) intact.

Four design questions had to be answered before any code landed. They are the load-bearing
decisions this ADR records.

### The four questions

1. **Token-injection seam.** The token has to ride every inference and listing request to the real
   `gateway_url`. A `port.LLMProvider` decorator would sit OUTSIDE the construction closure, and
   per-session/per-heal re-minting re-mints the INNER adapter (`llmresilience.Wrap(openai.New(...))`),
   so a decorator would be lost on re-mint or need a second wrapping point — exactly the drift the
   0064 construction path exists to prevent.

2. **Config / mode surface.** Direct mode needs the OIDC trio (`gateway_url` + `issuer` +
   `client_id`, i.e. `llm.Config.IsConfigured()`) to have anything to inject. A flat "direct on/off"
   flag would either silently fall back to proxy when OIDC is absent (surprising an operator who
   asked for direct) or brick boot (surprising an operator who merely hadn't run setup yet).

3. **`tls_skip_verify` gap.** `pkg/auth/oauth/oidc.go` <!-- lint:not-a-citation: path inside the toolhive dependency, not a repo file --> builds its own OIDC-discovery `http.Client`
   with no `InsecureSkipVerify` plumbing, and `pkg/llm`'s token source inherits that client. A
   self-signed gateway cert — common in dev — fails in direct mode with an opaque TLS error. The
   proxy mode honors `tls_skip_verify` (via `WithTLSSkipVerify` in `pkg/llm/proxy/proxy.go` <!-- lint:not-a-citation: path inside the toolhive dependency, not a repo file -->); direct
   mode cannot.

4. **Login UX.** A non-interactive cache miss (`llm.ErrTokenRequired`) is the headless case:
   `mecated` runs `--headless` (explicit deployment identity, per `Config.Headless`), and a browser
   popup from a daemon is wrong. The interactive case (`mecatui`) hosts its own in-process `mecated`
   over a UNIX socket and CAN launch a browser — so the operator should be able to log in without
   leaving the terminal or running a separate `thv` binary.

## Decision

mecatl imports ToolHive as a Go library (one file: `internal/adapter/toolhivellm/tokensource.go`) and
builds an in-process OIDC token source so the `toolhive` LLM provider can talk DIRECTLY to the real
`gateway_url` with the bearer injected per-request by an `http.RoundTripper`. No subprocess, no local
proxy hop. A new `--toolhive-llm-mode auto|proxy|direct` flag (default `auto`) selects the routing.

### 1 — Token-injection seam: `openai.WithHTTPClient` + a bearer `RoundTripper`

The token rides a custom `http.RoundTripper` inside the `*http.Client` passed to
`openai.WithHTTPClient` — NOT a `port.LLMProvider` decorator. `newOpenAICompatEntry`
(`internal/app/registry.go`) already closes over `extra ...openai.Option` and appends them to EVERY
`construct()` mint (the default provider AND every per-session `remintEntry` re-mint), so a
`WithHTTPClient` option passed to the direct-mode entry rides every re-mint for free — zero drift,
the same property the 0064 proxy entry relies on for `openaicompat.RefuseRedirects`.

`bearerRoundTripper` (`internal/app/registry.go`) strips any `Authorization` header the openai-go
SDK stamped before the transport fires (the SDK's `SetAPIKey` stamps a placeholder
`Bearer <key>` via `authPreference=bearer` before `*http.Client.Transport` sees the request) and sets
`Bearer <real-token>`, mirroring exactly what the ToolHive proxy does in its `Rewrite`
(`pkg/llm/proxy/proxy.go` <!-- lint:not-a-citation: path inside the toolhive dependency, not a repo file -->: `pr.Out.Header.Del("Authorization")` then `Set("Authorization", "Bearer
"+tok)`). The placeholder is never sent on the wire. The HTTP client composes TWO policies:
`bearerRoundTripper` (token injection) over the SDK default transport, AND `openaicompat.RefuseRedirects`
(CWE-918 — the gateway_url is operator-configured HTTPS, but a redirect must never bounce the
conversation body + bearer off the intended host). Both ride every re-mint. The token source is
built ONCE (`toolhivellm.DirectTokenSource`); a per-request `Token(ctx)` call handles refresh
internally, so the `RoundTripper` is stateless across requests.

The token NEVER enters a log, an error string, or an environment variable. It lives in the OS
keyring (`pkg/secrets`); only its REFERENCE (`CachedRefreshTokenRef`) is persisted to config. The
`RoundTripper` must never log `req.Header.Get("Authorization")` — and by construction it does not
(the `llmresilience` wrapper logs only provider-id/model/base_url, never the Authorization header;
`bearerRoundTripper.RoundTrip` returns the error as-is with no header echo). Errors are sanitised
via `llm.SanitizeTokenError` (strips any bearer material an OIDC IdP echoes back in a
`RetrieveError` body) before they cross any boundary.

### 2 — The mode flag: `--toolhive-llm-mode auto|proxy|direct`, default `auto`

`resolveToolhiveIntent` (`internal/app/registry.go`) gains a routing-mode discriminator driven by
`cfg.ToolhiveLLMMode`:

- **`auto`** (default): select DIRECT when the OIDC trio is configured
  (`toolhivellm.OIDCConfigured` over the same config read `DetectConfig` validated) AND the
  `gateway_url` is HTTPS (`gatewayURLIsHTTPS` — `https://`, or `http://localhost`/`http://127.0.0.1`
  as a dev carve-out; a non-HTTPS gateway_url would send the bearer over cleartext, CWE-319). When
  OIDC is absent OR the gateway_url is not HTTPS, fall back to PROXY with a WARN (byte-identical to
  pre-#265 behaviour when OIDC is absent — a proxy-only operator sees zero change). This is the
  HTTPS gate: `auto` never silently routes a bearer over cleartext.
- **`proxy`**: force the loopback path regardless of OIDC. The escape hatch for a misconfigured
  OIDC block or a self-signed gateway cert.
- **`direct`**: force direct. Build-fails fast (`validateToolhiveLLMMode`, `internal/app/build.go`)
  when OIDC is not configured, naming the missing fields and the remediation — never a silent
  fallback to proxy (the operator asked for direct and would be surprised by a loopback that has no
  token to inject).

The direct base URL is DERIVED, never hand-set: `gateway_url + "/v1"` (`directBaseURL`,
mirroring what the ToolHive proxy forwards — `pkg/llm/proxy/proxy.go` <!-- lint:not-a-citation: path inside the toolhive dependency, not a repo file --> sets the upstream URL verbatim
and forwards every path, so the gateway serves `/v1/models` at `gateway_url + /v1/models`). There
is no `--toolhive-llm-direct-base-url` flag: it would duplicate `--toolhive-llm-base-url`'s
security-sensitive surface for zero gain, and the config file's `gateway_url` is already the source
of truth (the operator set it via `thv llm config set`). An explicit `--toolhive-llm-base-url`
ALWAYS forces proxy mode (it is a loopback address; direct derives its base URL from the config) —
the override is the documented proxy escape hatch, so `--toolhive-llm-mode` is IGNORED on that
path, and `validateToolhiveLLMMode` rejects `direct` + `--toolhive-llm-base-url` as contradictory.

### 3 — `tls_skip_verify` is NOT honored in direct mode (v1 limitation, documented)

Direct mode does NOT honor `tls_skip_verify` from the ToolHive config. A self-signed gateway
certificate requires `--toolhive-llm-mode proxy` (which DOES honor it). This is an upstream ToolHive
gap (no `InsecureSkipVerify` plumbing on the OIDC-discovery client or the token source), not a
mecatl choice; fixing it requires a ToolHive release. The documentation (this ADR + `docs/usage.md`
+ `user-docs/`) states the one-line remediation explicitly so it is never a silent failure: the TLS
error surfaces at the first request, and the operator switches to `proxy`. A future ToolHive bump
that closes the gap removes the limitation with a one-line code change and a docs edit.

### 4 — Login UX: fail-with-instructions in mecated, interactive in mecatui

When direct mode is active but `Token(ctx)` returns `llm.ErrTokenRequired` (no cached credential,
non-interactive), the provider surfaces a terminal error carrying
`toolhivellm.ErrTokenRequiredHint`:
`"no cached ToolHive LLM gateway credential — run \`thv llm setup\` (or \`mecatui login\`) to log
in, or use --toolhive-llm-mode proxy"`. Both remediations are named, mirroring the existing
`errToolhiveNoModels` actionable-error pattern.

`mecatui login` (`cmd/mecatui/login.go`) is a NEW CLI-only subcommand that runs the interactive
OIDC browser flow IN-PROCESS (the SAME `toolhivellm.RunInteractiveLogin` pipeline —
`buildTokenSource` with `interactive=true` — that `thv llm token` and the ToolHive proxy use, so
there is ONE token-source construction path, not two). It is NOT a session and NOT a transport: it
writes a refresh-token REFERENCE (never the token value) into ToolHive's own config so a subsequent
non-interactive direct-mode provider finds the credential without re-login. A `--skip-browser`
flag prints the authorization URL instead of opening a browser (headless/SSH/CI). It runs in the
normal buffer (no alt screen). mecated stays headless — a browser popup from a daemon is wrong;
its only login surface is the actionable error above.

### Version-bump rationale

The ToolHive dependency is bumped v0.31.0 → v0.40.0. v0.40.0 is the EARLIEST release with
`pkg/llm.NewTokenSource` (the in-process token source this feature imports). Pinning the earliest
version minimizes the k8s/controller-runtime + AWS SDK drag the bump carries (the
`internal/adapter/mcp/source/toolhive.go` single import already compiles those, so the risk is
version skew, not new imports). A later v0.42.0+ pulls `modelcontextprotocol/go-sdk` v1.7.0, which
has a goroutine leak that would regress `task test`'s goleak gate; v0.40.0 is the version that
AVOIDS that leak. The `engine/` module stays clean — `engine/` imports no toolhive symbol; the
depguard allowlist scopes `github.com/stacklok/toolhive/*` to
`internal/adapter/toolhivellm/tokensource.go` ONLY, and `task test:engine-standalone`
(`GOWORK=off`) is the CI gate.

## Consequences

**Easier:** a ToolHive LLM gateway operator with the OIDC trio configured gets a working
direct-to-gateway session with zero local proxy to run — no `thv llm proxy start`, no loopback
listener, just `mecatui login` (or `thv llm setup`) once and then `mecatui`/`mecated`. The bearer
is minted and refreshed in-process; the only thing on the wire is the real gateway URL. The
proxy-mode path is unchanged and remains the fallback for self-signed certs or a misconfigured
OIDC block.

**Harder / costs paid honestly:**

- **One ToolHive Go import exists where ADR 0064 D8 said none ever would.** It is confined to a
  SINGLE file (`internal/adapter/toolhivellm/tokensource.go`); the rest of the package
  (`detect*.go`) stays stdlib + `go.yaml.in/yaml/v3`, and the detector's
  "tls_skip_verify/oidc never decoded" invariant (pinned by
  `TestDetectConfig_TLSSkipVerifyNeverDecoded`) is untouched — the OIDC-presence check lives in
  `tokensource.go` over toolhive's own config read, not in `DetectConfig`. The depguard allowlist
  and the `GOWORK=off` standalone build are the two machine-enforced guards that keep it that way.
- **`tls_skip_verify` is not honored in direct mode.** An operator with a self-signed gateway
  must use `--toolhive-llm-mode proxy`. Documented in three places (this ADR, `docs/usage.md`,
  `user-docs/`); not a silent failure (the TLS error surfaces at the first request).
- **The dependency graph grows.** The bump drags k8s/controller-runtime and the AWS SDK forward;
  the risk is version skew, contained by the single-import-file discipline and the engine-module
  hygiene gate.
- **The mode discriminator is a third routing decision in `resolveToolhiveIntent`.** It rides the
  existing intent-only registration (the proxy probe still never gates whether the entry exists),
  but a future fourth mode or a second gateway-shaped provider would need a per-vendor hint table
  (the `statusHintFor` trip-wire from ADR 0064's cleanup generalizes).

The `auto` default is byte-identical to pre-#265 behaviour when OIDC is absent: a proxy-only
operator sees zero change. When the OIDC trio is present, `auto` upgrades to direct (the operator
already did `thv llm setup` and has a cached credential) — this is the only behaviour change a
pre-existing operator can see, and it removes a hop.

## See also

- [ADR 0064 — Auto-detect the ToolHive LLM gateway proxy as a native provider](./0064-toolhive-llm-gateway-provider.md)
  — the proxy-mode feature this extends; the register-on-intent, disclosure surface, and
  accepted sole+probe-down boot deviation all remain in force.
- [Architecture guide — providers](../architecture/providers.md) — the intent-driven availability
  paragraph, now with the direct-mode row.
- [Usage guide](../usage.md) — the ToolHive LLM gateway walkthrough, the `--toolhive-llm-mode`
  table, `mecatui login`, the headless remediation, and the `tls_skip_verify` → proxy note.
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — the direct-mode token-source
  section beside the `openaicompat` + `toolhivellm` entry.
- [ADR 0002 — Documentation lifecycle](./0002-documentation-lifecycle.md) — the ADR freeze
  convention this record follows.
