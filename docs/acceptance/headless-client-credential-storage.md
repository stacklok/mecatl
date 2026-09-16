# Headless mecatui credential storage — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** Remote mecatui credential-storage portability
**Status:** in-progress, 2026-09-09. User approval is recorded via merged PR1281; implementation is landed in PR1291. Scenario 5's manual Kind/Keycloak qualification (macOS explicit-file and Linux headless-auto journeys) passed on 2026-09-09; remaining verification is the baseline-checks list in Definition of done item 4.
**Delivery:** Split. This changes local CLI behavior, durable credential routing, and the confidentiality boundary, so implementation proceeds through a separate implementation PR against this approved contract.
**Expected tasks:** deferred to orchestration

Remote `mecatui` currently always creates a keyring-wrapped encrypted credential store. This plan adds a deliberately weaker owner-only plaintext backend for headless Linux first use. It does not make normal keyring initialization noninteractive: Linux `auto` performs only a read-only Secret Service *detection* before choosing keyring; once the service is present, ordinary keyring initialization may prompt or require an unlocked desktop keyring. macOS keeps the current keyring-default behavior. Explicit `file` is supported on both platforms; automatic headless detection is Linux-only in v1.

One backend is selected per canonical local clientauth store root, normally `$XDG_CONFIG_HOME/mecatl`. The choice is durable non-secret metadata, pinned before interactive OAuth, and binds every later login, connect, refresh, reauthentication, and logout. Backend failure never falls back, switches, or migrates credentials. This work is grant-neutral and composes with, but does not expand, the separate pending Remote device-login sibling plan.

## Human decisions

- [x] Approve root-scoped selection and durable pinning. — Decision: select exactly one backend per canonical clientauth store root; persist that selection before OAuth starts, even when OAuth is later cancelled. Every lifecycle operation uses only the pin; no automatic migration, failure fallback, or later override exists.
- [x] Approve the public selector. — Decision: only `mecatui login` accepts `--credential-store=auto|keyring|file`, defaulting to `auto`. There is no settings key or environment override. A conflicting explicit value after a pin exists is an error; connect, refresh, reauthentication, and logout expose no selector.
- [x] Approve Linux automatic selection and failure handling. — Decision: on Linux fresh first use, `auto` detects an already-running Secret Service without starting or unlocking it, with a 500 ms detection execution budget starting before helper launch and covering address discovery, dial, authentication, Hello, and NameHasOwner. A private read-only helper in the same executable makes synchronous setup terminable without a new dependency. No mutating keyring probe is permitted. Absence selects file; the detection's own timeout selects file only after killing and waiting for the helper. Both emit the approved mild line. Parent cancellation and helper start failure, crash, or unknown result fail closed. A present service proceeds to ordinary keyring initialization; locked, denied, malformed, or initialization failures are actionable errors with no fallback. The 500 ms limit applies only to detection, never to normal keyring operations; OS termination/reap scheduling is not hard realtime, so return may follow the deadline. Pinned enrollments and explicit `keyring` never fall back.
- [x] Approve detector SIGKILL attribution residual (explicit user clarification). — Decision: on fresh Linux `auto`, permit file fallback when the detector's own deadline expires, cancellation successfully calls `Kill`, and `Wait` reaps SIGKILL, even though an independent SIGKILL racing the deadline is indistinguishable. This is an accepted residual, not stronger cause attribution. Parent cancellation/deadline and observable non-timeout failures still fail closed. All other conditions remain unchanged.
- [x] Approve macOS and explicit-file behavior. — Decision: macOS retains the current keyring-default `auto` behavior and performs no automatic headless detection in v1. Explicit `file` selects the file backend without contacting the keyring on Linux or macOS.
- [x] Approve metadata and plaintext locations. — Decision: under `$XDG_CONFIG_HOME/mecatl`, persist schema-versioned backend metadata as `clientauth-credential-backend.json` and store plaintext records below the distinct `clientauth-plaintext/` directory. The root and plaintext directory are `0700`; metadata, records, temporary files, and lock files are `0600`.
- [x] Approve notice frequency and wording. — Decision: the first automatic or explicit file selection prints exactly `Using file-backed credential storage (owner-only permissions).` as a mild neutral line before OAuth. Do not repeat it during connect or refresh. Documentation, not this line, explains plaintext-at-rest and same-account exposure.
- [x] Approve legacy routing evidence. — Decision: a missing marker plus a structurally valid, registry-reachable legacy connection row pins keyring before any secret read, even when its credential is missing. A successful secret read is not required to prove an existing enrollment. Malformed, unknown, quarantined, ambiguous, corrupt, or unreadable registry state fails closed.
- [x] Approve the supported mixed-version posture. — Decision: v1 requires upgrade-only use, with no concurrent older clients sharing the credential-store root. Older clients ignore `clientauth-credential-backend.json`; document this incompatibility rather than promising automatic detection or rejection of arbitrary older clients.
- [x] Approve simpler live qualification evidence. — Decision: remove the standalone fixture checker and its task. The manual Kind journey is limited to login, authenticated connect, natural refresh on a later connect, a new-process authenticated connect, logout, and a failed post-logout authenticated connect. It is weaker evidence: it has no field-by-field semantic persistence oracle and makes no claim that helper checks remain. Hermetic persistence, permission, and conflicting-selector tests remain the evidence for those properties.

