# Mecatui remote endpoints and first-use connect sign-in — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — the directing human classified this as Bounded with no ADR (2026-10-01). It adds a client-only settings block and a narrowly gated sign-in path to `connect`. It reuses the existing ADR 0305 discovery, ADR 0286 issuer transports, and ADR 0277 credential lifecycle without changing server, protobuf, engine, or persistence contracts. See the classification risk under Human decisions.
**Decision record:** None — the directing human chose this plan as the sole record. It narrows two existing rules without amending their ADRs: interactive first-use sign-in is now allowed from `connect`, and discovery may use a private issuer only for an operator-pinned endpoint.
**Phase:** Remote mecatui OAuth bootstrap
**Status:** draft, 2026-10-01. Converted from an uncommitted ADR draft ("Operator-configured remote endpoints and first-use connect sign-in") at the directing human's request. That draft is intentionally not committed.
**Delivery:** Split. The change adds a new client settings schema, an authentication flow, a trust exception, and new kind fixture behavior. These interfaces need review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** None — no tracking issue exists yet.
**Plan PR:** [#2049](https://github.com/stacklok/mecatl/pull/2049)

Goal: one command reaches the OIDC-protected `deploy/mecak8s-kind` Keycloak fixture from a
fresh client: `task mecak8s:kind-connect`, or `mecatui connect mecatl-kind` once the endpoint
is configured. The first interactive connect discovers the server's RFC 9728 metadata the way
`mecatui login HOST` does, confirms the discovered tuple, runs the browser login, and then
continues straight into the TUI. Later connects reuse the saved enrollment and never open a
browser.

Today the kind journey takes two commands (`kind-login`, then `kind-connect`). It also needs
six explicit enrollment flags and a repeated `--tls-ca` flag. The fixture cannot use discovery
for three reasons:

- the deployment publishes no protected-resource metadata;
- its issuer resolves to loopback, which public-only discovery rejects;
- the saved enrollment does not remember the gRPC server CA.

## Human decisions

- [x] Work classification and decision record. — Decision: the directing human chose Bounded with no ADR on 2026-10-01; the source ADR draft is not committed or merged. Risk accepted: [ADR 0277](../adr/0277-remote-mecatui-oidc.md) still says `connect` never opens a browser, and [ADR 0305](../adr/0305-oauth-protected-resource-discovery.md) still excludes private-resource discovery. Neither ADR is amended, so this plan is the only record of the two narrowed exceptions.
- [x] `connect` may run discovered enrollment in-process before the TUI starts, but only when every Scenario 2 gate holds. — Decision: directed by the source draft. Saved targets keep ADR 0277 behavior exactly.
- [x] Private discovery may be selected only by an operator-written endpoint entry. It can never come from a command-line address or from metadata. — Decision: directed by the source draft.
- [x] A gRPC server CA set on an endpoint is gRPC trust only, never issuer trust. — Decision: directed by the source draft; it follows ADR 0286 and [ADR 0287](../adr/0287-target-aware-mecatui-tls.md).
- [ ] Settings home. Recommendation: put `remote:` in the strict, client-owned `~/.config/mecatui/settings.yaml`, which no project can write. The source draft instead chose the operator tier of the shared `~/.config/mecatl/settings.yaml`, which would need new project-tier-ignore logic like `telemetry:`. That shared file configures the server, while the remote endpoint is client state.
- [ ] How the kind fixture provides its endpoint. Recommendation: `kind-connect` runs `mecatui` with an absolute, `{{.ROOT_DIR}}`-anchored `XDG_CONFIG_HOME={{.ROOT_DIR}}/.scratch/kind/mecatl-dev/client-xdg` (relative XDG values are ignored and the keyring root must be absolute). That fixture-owned root holds the endpoint settings, the registry, and the credential-store marker, and destroy logs out and then deletes it. Rejected alternatives: editing the operator's own client settings file from a Taskfile, which risks a lossy YAML merge, and printing a snippet to paste, which breaks the one-command goal.
- [ ] Endpoint name grammar. Recommendation: names match `^[a-z][a-z0-9-]{0,62}$`; `sessions` and `debug` are reserved; an exact name match wins over host interpretation. A name contains no `.` or `:`, so it cannot be mistaken for a DNS host, `host:port`, or URL.

## Interface contract

- **gRPC / protobuf:** None — the change only alters client target resolution and the client sign-in flow. The server's RFC 9728 endpoint and every RPC stay unchanged.
- **Exported Go APIs / interfaces:** None — no `engine/` exported symbol or API snapshot changes. The work lives in `cmd/mecatui` (package `main`) and host-internal `internal/adapter/clientauth`. Any new helper there is unexported, or internal to the host module.
- **Tool schemas:** None — this is not a model-facing surface.
- **CLI / config:** One new client settings block, endpoint-aware positional resolution, two flag-conflict rules, and kind fixture overlay changes. No new flags.
  - **New settings block.** Optional `remote:` block in the client settings file (location set by the settings-home decision). The block is decoded strictly, like the other keys in that file:
    ```yaml
    remote:
      default: mecatl-kind            # optional; must name an entry in endpoints
      endpoints:
        mecatl-kind:                  # name grammar per the endpoint-name decision
          resource: https://mecak8s-mecak8s.mecatl.svc.cluster.local:18081   # required, canonical HTTPS resource URL
          grpc:
            target: mecak8s-mecak8s.mecatl.svc.cluster.local:18080           # optional; default is the resource authority (ADR 0305)
            ca: /abs/path/fixture-ca.crt                                     # optional; absolute; gRPC server trust only
          discovery:
            address_policy: private   # public (default) | private
            ca: /abs/path/fixture-ca.crt                                     # required iff private; absolute; metadata + issuer trust
    ```
  - **Validation.** Each of these makes the settings read fail with a path-qualified error:
    - an unknown key;
    - a relative CA path;
    - `private` without `discovery.ca`, or `discovery.ca` under `public`;
    - a non-canonical resource or target;
    - a dangling `default`;
    - a name that breaks the grammar or uses a reserved word.
  - **Target resolution** for `connect`, `login`, and `logout`. `resolveInvocation` stays pure argv classification; a separate step that receives the decoded `remote:` block resolves endpoint names after parsing and before any network I/O.
    - A positional that exactly matches an endpoint name selects that entry.
    - With a configured `remote.default`: `mecatui connect`, `mecatui connect --flag ...`, `mecatui connect sessions`, and `mecatui connect debug TARGET` use the default, as do `mecatui login` and `mecatui logout` with no positional.
    - Without a default, every invocation parses exactly as today, including `connect sessions` treating `sessions` as an address and the existing "ADDRESS required" errors.
    - Any other positional keeps its current meaning: `host:port`, a bare DNS host, or an `https://` URL.
  - **Enrollment lookup for an endpoint.** The registry row for an endpoint is the single row whose `ResourceURL` equals the endpoint's `resource`. A row that instead matches only the endpoint's gRPC target (for example a legacy explicit `kind-login` row with no `ResourceURL`) is never adopted: connect fails closed and names `mecatui login <name>`. More than one candidate row is rejected as ambiguous, per ADR 0305.
  - **First-use defaults.** First-use sign-in from `connect` uses credential store mode `auto` and the same callback timeout default as `login`; `connect` gains no `--credential-store` or `--timeout` flag.
  - **Flags.** No new flags.
    - `connect` with an endpoint uses the endpoint's `grpc.ca` as its TLS CA. Passing `--tls-ca` together with an endpoint that sets `grpc.ca` is a usage error. `--tls=false` and `--insecure` stay rejected for saved authenticated targets.
    - `login <endpoint>` rejects `--issuer`, `--client-id`, `--audience`, `--tls-ca`, `--private-issuer`, `--grpc-target`, and `--scopes`. The endpoint entry supplies the transport, and discovery supplies the identity.
  - **Kind fixture.** These are fixture overlay changes, not shipped Helm defaults:
    - `deploy/helm/mecak8s/values-kind-keycloak.yaml` adds `oidc.resource: https://mecak8s-mecak8s.mecatl.svc.cluster.local:18081`, `oidc.clientID: mecatui-kind`, and `oidc.scopes: [openid, profile, mecak8s:access, offline_access]`.
    - `kind-connect` writes the fixture endpoint `mecatl-kind` as `remote.default` and runs `mecatui connect`.
    - `kind-login` runs `mecatui login mecatl-kind`.
- **Events / persistence:** None — no new persisted fields. A first-use enrollment writes the existing registry `Connection` fields:
  - `Identity` (the target comes from the endpoint);
  - `ResourceURL`;
  - `IssuerAddressPolicy` (`private` for pinned private discovery);
  - `IssuerCAFile` (the endpoint's `discovery.ca`).

  The credential record key is unchanged. Endpoint names and `grpc.ca` are never persisted to the registry; they are re-read from settings on every run.
- **Security / authority:** Operator-written endpoint settings become a new trust source for private discovery and endpoint CAs; nothing else gains authority.
  - **Trust origin.** Settings written by the operator are the only source of private discovery and of endpoint CAs. A command-line address and server metadata can't select private address admission or custom CA roots.
  - **Discovery transports.** Public discovery keeps the ADR 0305 public-bootstrap transport. Private discovery uses the ADR 0286 private issuer transport for both the metadata fetch and issuer discovery: only private addresses are admitted, and `discovery.ca` replaces the system roots. Both stay anonymous, redirect-free, and bounded, with exact resource and issuer binding.
  - **CA separation.** `grpc.ca` never becomes issuer trust, and `discovery.ca` never becomes gRPC trust.
  - **Browser from connect.** `connect` opens a browser only on interactive first use, after confirmation. It never does so for a saved target, a non-TTY process, a static bearer, `--anonymous`, or a `host:port` target.
  - **Logging.** Credential values never reach logs, prompts, or argv.
- **Compatibility / migration:** Additive client behavior.
  - Without a `remote:` block, every existing invocation resolves and behaves as before. One message changes: a credential-free `connect` to a resource-addressable, unenrolled target on a TTY now offers sign-in instead of printing "run mecatui login".
  - Existing enrollments still match by target and resource.
  - `login` and `logout` now read the strict client settings file, so an invalid client settings file (for example a bad `keymap`) fails them with the same path-qualified error the TUI already reports. This is an accepted behavior change.
  - When a saved enrollment no longer matches a changed endpoint entry (`resource`, `grpc.target`, `address_policy`, or `discovery.ca`), connect fails closed and asks for an explicit `mecatui login <name>`.
  - Kind fixture users drop `kind-login` from the normal journey. Existing explicit-flag commands still work.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — Endpoints resolve remote targets

The operator names a deployment once and refers to it by name or default from `connect`,
`login`, and `logout`. Resolution happens before any network I/O and follows the existing pure
`resolveInvocation` classification in
[`cmd/mecatui/command.go`](../../cmd/mecatui/command.go). The settings use the strict client
decoder in [`cmd/mecatui/client_settings.go`](../../cmd/mecatui/client_settings.go). Settings
stay operator-owned per [AGENTS.md](../../AGENTS.md) (operator grants are explicit).

**Acceptance:**
- AC1.1: Each of these resolves to the same endpoint entry, with its resource, gRPC target, and CAs: `mecatui connect mecatl-kind`, `mecatui connect` with `remote.default: mecatl-kind`, `mecatui connect sessions` with that default, `mecatui login mecatl-kind`, and `mecatui logout mecatl-kind`.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_NameAndDefaultResolution`
- AC1.2: A positional that matches no endpoint name keeps its current meaning, and an exact name match wins over host interpretation. With no `remote:` block, the existing invocation tests still pass unchanged.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_ExplicitAddressUnchanged`
- AC1.3: Each invalid shape listed in the contract fails the settings read with a path-qualified error, before any dial or discovery.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_SchemaValidation`
- AC1.4: `--tls-ca` combined with an endpoint that sets `grpc.ca` is a usage error. Each explicit identity or transport flag on `login <endpoint>` is rejected before discovery.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_FlagConflicts`
- AC1.5: Only the operator-owned file chosen by the settings-home decision is an endpoint source; a `remote:` block in any project-scoped file, or in the other settings file, selects no endpoint.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_OperatorOwnedOnly`

### Scenario 2 — First interactive connect signs in, then connects

When an interactive `connect` reaches an unenrolled, resource-addressable target that requires
authentication, it runs the [ADR 0305](../adr/0305-oauth-protected-resource-discovery.md)
discovered enrollment (confirmation, then the ADR 0277 PKCE login). It then dials again with the
new credential in the same process, before the TUI takes the terminal. Because `client.Dial`
is lazy and the TUI's first RPC is the mutating `CreateSession`, connect adds one explicit
pre-TUI probe: a single credential-free, read-only `ListSessions` first-page request. "Requires
authentication" means that probe returned `Unauthenticated`, which `client.AuthFailure`
classifies as `not_enrolled`. The probe dials the endpoint's `grpc.target`, or for a bare host
or `https://` URL the resource authority (port 443 by default, per ADR 0305), always with
verified TLS. The TUI's existing auth-recovery overlay still never opens a browser. Scope selection follows
[ADR 0316](../adr/0316-server-owned-discovered-oidc-scopes.md). Every other case keeps the
ADR 0277 rule that `connect` only connects
([ADR 0277](../adr/0277-remote-mecatui-oidc.md)).

**Acceptance:**
- AC2.1: All six gates hold for an endpoint target, a bare DNS host, or an `https://` resource URL:
  - the target is resource-addressable;
  - the registry has no entry for it;
  - neither a bearer token nor `--anonymous` is set;
  - the credential-free probe returned `not_enrolled`;
  - stdin and stderr are a TTY;
  - the operator answers `y`.

  When they do, connect shows the same confirmation as `login`, completes PKCE, persists the
  enrollment, re-dials with the bearer, and starts the TUI. The operator types one command and
  makes no second invocation.
  - verify: `TestMecatuiRemoteEndpoints_Scenario2_FirstUseConnectEnrollsAndConnects`
- AC2.2: Each failed gate keeps today's behavior and leaves no registry, credential, or keyring state:
  - a non-TTY run;
  - a declined confirmation;
  - a `host:port` target;
  - `--auth-token` or `--anonymous`;
  - a server that accepts the anonymous dial;
  - a discovery, mismatch, timeout, or cancellation failure.

  Today's behavior means one of three things: the anonymous connection proceeds, the
  actionable "run mecatui login" error appears, or the existing auth-recovery screen opens.
  None of these opens a browser.
  - verify: `TestMecatuiRemoteEndpoints_Scenario2_GatesFailClosedWithoutState`
- AC2.3: A saved target whose credential is expired, unusable, absent, or server-rejected never opens a browser from `connect`. It surfaces the existing ADR 0277 recovery reasons. Live metadata never rewrites a saved enrollment.
  - verify: `TestADR_0277_SavedConnectNeverOpensBrowser`
- AC2.4: The probe is the only extra RPC. A server that accepts anonymous callers receives one `ListSessions` probe and no additional session-creating or other mutating call before the TUI starts. For a bare host or `https://` URL the probe dials the derived resource authority with verified TLS, never the raw URL string.
  - verify: `TestMecatuiRemoteEndpoints_Scenario2_ProbeIsReadOnlyAndTargeted`

### Scenario 3 — Operator-pinned private discovery

An endpoint with `discovery.address_policy: private` and `discovery.ca` discovers metadata and
the issuer through the private issuer transport from
[ADR 0286](../adr/0286-issuer-transport-split.md), not ADR 0305's public bootstrap. That makes
loopback and internal-CA deployments discoverable without opening that path to arbitrary
addresses.

**Acceptance:**
- AC3.1: A private endpoint fetches `/.well-known/oauth-protected-resource` and the issuer's OIDC document from a private-address `httptest` TLS server signed by `discovery.ca`. The confirmed enrollment persists `issuer_address_policy: private` and `issuer_ca_file` set to that CA. Saved refresh then uses the same private transport.
  - verify: `TestMecatuiRemoteEndpoints_Scenario3_PrivateEndpointDiscovers`
- AC3.2: Each of these rejects a private or loopback resource or issuer before any persistence: a command-line URL, a bare host, a public endpoint, and metadata naming a private issuer. Public discovery behavior is unchanged.
  - verify: `TestADR_0305_PublicBootstrapUnchangedForCommandLineTargets`
- AC3.3: A private endpoint rejects a resource or issuer that resolves to a public address. It ignores system roots. It sends no bearer, cookie, or client certificate. It follows no redirect.
  - verify: `TestMecatuiRemoteEndpoints_Scenario3_PrivateTransportBounds`
- AC3.4: The endpoint's `grpc.ca` is never offered as issuer or metadata trust, and `discovery.ca` is never used for gRPC dial verification.
  - verify: `TestMecatuiRemoteEndpoints_Scenario3_CASeparation`

### Scenario 4 — Saved endpoints reconnect with one command and fail closed on drift

After enrollment, `mecatui connect <name>` (or bare `connect` with a default) authenticates
without flags. The gRPC target and the TLS CA come from the endpoint, and the credential and
issuer trust come from the registry. This preserves the separate resource and transport
identities from ADR 0305 and the verified-TLS rules from
[ADR 0287](../adr/0287-target-aware-mecatui-tls.md).

**Acceptance:**
- AC4.1: A saved endpoint connect dials `grpc.target` with verified TLS against `grpc.ca` and the saved bearer, without `--tls` or `--tls-ca`. A new process does the same.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_SavedEndpointReconnect`
- AC4.2: Connect fails closed with a message naming `mecatui login <name>`, and without opening a browser, when the endpoint no longer matches the saved enrollment. That covers a changed `resource`, `grpc.target`, `address_policy`, or `discovery.ca`. A later `login <name>` re-confirms and replaces the enrollment under the existing ADR 0305 rediscovery rules.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_EndpointDriftFailsClosed`
- AC4.3: `mecatui logout <name>` removes the enrollment for the endpoint's resource. The next interactive `connect <name>` is treated as first use again.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_LogoutReturnsToFirstUse`
- AC4.4: Every connect restart action — connect saved, reauthenticate, and retry after credential cleanup — started from an endpoint-resolved connection carries the endpoint name, so the restarted process re-resolves `grpc.target` and `grpc.ca` instead of rebuilding `--tls-ca` from the original flags. A picker row whose `ResourceURL` matches a configured endpoint restarts through that endpoint.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_RestartActionsPreserveEndpoint`
- AC4.5: An endpoint whose gRPC target already has a non-endpoint enrollment (no `ResourceURL`) is not adopted: connect fails closed naming `mecatui login <name>`, and two candidate rows are rejected as ambiguous. Neither case opens a browser.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_LegacyRowNotAdopted`

### Scenario 5 — The kind fixture connects in one command

The [mecak8s kind fixture](mecak8s-kind-fixture.md) publishes RFC 9728 metadata and provides
its endpoint, so `task mecak8s:kind-connect` is the whole journey after
`task mecak8s:kind-keycloak-setup`. The fixture keeps two existing pieces:
- the loopback `/etc/hosts` aliases, including the mecak8s DNS alias that avoids the
  `IsLoopbackHost` workspace footgun;
- per-cluster CA refresh.

Fixture boundaries follow `deploy/mecak8s-kind/README.md`. Its credentials stay isolated per
[AGENTS.md](../../AGENTS.md).

**Acceptance:**
- AC5.1: The Keycloak overlay renders `--oidc-resource=https://mecak8s-mecak8s.mecatl.svc.cluster.local:18081`, `--oidc-client-id=mecatui-kind`, and `--oidc-scopes=openid,profile,mecak8s:access,offline_access`. The base `values-kind.yaml` and the chart defaults gain none of these.
  - verify: `TestMecatuiRemoteEndpoints_Scenario5_KeycloakOverlayPublishesProfile`
- AC5.2: `kind-connect` does four things in order: adds the aliases, refreshes the CA, writes the `mecatl-kind` endpoint as `remote.default`, and runs `mecatui connect`. The fixture `XDG_CONFIG_HOME` and both CA paths are absolute. The endpoint uses the refreshed CA for both `grpc.ca` and `discovery.ca`, with `address_policy: private`. The task passes no explicit issuer, client, audience, or scope flags.
  - verify: `TestMecatuiRemoteEndpoints_Scenario5_KindConnectIsOneCommand`
- AC5.3: `kind-destroy` and the setup reset run a best-effort `mecatui logout mecatl-kind` under the fixture root before deleting the cluster and state, so no root-keyed keyring item outlives the deleted root. A recreated cluster then starts at first use. Using `mecatui connect mecatl-kind` from the operator's personal root is not managed by the fixture; after a recreate it needs an explicit `mecatui login mecatl-kind`, and the README says so.
  - verify: `TestMecatuiRemoteEndpoints_Scenario5_DestroyLogsOutFirst`
- AC5.4: A fresh client root, a recreated cluster, and one `task mecak8s:kind-connect` together show the discovered-tuple confirmation, a browser login, and the TUI connected as the Keycloak user. A second `kind-connect` reconnects without a browser.
  - verify: manual — a live kind cluster and a browser login are operator-run and not part of offline gates; record the result in the implementation PR.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Amending ADR 0277 or ADR 0305 text | Later, if the human wants it | The directing human chose this plan as the sole record |
| A `mecatui remote add/list/remove` command for editing settings | Later UX issue | Operators edit settings; the fixture uses a fixture-owned root |
| A `--grpc-target` flag on `connect` | Later | Split-listener deployments use an endpoint entry |
| Device-code or other non-browser grants for headless first use | Later | Non-TTY connect keeps today's behavior |
| Removing the kind `/etc/hosts` aliases or the `IsLoopbackHost` heuristic | Separate issue | The aliases remain the fixture's documented fix |
| Changing shipped Helm defaults or `values-kind.yaml` | — | Only the Keycloak fixture overlay changes |
| Managing kind enrollments in the operator's personal client root | — | Only the fixture-owned root is cleaned up; personal-root use re-logs in explicitly |
| Opening a browser from the in-TUI auth-recovery overlay | — | The overlay keeps its no-browser rule; first use happens only before the TUI starts |

## Definition of done

1. `task lint`, `task test:race`, `task docs`, and `task api:check` pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` still shows a tool call, a permission ask and approval, and a result.
4. `user-docs/mecatui/remote-servers.md` documents endpoints, first-use connect sign-in, and private endpoints. `deploy/mecak8s-kind/README.md` documents the one-command journey. `task site:build` passes.
5. The implementation PR links the Plan / Interface PR and the approved commit, reports interface conformance, and records the AC5.4 manual result.
6. `/panel-review` reports no ship blockers and no unwaived reviewer failures.

## Deferred decisions and known risks

- Without an amending ADR, a reader of ADR 0277 or 0305 alone will see rules this plan
  narrows. The user doc and this plan are the only places the exceptions are recorded.
- The kind fixture keeps its `sudo` `/etc/hosts` step. The one command can still prompt for a
  password the first time.
- If the TTY probe uses only stdin, a piped stdout may still start the browser flow. Checking
  both stdin and stderr is the contract here; it matches the existing confirmation, which
  reads stdin and writes stderr.
- With the fixture-owned root, `mecatui` started through `kind-connect` does not load the
  operator's personal client settings (keymap, theme). That is accepted for a disposable
  fixture.
