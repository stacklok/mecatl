# Mecatui remote endpoints and first-use connect sign-in — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — the directing human classified this as Bounded with no ADR (2026-10-01). It adds connection-profile flags to `connect`, a client-only settings fallback for them, and a narrowly gated sign-in path to `connect`. It reuses the existing ADR 0305 discovery, ADR 0286 issuer transports, and ADR 0277 credential lifecycle without changing server, protobuf, engine, or persistence contracts. See the classification risk under Human decisions.
**Decision record:** None — the directing human chose this plan as the sole record. It narrows two existing rules without amending their ADRs: interactive first-use sign-in is now allowed from `connect`, and discovery may use a private issuer when the operator pins it explicitly, by flags or in an endpoint entry.
**Phase:** Remote mecatui OAuth bootstrap
**Status:** proposed, 2026-10-01. The directing human resolved the three open human decisions on 2026-10-01 by direct amendment ("accept all three recommendations"), and the same day added `mecatui remote` commands for editing endpoint settings (Scenario 6). Converted from an uncommitted ADR draft ("Operator-configured remote endpoints and first-use connect sign-in") at the directing human's request. That draft is intentionally not committed.
**Delivery:** Split. The change adds new `connect` flags, a new client settings schema, an authentication flow, a trust exception, and new kind fixture behavior. These interfaces need review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** None — no tracking issue exists yet.
**Plan PR:** [#2049](https://github.com/stacklok/mecatl/pull/2049)

Goal: one command reaches the OIDC-protected `deploy/mecak8s-kind` Keycloak fixture from a
fresh client: `task mecak8s:kind-connect`. That task passes the connection profile as flags. An
operator who prefers short commands can instead write the same profile once as a named
endpoint and run `mecatui connect mecatl-kind`. The first interactive connect discovers the server's RFC 9728 metadata the way
`mecatui login HOST` does, confirms the discovered tuple, runs the browser login, and then
continues straight into the TUI. Later connects reuse the saved enrollment and never open a
browser.

Today the kind journey takes two commands (`kind-login`, then `kind-connect`). It also needs
six explicit enrollment flags and a repeated `--tls-ca` flag. The fixture cannot use discovery
for three reasons:

- the deployment publishes no protected-resource metadata;
- its issuer resolves to loopback, which public-only discovery rejects;
- the saved enrollment does not remember the gRPC server CA.

The fix gives `connect` a **connection profile**: the gRPC target, the gRPC server CA, and the
discovery trust (public, or private with a pinned CA). The profile comes from flags when any
profile flag is given. Otherwise it falls back to a named endpoint in client settings, and
otherwise to the ADR 0305 public defaults. Flags and settings never mix.

## Human decisions

- [x] Work classification and decision record. — Decision: the directing human chose Bounded with no ADR on 2026-10-01; the source ADR draft is not committed or merged. Risk accepted: [ADR 0277](../adr/0277-remote-mecatui-oidc.md) still says `connect` never opens a browser, and [ADR 0305](../adr/0305-oauth-protected-resource-discovery.md) still excludes private-resource discovery. Neither ADR is amended, so this plan is the only record of the two narrowed exceptions.
- [x] `connect` may run discovered enrollment in-process before the TUI starts, but only when every Scenario 2 gate holds. — Decision: directed by the source draft. Saved targets keep ADR 0277 behavior exactly.
- [x] Private discovery is selected only by explicit operator intent: the `--private-issuer` and `--discovery-ca` flags, or an endpoint entry the operator wrote. A bare address and server metadata can never select it. — Decision: directing human, 2026-10-01, amending the source draft's settings-only rule.
- [x] The connection profile comes from flags or from client settings, all-or-none. — Decision: directing human, 2026-10-01. Interpreted here as: if any profile flag is present, the flags are the complete profile and no endpoint settings are read for that invocation; an endpoint name combined with any profile flag is a usage error; within the flags, `--private-issuer` and `--discovery-ca` are required together. Unset profile fields take the ADR 0305 defaults, not settings values.
- [x] A gRPC server CA set on an endpoint is gRPC trust only, never issuer trust. — Decision: directed by the source draft; it follows ADR 0286 and [ADR 0287](../adr/0287-target-aware-mecatui-tls.md).
- [x] Settings home. `remote:` lives in the strict, client-owned `~/.config/mecatui/settings.yaml`, which no project can write. Rejected: the source draft's operator tier of the shared `~/.config/mecatl/settings.yaml`, which would need new project-tier-ignore logic like `telemetry:`; that shared file configures the server, while the remote endpoint is client state. — Decision: directing human, 2026-10-01, accepted the recommendation.
- [x] How the kind fixture provides its profile. `kind-connect` and `kind-login` pass the profile flags and use the operator's normal client root, so the task writes no settings file; destroy and reset run a best-effort `mecatui logout` for the fixture resource first. Rejected alternatives: a fixture-owned `XDG_CONFIG_HOME` root, which hides the enrollment from a plain `mecatui connect` and drops personal client settings; and editing the operator's settings file from a Taskfile, which risks a lossy YAML merge. — Decision: directing human, 2026-10-01, accepted the recommendation.
- [x] Endpoint name grammar. Names match `^[a-z][a-z0-9-]{0,62}$`; `sessions` and `debug` are reserved; an exact name match wins over host interpretation. A name contains no `.` or `:`, so it cannot be mistaken for a DNS host, `host:port`, or URL. — Decision: directing human, 2026-10-01, accepted the recommendation.
- [x] `mecatui remote` commands for configuring endpoints are in scope. — Decision: directing human, 2026-10-01 ("There should be mecatui cli commands to configure the remote in the settings file"). The command grammar in the interface contract is the agent's proposal; merging the Plan / Interface PR approves it. The kind fixture decision is unchanged: its tasks still pass profile flags and run no `remote` command.

## Interface contract

- **gRPC / protobuf:** None — the change only alters client target resolution and the client sign-in flow. The server's RFC 9728 endpoint and every RPC stay unchanged.
- **Exported Go APIs / interfaces:** None — no `engine/` exported symbol or API snapshot changes. The work lives in `cmd/mecatui` (package `main`) and host-internal `internal/adapter/clientauth`. Any new helper there is unexported, or internal to the host module.
- **Tool schemas:** None — this is not a model-facing surface.
- **CLI / config:** New connection-profile flags on `connect` and on discovered `login`, one new client settings block as their fallback, endpoint-aware positional resolution, and kind fixture overlay changes.
  - **Profile flags.** On `connect`:
    - `--grpc-target HOST:PORT` (new on `connect`; already on `login`) — the gRPC transport target. Default: the resource authority, per ADR 0305.
    - `--tls-ca PATH` (existing) — gRPC server trust only.
    - `--private-issuer` (new on `connect`) — discover through the ADR 0286 private issuer transport.
    - `--discovery-ca PATH` (new) — metadata and issuer trust for private discovery; replaces system roots.

    On discovered `login HOST|URL`, `--grpc-target`, `--private-issuer`, and `--discovery-ca` are accepted, replacing today's rule that rejects `--private-issuer` without an explicit identity. Explicit-identity `login` keeps its current flags; `--discovery-ca` is rejected there because `--tls-ca` already means issuer trust in that mode.
  - **All-or-none.** If any profile flag is present, the flags are the complete profile: no endpoint settings are read for that invocation, and unset fields take the ADR 0305 defaults. An endpoint name (or an implied `remote.default`) combined with any profile flag is a usage error. `--private-issuer` and `--discovery-ca` must be given together. `--tls=false` and `--insecure` remain rejected for authenticated targets.
  - **Settings fallback.** Optional `remote:` block in the client settings file `~/.config/mecatui/settings.yaml`. Each endpoint is the same profile in YAML, decoded strictly like the other keys in that file:
    ```yaml
    remote:
      default: mecatl-kind            # optional; must name an entry in endpoints
      endpoints:
        mecatl-kind:                  # ^[a-z][a-z0-9-]{0,62}$; not sessions or debug
          resource: https://mecak8s-mecak8s.mecatl.svc.cluster.local:18081   # required, canonical HTTPS resource URL
          grpc:
            target: mecak8s-mecak8s.mecatl.svc.cluster.local:18080           # = --grpc-target
            ca: /abs/path/fixture-ca.crt                                     # = --tls-ca; absolute; gRPC trust only
          discovery:
            address_policy: private   # public (default) | private          # = --private-issuer
            ca: /abs/path/fixture-ca.crt                                     # = --discovery-ca; required iff private; absolute
    ```
  - **Validation.** Each of these fails with a path-qualified (settings) or usage (flags) error before any dial or discovery:
    - an unknown key;
    - a relative CA path;
    - private discovery without its CA, or a discovery CA under public discovery;
    - a non-canonical resource or target;
    - a dangling `default`;
    - a name that does not match `^[a-z][a-z0-9-]{0,62}$`, or is the reserved `sessions` or `debug`.
  - **Profile resolution order** for `connect`, `login`, and `logout`. `resolveInvocation` stays pure argv classification; a separate step that receives the parsed flags and the decoded `remote:` block resolves the profile after parsing and before any network I/O:
    1. any profile flag present: the flag profile;
    2. otherwise, an endpoint selected by an exact name match, by `remote.default` when no positional is given, or by the single endpoint whose `resource` equals the positional's canonical resource URL;
    3. otherwise, the ADR 0305 defaults for a bare host or `https://` URL, and today's behavior for `host:port`.
  - **Default endpoint grammar.** With a configured `remote.default`: `mecatui connect`, `mecatui connect sessions`, and `mecatui connect debug TARGET` use the default, as do `mecatui login` and `mecatui logout` with no positional. Without a default, every invocation parses exactly as today, including `connect sessions` treating `sessions` as an address and the existing "ADDRESS required" errors.
  - **Enrollment lookup.** The registry row for a resource-addressable profile is the single row whose `ResourceURL` equals the profile's resource. A row that instead matches only the profile's gRPC target (for example a legacy explicit `kind-login` row with no `ResourceURL`) is never adopted: connect fails closed and names the `mecatui login` command to run. More than one candidate row is rejected as ambiguous, per ADR 0305.
  - **First-use defaults.** First-use sign-in from `connect` uses credential store mode `auto` and the same callback timeout default as `login`; `connect` gains no `--credential-store` or `--timeout` flag.
  - **Kind fixture.** These are fixture overlay changes, not shipped Helm defaults:
    - `deploy/helm/mecak8s/values-kind-keycloak.yaml` adds `oidc.resource: https://mecak8s-mecak8s.mecatl.svc.cluster.local:18081`, `oidc.clientID: mecatui-kind`, and `oidc.scopes: [openid, profile, mecak8s:access, offline_access]`.
    - `kind-connect` runs `mecatui connect https://mecak8s-mecak8s.mecatl.svc.cluster.local:18081 --grpc-target mecak8s-mecak8s.mecatl.svc.cluster.local:18080 --tls-ca CA --private-issuer --discovery-ca CA`, with `CA` the absolute, refreshed fixture CA path.
    - `kind-login` runs discovered `mecatui login` with the same resource and profile flags (without `--tls-ca`).
  - **`mecatui remote` commands.** A new top-level command group that edits the `remote:` block of `~/.config/mecatui/settings.yaml`. It makes no network I/O and never touches the registry or the credential store.
    - `mecatui remote add NAME RESOURCE_URL [--grpc-target HOST:PORT] [--tls-ca PATH] [--private-issuer --discovery-ca PATH] [--default]` adds one endpoint. Profile flags map to fields exactly as in the settings example, with the same pairing rule: `--private-issuer` and `--discovery-ca` together or not at all. A relative CA path is resolved against the working directory and stored absolute, and it must name a readable file containing at least one PEM certificate. `--default` also sets `remote.default`. An existing `NAME` is an error; to change an entry, remove it and add it again.
    - `mecatui remote list` prints each endpoint's name, resource, gRPC target, discovery address policy, and whether it is the default. It prints CA paths but no file contents and no credential state.
    - `mecatui remote remove NAME` deletes the entry and clears `remote.default` if it named that entry. It does not log out; it prints the `mecatui logout` command for the endpoint's resource.
    - `mecatui remote default NAME` sets `remote.default`; `mecatui remote default --clear` removes it.
    - **Writes.** Each mutation edits the YAML document in place, like the `mecated mcp add` mutators in `internal/adapter/permconfig`: comments, key order, and every key outside the edited `remote:` subtree are preserved. Before writing, the command decodes the whole result with the strict client decoder and applies the Scenario 1 validation. It refuses to edit an existing file that already fails that decode. It writes through a temporary file and rename, and it aborts without writing if the file changed since it was read. A missing file is created with mode `0600`, in a directory created with mode `0700`.
- **Events / persistence:** None — no new persisted fields. A first-use enrollment writes the existing registry `Connection` fields:
  - `Identity` (the target comes from the profile);
  - `ResourceURL`;
  - `IssuerAddressPolicy` (`private` for pinned private discovery);
  - `IssuerCAFile` (the profile's discovery CA).

  The credential record key is unchanged. Endpoint names and the gRPC CA are never persisted to the registry; they come from flags or settings on every run. `mecatui remote` writes only the operator's client settings file, which is configuration, not registry or credential state.
- **Security / authority:** Explicit profile flags and operator-written endpoint settings become the trust sources for private discovery and connection CAs; nothing else gains authority.
  - **Trust origin.** Private address admission and custom discovery roots come only from `--private-issuer` with `--discovery-ca`, or from an endpoint entry. A bare address, a URL alone, and server metadata can't select them.
  - **Discovery transports.** Public discovery keeps the ADR 0305 public-bootstrap transport. Private discovery uses the ADR 0286 private issuer transport for both the metadata fetch and issuer discovery: only private addresses are admitted, and `discovery.ca` replaces the system roots. Both stay anonymous, redirect-free, and bounded, with exact resource and issuer binding.
  - **CA separation.** The gRPC CA (`--tls-ca` or `grpc.ca`) never becomes issuer or metadata trust, and the discovery CA (`--discovery-ca` or `discovery.ca`) never becomes gRPC trust.
  - **Browser from connect.** `connect` opens a browser only on interactive first use, after confirmation. It never does so for a saved target, a non-TTY process, a static bearer, `--anonymous`, or a `host:port` target.
  - **Settings writes.** `mecatui remote` writes only `~/.config/mecatui/settings.yaml`, never a project-scoped file or `~/.config/mecatl/settings.yaml`. It runs only at the operator's explicit invocation, so endpoint entries stay operator-written.
  - **Logging.** Credential values never reach logs, prompts, or argv.
- **Compatibility / migration:** Additive client behavior.
  - Without new flags or a `remote:` block, every existing invocation resolves and behaves as before, including `connect HOST:PORT --tls-ca PATH`. One message changes: a credential-free `connect` to a resource-addressable, unenrolled target on a TTY now offers sign-in instead of printing "run mecatui login".
  - Existing enrollments still match by target and resource.
  - `login` and `logout` now read the strict client settings file, so an invalid client settings file (for example a bad `keymap`) fails them with the same path-qualified error the TUI already reports. This is an accepted behavior change.
  - When a saved enrollment no longer matches the resolved profile (resource, gRPC target, address policy, or discovery CA, from flags or from a changed endpoint entry), connect fails closed and names the `mecatui login` command to run.
  - Kind fixture users drop `kind-login` from the normal journey. Existing explicit-identity `login` commands still work.

## In scope — 6 scenarios

### Scenario 1 — Profile flags, with endpoint settings as the fallback

The operator either passes the connection profile as flags, or names a deployment once in
settings and refers to it by name, by default, or by its resource URL from `connect`, `login`,
and `logout`. Resolution happens before any network I/O and follows the existing pure
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
- AC1.4: Flags and settings never mix. Any profile flag combined with an endpoint name or an implied default is a usage error. A profile flag with a URL whose resource matches an endpoint uses only the flags, and no settings field leaks into the profile. `--private-issuer` without `--discovery-ca`, or the reverse, is a usage error. Each explicit identity flag on `login <endpoint>` is rejected before discovery.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_FlagsAllOrNone`
- AC1.6: A URL positional with no profile flags uses the single endpoint whose `resource` matches it. The flag profile and the equivalent endpoint entry produce identical discovery, enrollment, and dial behavior.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_FlagsAndSettingsEquivalent`
- AC1.5: Only the operator-owned `~/.config/mecatui/settings.yaml` is an endpoint source; a `remote:` block in any project-scoped file, or in `~/.config/mecatl/settings.yaml`, selects no endpoint.
  - verify: `TestMecatuiRemoteEndpoints_Scenario1_OperatorOwnedOnly`

### Scenario 2 — First interactive connect signs in, then connects

When an interactive `connect` reaches an unenrolled, resource-addressable target that requires
authentication, it runs the [ADR 0305](../adr/0305-oauth-protected-resource-discovery.md)
discovered enrollment (confirmation, then the ADR 0277 PKCE login). It then dials again with the
new credential in the same process, before the TUI takes the terminal. Because `client.Dial`
is lazy and the TUI's first RPC is the mutating `CreateSession`, connect adds one explicit
pre-TUI probe: a single credential-free, read-only `ListSessions` first-page request. "Requires
authentication" means that probe returned `Unauthenticated`, which `client.AuthFailure`
classifies as `not_enrolled`. The probe dials the profile's gRPC target, which defaults to the resource authority (port 443 by default, per ADR 0305), always with
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

A profile with private discovery (`--private-issuer --discovery-ca`, or an endpoint with
`discovery.address_policy: private` and `discovery.ca`) discovers metadata and the issuer through the private issuer transport from
[ADR 0286](../adr/0286-issuer-transport-split.md), not ADR 0305's public bootstrap. That makes
loopback and internal-CA deployments discoverable without opening that path to arbitrary
addresses.

**Acceptance:**
- AC3.1: A private profile, from flags or from an endpoint, fetches `/.well-known/oauth-protected-resource` and the issuer's OIDC document from a private-address `httptest` TLS server signed by `discovery.ca`. The confirmed enrollment persists `issuer_address_policy: private` and `issuer_ca_file` set to that CA. Saved refresh then uses the same private transport.
  - verify: `TestMecatuiRemoteEndpoints_Scenario3_PrivateEndpointDiscovers`
- AC3.2: Each of these rejects a private or loopback resource or issuer before any persistence: a URL or bare host without `--private-issuer`, a public endpoint, and metadata naming a private issuer. Public discovery behavior is unchanged.
  - verify: `TestADR_0305_PublicBootstrapUnchangedForCommandLineTargets`
- AC3.3: A private profile rejects a resource or issuer that resolves to a public address. It ignores system roots. It sends no bearer, cookie, or client certificate. It follows no redirect.
  - verify: `TestMecatuiRemoteEndpoints_Scenario3_PrivateTransportBounds`
- AC3.4: The gRPC CA is never offered as issuer or metadata trust, and the discovery CA is never used for gRPC dial verification, whichever source supplied them.
  - verify: `TestMecatuiRemoteEndpoints_Scenario3_CASeparation`

### Scenario 4 — Saved profiles reconnect and fail closed on drift

After enrollment, the same profile reconnects without a browser: the same flags again, or
`mecatui connect <name>` (or bare `connect` with a default) with no flags. The gRPC target and
the TLS CA come from the profile, and the credential and
issuer trust come from the registry. This preserves the separate resource and transport
identities from ADR 0305 and the verified-TLS rules from
[ADR 0287](../adr/0287-target-aware-mecatui-tls.md).

**Acceptance:**
- AC4.1: A saved connect dials the profile's gRPC target with verified TLS against its gRPC CA and the saved bearer. With an endpoint this needs no flags; with flags it needs no `--tls`. A new process does the same.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_SavedEndpointReconnect`
- AC4.2: Connect fails closed with a message naming the `mecatui login` command to run, and without opening a browser, when the resolved profile no longer matches the saved enrollment. That covers a changed resource, gRPC target, address policy, or discovery CA, from flags or from settings. A later `login` with that profile re-confirms and replaces the enrollment under the existing ADR 0305 rediscovery rules.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_EndpointDriftFailsClosed`
- AC4.3: `mecatui logout <name>` removes the enrollment for the endpoint's resource. The next interactive `connect <name>` is treated as first use again.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_LogoutReturnsToFirstUse`
- AC4.4: Every connect restart action — connect saved, reauthenticate, and retry after credential cleanup — carries the resolved profile's source: the endpoint name, or the original profile flags. The restarted process therefore re-resolves the same gRPC target and CAs. A picker row whose `ResourceURL` matches a configured endpoint restarts through that endpoint.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_RestartActionsPreserveEndpoint`
- AC4.5: A profile whose gRPC target already has a non-endpoint enrollment (no `ResourceURL`) is not adopted: connect fails closed naming the `mecatui login` command to run, and two candidate rows are rejected as ambiguous. Neither case opens a browser.
  - verify: `TestMecatuiRemoteEndpoints_Scenario4_LegacyRowNotAdopted`

### Scenario 5 — The kind fixture connects in one command

The [mecak8s kind fixture](mecak8s-kind-fixture.md) publishes RFC 9728 metadata and its tasks
pass the connection profile as flags, so `task mecak8s:kind-connect` is the whole journey after
`task mecak8s:kind-keycloak-setup`. The fixture keeps two existing pieces:
- the loopback `/etc/hosts` aliases, including the mecak8s DNS alias that avoids the
  `IsLoopbackHost` workspace footgun;
- per-cluster CA refresh.

Fixture boundaries follow `deploy/mecak8s-kind/README.md`. Its credentials stay isolated per
[AGENTS.md](../../AGENTS.md).

**Acceptance:**
- AC5.1: The Keycloak overlay renders `--oidc-resource=https://mecak8s-mecak8s.mecatl.svc.cluster.local:18081`, `--oidc-client-id=mecatui-kind`, and `--oidc-scopes=openid,profile,mecak8s:access,offline_access`. The base `values-kind.yaml` and the chart defaults gain none of these.
  - verify: `TestMecatuiRemoteEndpoints_Scenario5_KeycloakOverlayPublishesProfile`
- AC5.2: `kind-connect` adds the aliases, refreshes the CA, and runs `mecatui connect` with the fixture resource URL and the complete profile flags: `--grpc-target`, `--tls-ca`, `--private-issuer`, and `--discovery-ca`, with both CA paths absolute and pointing at the refreshed CA. It writes no settings file and passes no issuer, client, audience, or scope flags. `kind-login` uses discovered `login` with the same profile.
  - verify: `TestMecatuiRemoteEndpoints_Scenario5_KindConnectIsOneCommand`
- AC5.3: `kind-destroy` and the setup reset run a best-effort `mecatui logout` for the fixture resource before deleting the cluster, so a recreated cluster with its rotated CA and new Keycloak realm starts at first use rather than at a saved-credential rejection. A logout failure (for example a missing binary) does not block destroy.
  - verify: `TestMecatuiRemoteEndpoints_Scenario5_DestroyLogsOutFirst`
- AC5.4: No saved fixture enrollment, a recreated cluster, and one `task mecak8s:kind-connect` together show the discovered-tuple confirmation, a browser login, and the TUI connected as the Keycloak user. A second `kind-connect` reconnects without a browser.
  - verify: manual — a live kind cluster and a browser login are operator-run and not part of offline gates; record the result in the implementation PR.

### Scenario 6 — `mecatui remote` commands configure endpoints

The operator adds, lists, removes, and selects a default endpoint from the command line
instead of editing YAML by hand. The commands write the same Scenario 1 schema to the same
operator-owned file, so a command-written entry and a hand-written entry are interchangeable.
They join the `topLevelCommands` catalog in
[`cmd/mecatui/command.go`](../../cmd/mecatui/command.go) and edit the file the strict decoder
in [`cmd/mecatui/client_settings.go`](../../cmd/mecatui/client_settings.go) reads. Writes
follow the settings-mutation rules of [ADR 0345](../adr/0345-direct-mcp-onboarding.md): one
exact writable target, preservation outside the edit, stale-write detection, full validation,
and private atomic replacement. Entries stay operator-written per [AGENTS.md](../../AGENTS.md)
(operator grants are explicit).

**Acceptance:**
- AC6.1: `mecatui remote add` writes an entry that Scenario 1 resolves exactly like the equivalent hand-written entry and like the equivalent profile flags. `--default` sets `remote.default`. A relative CA path is stored absolute. A missing settings file is created with mode `0600`.
  - verify: `TestMecatuiRemoteEndpoints_Scenario6_AddMatchesHandWrittenEntry`
- AC6.2: Every `remote` mutation preserves comments, key order, and all content outside the edited `remote:` subtree, including `keymap` and the other client settings keys.
  - verify: `TestMecatuiRemoteEndpoints_Scenario6_EditsPreserveDocument`
- AC6.3: Each of these fails with a usage or path-qualified error and leaves the file byte-identical (or absent):
  - a name that breaks the grammar or is reserved;
  - a duplicate name;
  - an invalid profile flag combination;
  - a non-canonical resource or target;
  - an unreadable or non-PEM CA file;
  - an existing settings file that fails the strict decode;
  - a file that changed between read and write.
  - verify: `TestMecatuiRemoteEndpoints_Scenario6_RejectsWithoutWriting`
- AC6.4: `mecatui remote remove NAME` deletes the entry, clears a default that named it, leaves the registry and credential store unchanged, and prints the `mecatui logout` command for its resource. `mecatui remote default NAME` and `mecatui remote default --clear` set and clear `remote.default`. An unknown name is an error.
  - verify: `TestMecatuiRemoteEndpoints_Scenario6_RemoveAndDefault`
- AC6.5: `mecatui remote list` shows each entry's name, resource, gRPC target, discovery address policy, and default marker. It makes no network I/O and reads no credential. The top-level command index lists `remote`.
  - verify: `TestMecatuiRemoteEndpoints_Scenario6_ListIsOfflineAndSecretFree`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Amending ADR 0277 or ADR 0305 text | Later, if the human wants it | The directing human chose this plan as the sole record |
| Device-code or other non-browser grants for headless first use | Later | Non-TTY connect keeps today's behavior |
| Removing the kind `/etc/hosts` aliases or the `IsLoopbackHost` heuristic | Separate issue | The aliases remain the fixture's documented fix |
| Changing shipped Helm defaults or `values-kind.yaml` | — | Only the Keycloak fixture overlay changes |
| Merging flag values with endpoint settings | — | All-or-none by decision |
| A `remote` command that edits an existing entry in place, or that logs out on remove | Later, if needed | Remove and add again; `logout` stays a separate command |
| Opening a browser from the in-TUI auth-recovery overlay | — | The overlay keeps its no-browser rule; first use happens only before the TUI starts |

## Definition of done

1. `task lint`, `task test:race`, `task docs`, and `task api:check` pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` still shows a tool call, a permission ask and approval, and a result.
4. `user-docs/mecatui/remote-servers.md` documents the profile flags, endpoint settings, the `mecatui remote` commands, first-use connect sign-in, and private discovery. `deploy/mecak8s-kind/README.md` documents the one-command journey and the equivalent `mecatui remote add` entry. `task site:build` passes.
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
- Private discovery can now be selected on the command line. A copied command can pin a
  private CA the operator did not intend; the confirmation prompt shows the discovered tuple,
  and the flags never apply without both `--private-issuer` and `--discovery-ca`.
- `--tls-ca` means gRPC trust on `connect` but issuer trust on explicit-identity `login`. The
  new `--discovery-ca` name keeps discovered profiles unambiguous; the help text must say so.