## Interface contract

- **gRPC / protobuf:** None — backend selection and persistence remain local to mecatui; server authentication and bearer transport contracts do not change.
- **Exported Go APIs / interfaces:** In `internal/adapter/clientauth`, add the closed vocabularies `type CredentialStoreMode string` (`CredentialStoreAuto = "auto"`, `CredentialStoreKeyring = "keyring"`, `CredentialStoreFile = "file"`) and `type CredentialBackend string` (`CredentialBackendKeyring = "keyring"`, `CredentialBackendFile = "file"`), plus `type CredentialStoreSelection struct { Backend CredentialBackend; NewlyPinned bool }`. Add `func ResolveCredentialStore(ctx context.Context, root string, requested CredentialStoreMode) (CredentialStoreSelection, error)` for login-only selection and pin persistence; `func OpenCredentialStore(ctx context.Context, root string, backend CredentialBackend) (credentialstore.Store, error)` for a selected creating store; and `func OpenExistingCredentialStore(ctx context.Context, root string) (credentialstore.Store, CredentialBackend, error)` for pinned existing-only lifecycle paths. Add `func NewPlainFile(root, namespace string) (*credentialstore.PlainFileStore, error)` and `func OpenExistingPlainFile(root, namespace string) (*credentialstore.PlainFileStore, error)` beside the existing encrypted constructors. The Linux detector is private: `detectLinuxSecretService(ctx context.Context) (secretServiceState, error)`. `credentialstore.Store`, `clientauth.Credentials`, and all `engine/` APIs remain unchanged.
- **Tool schemas:** None — local remote-client credential selection is not an agent tool.
- **CLI / config:** `mecatui login` alone adds `--credential-store=auto|keyring|file`; omitted is `auto`. On an unpinned root, Linux `auto` detects an existing Secret Service; absent or detection timeout selects file, present service uses normal keyring initialization, and every locked/denied/init failure is fail-closed. macOS `auto` directly uses normal keyring initialization. Explicit `file` bypasses keyring access. A pin rejects a conflicting login flag and controls all later commands. No config key or environment variable is added. The Kind qualification uses only existing `mecatui` commands; no fixture checker or qualification task is added.
- **Events / persistence:** Atomically write `clientauth-credential-backend.json` under the canonical root lock before OAuth, with a strict versioned document containing only `version` and `backend`. It contains neither token nor key material. File records are strict plaintext records below `clientauth-plaintext/`, use a fresh random generation, derive their opaque version from the complete record, and retain create-only/replace/delete CAS and ABA resistance. A cancelled OAuth run leaves its backend marker in place. No session or wire event changes.
- **Security / authority:** Linux detection uses the private same-executable read-only helper protocol below; its whole execution, including synchronous godbus setup, is deadline-controlled by the parent and joined before selection. It asks only the bus whether Secret Service already has an owner, never contacts Secret Service or keyring APIs, starts a bus/service, creates a probe account, or unlocks a collection. Normal keyring initialization after a present detection result is outside the detection budget and may interact with the desktop; explicit keyring and pinned routes do not detect or fall back. File storage is a confidentiality-at-rest degradation only: enforce owner, symlink/special-file/hard-link, mode, local-filesystem, lock, sync, and atomic-mutation protections equivalent to the current local store. Root, same-UID processes, memory inspection, rollback, backups, and snapshots remain out of scope. Operators must not `cat` token files, export bearer tokens, or place token material in shell history or evidence.
- **Compatibility / migration:** Missing metadata plus a valid legacy registry row deterministically materializes a keyring pin without reading its credential; a missing credential then fails through the keyring route rather than downgrading. Credential-only crash orphans are not registry evidence and do not pin a root. Malformed metadata, unknown schema/backend, or invalid registry evidence fails closed before opening either store. No keyring-to-file, file-to-keyring, or cross-root migration is provided. All clients sharing the root must be upgraded before file storage is used; concurrent older clients are unsupported, and automatic detection of their use is not promised.

### Private Linux detection mechanism

The proposed private `detectLinuxSecretService(ctx context.Context) (secretServiceState, error)` parent boundary creates `context.WithTimeout(ctx, 500*time.Millisecond)` before `os.Executable` and helper launch. It invokes that executable directly through `exec.CommandContext`, never a shell or PATH lookup, with the sole private argument `--internal-clientauth-detect-secret-service`. The executable dispatches this exact helper invocation before ordinary CLI, TUI, OAuth, store, or keyring initialization. The helper launches no subprocesses. This is an internal protocol, not a supported user-facing command or configuration surface.

The parent sets stdin, stdout, and stderr to null (nil `exec.Cmd` streams); it captures no provider errors, bus addresses, or output. Reserved exit statuses form a closed vocabulary: `80` = present, `81` = absent, `82` = detection failed. Exit `0`, every other exit status, a signal/crash, executable lookup/start failure, and unclassified wait failure are errors, not absence. After a successful start the parent always calls `Wait`; expiry triggers `CommandContext`'s kill and mandatory `Wait` before any timeout fallback. Only the detection's own deadline on a successfully started helper is timeout-eligible. File fallback requires cancellation to successfully call `Kill` and `Wait` to reap SIGKILL; an independent SIGKILL racing that deadline is indistinguishable and explicitly accepted as a residual, not a claim of stronger attribution. Observable other crash/unknown results remain failures even when the deadline elapsed. Parent-context cancellation/deadline takes precedence over detection timeout and fails closed. Selection never proceeds with a live helper. The deadline covers all helper execution; kill/reap scheduling may delay the joined result beyond 500 ms and is not a hard-realtime guarantee.

Inside the helper, use the existing `github.com/godbus/dbus/v5` dependency: `SessionBusPrivateNoAutoStartup`, `Auth(nil)`, `Hello`, then `conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", dbus.FlagNoAutoStart, "org.freedesktop.secrets").Store(&present)`. Close the private connection on every returning path. The parent deadline bounds even synchronous address discovery/dial/Auth/Hello by terminating the helper. Do not use a shared or auto-start session connection. No Secret Service or keyring API, collection access, probe item, unlock, service activation, or descendant process is permitted.

A decoded ownership boolean maps to present/absent. No discoverable session-bus address, or narrowly identified missing/refused bus transport (`ENOENT`/`ECONNREFUSED`), also maps to absent. Recognize the dependency's no-address outcome narrowly, never by broad error-substring matching. Permission denial, authentication failure, malformed addresses/replies, and other errors map to detection failed. Only absent or the parent's own joined detection timeout can choose file, and only on fresh Linux `auto`; present proceeds to normal keyring initialization outside this budget without failure fallback.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — First login chooses and pins one root backend

Selection occurs before OAuth material is acquired, preserving the credential-acquisition boundary in [ADR 0277](../adr/0277-remote-mecatui-oidc.md). `ResolveCredentialStore` owns the root lock, legacy classification, first-use selection, and atomic marker write; `OpenCredentialStore` only opens the resulting backend. This replaces the repeated direct keyring/store construction in [`cmd/mecatui/login.go`](../../cmd/mecatui/login.go) without giving later lifecycle paths a second selection route.

**Acceptance:**
- AC1.1: A fresh Linux `auto` login uses the same-executable no-autostart read-only detector. An absent owner, no session-bus address, or narrowly missing/refused bus selects and persists `file`; a present owner selects and persists `keyring` before OAuth begins. Denied/authentication/malformed/unclassified detection failures do not select file.
  - verify: `TestHeadlessCredentialStorage_Scenario1_LinuxAutoSelection`
- AC1.2: The private helper runs before normal application initialization with null stdin/stdout/stderr and only the reserved exit-status protocol. It neither creates nor reads a keyring item, contacts Secret Service APIs, starts a D-Bus bus/service, unlocks a collection, nor launches a subprocess. Normal keyring initialization may prompt after a present result and is outside the detection budget.
  - verify: `TestHeadlessCredentialStorage_Scenario1_DetectionIsReadOnly`
- AC1.3: Explicit `file` never contacts the keyring; explicit `keyring`, a locked/denied/malformed/init-failed keyring, and every pinned backend failure return an actionable error without fallback.
  - verify: `TestHeadlessCredentialStorage_Scenario1_NoFallback`
- AC1.4: A root-lock-serialized selection is written before OAuth; simultaneous first logins observe the same backend, and cancellation after selection retains that pin.
  - verify: `TestHeadlessCredentialStorage_Scenario1_PersistBeforeOAuth`
- AC1.5: The 500 ms detection deadline starts before launch and covers address discovery, dial, Auth, Hello, and NameHasOwner. Hermetic stall fixtures at each stage prove timeout triggers kill and mandatory Wait before fresh-auto file fallback; no helper survives selection. Tests assert lifecycle ordering rather than a hard-realtime reap deadline.
  - verify: `TestHeadlessCredentialStorage_Scenario1_WholeDetectionBudget`
- AC1.6: Parent cancellation/deadline, helper executable/start failure, crash, exit zero, unknown status, or unclassified wait error fails closed without file selection. A crash/unknown result racing expiry remains a failure except for the explicitly accepted indistinguishable SIGKILL race: the detector's own deadline, a successful cancellation `Kill`, and a joined SIGKILL permit fresh-Linux-auto file fallback. Parent cancellation/deadline still takes precedence; no stronger cause attribution is claimed. No captured helper output, address, or provider error reaches parent diagnostics.
  - verify: `TestHeadlessCredentialStorage_Scenario1_HelperFailuresFailClosed`

### Scenario 2 — Pinned lifecycle routing has no override or migration

The current login, connect, reauthentication, refresh, and logout paths separately construct keyring stores in [`cmd/mecatui/login.go`](../../cmd/mecatui/login.go), [`cmd/mecatui/main.go`](../../cmd/mecatui/main.go), and [`cmd/mecatui/logout.go`](../../cmd/mecatui/logout.go). They must use the one resolver/opener boundary so a backend is never inferred differently by command, while retaining [ADR 0275](../adr/0275-bounded-scoped-https-keepalive-oidc.md)'s distinction between unavailable storage and repairable credential corruption.

**Acceptance:**
- AC2.1: Login accepts the exact selector and rejects a conflicting selection after a marker exists; connect, refresh, reauthentication, and logout accept no backend selector and open only the pinned backend.
  - verify: `TestHeadlessCredentialStorage_Scenario2_PinnedLifecycle`
- AC2.2: A Secret Service appearing after file selection does not shadow or move file credentials. A later unavailable keyring after keyring selection does not inspect or create plaintext state.
  - verify: `TestHeadlessCredentialStorage_Scenario2_NoImplicitMigration`
- AC2.3: File and keyring routes preserve canonical identity keys, conditional refresh-token rotation, corrupt-record handling, cross-process CAS, and logout-race behavior through the existing [`credentialstore.Store`](../../internal/adapter/credentialstore/store.go) contract.
  - verify: `TestHeadlessCredentialStorage_Scenario2_IdentityAndRotation`

### Scenario 3 — Plaintext storage is locally safe and truthfully disclosed

The new file store reuses the local-file atomicity and owner-control invariants of [ADR 0218](../adr/0218-credential-store.md), but does not claim encryption. Its physical directory is separate from the encrypted namespace, preventing residual data from one backend being opened by the other.

**Acceptance:**
- AC3.1: `clientauth-credential-backend.json` and `clientauth-plaintext/` are created only beneath the canonical owner-controlled clientauth root with exact documented permissions; unsafe paths, links, special files, hard links, wrong owners/modes, and unsupported platforms fail before mutation.
  - verify: `TestHeadlessCredentialStorage_Scenario3_PrivateFilesystemBoundary`
- AC3.2: Plaintext records pass shared mutable-store conformance and subprocess race tests for create-only put, version-matched replace/delete, stable locks, full-record versions, private temporary files, fsync, atomic rename/remove, and directory sync.
  - verify: `TestHeadlessCredentialStorage_Scenario3_PlainFileConformance`
- AC3.3: The first file selection writes exactly `Using file-backed credential storage (owner-only permissions).` as a mild neutral line before OAuth, with no repeated connect/refresh warning; public documentation states plaintext-at-rest and same-account exposure without logging tokens, record names, or identity hashes.
  - verify: `TestHeadlessCredentialStorage_Scenario3_TruthfulNotice`

### Scenario 4 — Legacy registry evidence remains keyring-bound

Legacy installations lack a marker because [ADR 0277](../adr/0277-remote-mecatui-oidc.md) required a keyring-wrapped store. A structurally valid registry row is sufficient legacy enrollment evidence; checking whether its secret still reads would wrongly turn a lost credential into a backend downgrade. Invalid registry state is never silently treated as a fresh root.

**Acceptance:**
- AC4.1: A missing marker with a valid registry-reachable legacy row atomically pins keyring before any secret read, even when the credential is missing or the keyring is unavailable; that later failure never selects file.
  - verify: `TestHeadlessCredentialStorage_Scenario4_LegacyKeyringPin`
- AC4.2: Credential-only crash orphans, an empty encrypted namespace, or a legacy keyring account without valid registry evidence do not pin a root. Existing actual-record-only legacy key copying remains unchanged.
  - verify: `TestEmptyRootLogoutWithLegacyGlobalKeyCreatesNoRootAccount`, `TestExistingEmptyLegacyNamespaceDoesNotMigrateGlobalKey`, and `TestRootScopedKeyringMigratesLegacyAndPreservesCredentials`
- AC4.3: Corrupt/unreadable metadata, unknown backend/schema, malformed/unknown/quarantined registry rows, and ambiguous evidence fail closed before a probe, backend open, or metadata rewrite.
  - verify: `TestHeadlessCredentialStorage_Scenario4_FailClosedLegacyEvidence`

### Scenario 5 — Kind qualification proves the complete stored-credential journey

The implementation PR cannot claim full end-to-end coverage from hermetic tests alone. Before that claim, an operator must qualify the built `mecatui` against the existing `deploy/mecak8s-kind` Keycloak/TLS fixture with the default mock LLM. This is deliberately outside `task test`: it needs Kind, host aliases, a browser-capable Authorization Code + PKCE interaction, and about 15 minutes for natural access-token expiry. The overlay does not configure protected-resource metadata (`oidc.resource`, client, and scopes), so this journey uses the explicit issuer/client/audience login below and must not be described as discovered enrollment.

**Fixture preparation and boundary:**

1. Run `task build` separately. `task mecak8s:kind-keycloak-setup` is **destructive**: `kind-setup` deletes and recreates the named `mecatl-dev` cluster and its `.scratch/kind/mecatl-dev` state. Use it only for a disposable/new fixture. When that named cluster is already healthy, preserve it and run the idempotent `task mecak8s:kind-keycloak-apply` instead. Then run `task mecak8s:kind-hosts-add` and `task mecak8s:kind-keycloak-demo`. Do not export `OPENROUTER_API_KEY`; qualification uses the mock provider, two mecak8s replicas, ephemeral local Redis, and one ephemeral Keycloak `start-dev` pod.
2. Keep the current realm fixture unchanged: `accessTokenLifespan` is 900 seconds and the login requests `offline_access`. That scope is what makes an offline refresh token available; it does not turn an ordinary access token into a refresh token. The client refresh-ahead is 30 seconds, so wait for natural demand at about 870 seconds rather than editing token files, process clocks, or the public fixture lifetime.
3. Put every client artifact in a fresh repo-local root, not the operator's normal configuration: `install -d -m 0700 "$PWD/.scratch/kind/mecatl-dev/client-xdg"`, then run every command with `XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg"`. Do not copy provider settings into the normal root; any qualification-only settings and evidence stay below `.scratch/kind/mecatl-dev/`.

**macOS explicit-file journey:**

1. Login with the exact explicit identity path (the fixture has no protected-resource metadata):
   ```sh
   XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg" ./bin/mecatui login mecak8s-mecak8s.mecatl.svc.cluster.local:18080 --credential-store=file --issuer https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl --client-id mecatui-kind --audience mecak8s --tls-ca .scratch/kind/mecatl-dev/fixture-ca.crt --private-issuer --scopes openid,profile,mecak8s:access,offline_access
   ```
   Observe the approved file-storage notice once, before OAuth, and `login successful`.
2. Run an authenticated connect:
   ```sh
   XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg" ./bin/mecatui connect mecak8s-mecak8s.mecatl.svc.cluster.local:18080 sessions --tls --tls-ca .scratch/kind/mecatl-dev/fixture-ca.crt
   ```
3. After the original access token naturally enters its 30-second refresh window (about 870 seconds after issue), run the same authenticated connect again. Start a new `mecatui` process and run it once more; both connections must authenticate without another login or file-storage notice.
4. Run `XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg" ./bin/mecatui logout mecak8s-mecak8s.mecatl.svc.cluster.local:18080`, then run the authenticated connect again. It must fail with login required and must not silently start OAuth. Logout again before fixture cleanup if any later login was performed.

This manual journey is intentionally weaker evidence: it has no field-by-field semantic persistence oracle and makes no claim that helper checks remain. Hermetic tests retain the persistence, private-permission, and conflicting-selector coverage; do not inspect token files, export bearer tokens, or place token material in shell history or evidence.

**Linux headless-auto journey:**

Repeat the same login → connect → natural-refresh connect → new-process connect → logout → failed-connect journey against a **different fresh** repo-local `XDG_CONFIG_HOME` on an actual headless Linux host where no session bus/Secret Service is running. Do not simulate headlessness merely by unsetting `DBUS_SESSION_BUS_ADDRESS`. Create `.scratch/kind/mecatl-dev/client-xdg-linux` mode `0700`, omit `--credential-store` so the real default `auto` detector selects file, and run:

```sh
XDG_CONFIG_HOME="$PWD/.scratch/kind/mecatl-dev/client-xdg-linux" ./bin/mecatui login mecak8s-mecak8s.mecatl.svc.cluster.local:18080 --no-browser --issuer https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl --client-id mecatui-kind --audience mecak8s --tls-ca .scratch/kind/mecatl-dev/fixture-ca.crt --private-issuer --scopes openid,profile,mecak8s:access,offline_access
```

This remains Authorization Code + PKCE, not a device grant: `--no-browser` only prints the authorization URL and still listens at `http://127.0.0.1:18473/oauth/callback`. Before starting, arrange browser access to the Keycloak hostname and callback access to that listener (for SSH, forward browser-side local ports 8443 and 18473 to the same ports on the headless host, and resolve `keycloak.mecatl.svc.cluster.local` to the browser machine's loopback). If those prerequisites cannot be met, the Linux live qualification is blocked rather than silently replaced with a device flow or password grant.

Preserve the shared cluster after qualification by default: logout first, then run `task mecak8s:kind-hosts-remove`; run `task mecak8s:kind-destroy` only when the operator explicitly intends to remove the disposable cluster. No qualification task automatically tears down or mutates an unrelated keyring.

**Acceptance:**
- AC5.1: The fixture documentation pins the destructive-setup warning, healthy-cluster reuse path, explicit non-discovered enrollment parameters, fixed 900-second lifetime, mock-provider topology, isolated roots, exact login/connect/logout commands, headless callback prerequisite, logout-before-cleanup order, and no automatic shared-cluster teardown.
  - verify: documentation review and hermetic fixture tests
- AC5.2: Before the implementation PR claims full E2E, operator evidence records successful macOS explicit-file and genuinely headless Linux default-auto journeys through login, authenticated connect, natural refresh on a later connect, a new-process authenticated connect, logout, and failed post-logout authentication. This is weaker manual evidence: it has no field-by-field semantic persistence oracle and makes no claim that helper checks remain.
  - verify: operator-run because Kind, PKCE, platform selection, and natural expiry are intentionally non-hermetic
  - **evidence: passed 2026-09-09.** Both the macOS explicit-file journey and the Linux headless-auto journey completed against the `deploy/mecak8s-kind` Keycloak/TLS fixture: login, authenticated connect, natural refresh on a later connect, a new-process authenticated connect, logout, and a failed post-logout authenticated connect all behaved as specified.
- AC5.3: Hermetic tests retain unsafe-permission pre-OAuth failure, explicit keyring unavailability, persistence, and conflicting-selector coverage without touching a live keyring.
  - verify: `TestHeadlessCredentialStorage_Scenario2_BackendLifecycleMatrix`, `TestHeadlessCredentialStorage_Scenario3_PrivateFilesystemBoundary`, and `TestHeadlessCredentialStorage_Scenario3_PlainFileConformance`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| OAuth grant selection, RFC 8628 polling, provider fixtures, or device presentation | Separate pending Remote device-login sibling plan | Backend routing is grant-neutral. |
| User-requested migration between keyring and file | Future explicit migration plan | Automatic migration and failure-triggered switching are forbidden. |
| Automatic headless detection outside Linux | Future credential-backend work | v1 keeps macOS's existing keyring-default behavior. |
| Password-derived encryption, environment keys, cloud secret managers, TPM/HSM, or a privileged broker | Future credential-backend work | v1 supplies only keyring and owner-only plaintext file. |
| Protection from root, same-account processes, memory inspection, rollback, backups, or snapshots | None — documented protection limit | File permissions are not encryption. |
| Cross-host/network-filesystem atomicity | None — existing local guarantee | Cooperating-process flock/rename guarantees remain local only. |
| Acceptance of the storage policy ADR | [ADR 0318](../adr/0318-headless-mecatui-credential-backend-selection.md) | The accepted ADR records the settled policy; implementation proceeds against the approved contract. |

## Definition of done

1. This approved Split plan is human-approved through Plan / Interface review, and [ADR 0318](../adr/0318-headless-mecatui-credential-backend-selection.md) is accepted before implementation ships.
2. Hermetic unit and subprocess tests prove the read-only D-Bus detector, absent/timeout routing, pinned no-fallback lifecycle, pre-OAuth persistence, plaintext conformance, legacy routing, unconditional refresh-token rotation handling, logout races, the unsafe-permission pre-OAuth failure, and conflicting-selector rejection without a live keyring or network. Normal `task test` remains hermetic and never waits for token expiry or starts Kind.
3. Before the implementation PR claims full E2E, Scenario 5's operator-run evidence is complete for both macOS explicit-file and genuinely headless Linux default-auto roots against the Kind/Keycloak fixture: login, authenticated connect, natural refresh on a later connect, a new-process authenticated connect, logout, and failed post-logout authentication. This weaker manual evidence has no field-by-field semantic persistence oracle and makes no claim that helper checks remain; hermetic tests cover persistence, permissions, and selector conflicts.
4. `bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan.sh docs/acceptance/headless-client-credential-storage.md`, `bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan-test.sh`, `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build` pass; `go run ./cmd/mecademo` remains green.
5. `docs/architecture.md`, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/tui.md`, `user-docs/`, `deploy/mecak8s-kind/README.md`, and `user-docs/mecatui/remote-servers.md` state the selection policy, persistence, macOS/Linux distinction, plaintext limits, non-repeating notice, exact operator qualification, and fixture boundaries truthfully.

## Deferred decisions and known risks

- **500 ms scope:** the same-executable private read-only helper resolves the existing godbus dependency's synchronous setup limitation without a dependency change. The parent starts the deadline before launch, kills on expiry, and waits before fallback; all address discovery/dial/Auth/Hello/NameHasOwner execution is covered. OS kill/reap scheduling is not hard realtime, so this is an execution budget, not a promise to return within exactly 500 ms. Ordinary keyring initialization remains outside the budget and may prompt; failure never triggers fallback.
- **Mixed versions:** all clients sharing a credential root must be upgraded before file storage is used. Concurrent older clients are unsupported; they ignore the marker, and automatic detection is not promised.
- **Plaintext:** owner-only filesystem controls preserve local mutation safety but not confidentiality from the same account. The line is intentionally mild; detailed risk disclosure belongs in documentation.
