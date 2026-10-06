# Validate the scratch remote broker-session experiment

Use the parent checkout on branch `broker-simple-take-2` at
`/Users/jakub/devel/mecatl/.worktrees/broker-simple-take-2` for all commands below.
The nested `.scratch/broker-session-poc` checkout is an unchanged backup, not a
working location. Run build and test tasks from the parent root.

Use this checkout to connect `mecated` or `mecak8s` to the canonical
`mecatl.broker.v1.SessionService` with an explicit OWNERLESS deployment partition.
This local experiment is approved only for Kind readiness testing. It is not
merged or ship-ready; Workstream 2 is excluded.

## Prerequisites

Provide an existing valid `mecabroker` JSON configuration with anonymous, OAuth,
or mixed profiles, durable Redis, workload JWT verification and a TLS listener.
Keep its deployment identifier, workload subject, Redis namespace and (when OAuth
is configured) key ring stable across replacement. The host needs a CA bundle,
the broker's TLS DNS name, and a projected workload JWT file accepted by that broker.

OAuth and mixed profiles borrow the existing native encrypted Redis client;
credentials remain entirely in the donor Process. Anonymous-only configurations
reuse `protected_storage.redis` for metadata, omit `protected_storage.encryption`,
and need no credential key ring or OAuth callback. This explicitly named legacy
configuration section creates no credential store for anonymous profiles: the
production Lifecycle owns one metadata client and closes it on startup rollback
and shutdown. For example, retain your existing TLS Redis settings in:

```json
"protected_storage": {
  "redis": {
    "address": "redis.example.com:6379",
    "password_file": "<REDIS_PASSWORD_FILE>",
    "ca_file": "<REDIS_CA_FILE>"
  }
}
```

All required backends must complete discovery before any catalogue is published.
An anonymous discovery failure in a mixed enrollment exposes no partial tools.
Explicit `profiles: []` is a valid idle broker configuration: durable metadata Redis
is still required, and opening or reopening a session returns an empty
catalogue without upstream discovery. The session reference survives restart;
restoration may rotate its catalogue reference under the existing recovery contract.
Enrollment is unavailable
(`FailedPrecondition`) because native enrollment requires at least one service;
it does not fabricate a completed enrollment or browser flow.

Saved remote broker sessions require remote broker composition when loaded or run.
Switching the host to global/direct MCP does not reinterpret their tool names,
even without a pending invocation; restore broker composition to use those sessions.

## Start the broker and host

1. Add this member to your broker JSON configuration:

   ```json
   "session_api": {
     "mode": "OWNERLESS",
     "deployment": "broker-session-poc-a"
   }
   ```

2. Build from the parent checkout root and start the broker in a separate terminal:

   ```sh
   task build:broker
   task build:session-poc-hosts
   bin/mecabroker --config .scratch/broker-ownerless.json
   ```

   Supply your valid configuration at `.scratch/broker-ownerless.json`.
   The required `session_api` object selects the OWNERLESS workload/deployment
   partition. The authenticated TLS listener registers only SessionService.
   Missing configuration fails startup; there is no legacy or local fallback.

3. Start a local host with durable JSONL storage. Replace the three file paths
   and broker endpoint with your mounted configuration:

   ```sh
   bin/mecated serve --mock \
     --store-dir .scratch/session-host-store \
     --user-model-dir .scratch/session-host-user-model --no-user-model \
     --mcp-broker-address broker.example.com:8443 \
     --mcp-broker-tls-ca '<BROKER_CA_FILE>' \
     --mcp-broker-server-name broker.example.com \
     --mcp-broker-token-file '<PROJECTED_WORKLOAD_JWT_FILE>'
   ```

   `--mock` permits offline host control testing but does not call a chosen
   upstream tool. To exercise one deterministically, use `--mock-script` with
   your enrolled tool's actual name and valid arguments:

   ```json
   {"turns":[
     {"tool_calls":[{"id":"poc-call-1","name":"<ENROLLED_TOOL_NAME>","args":{}}]},
     {"text":"done"}
   ]}
   ```

   Keep your existing permission configuration. Tool permission checks and
   pre-tool hooks still run before dispatch. A permission ask requires approval;
   broker enrollment does not authorize the tool action. Bind this example host
   to loopback; non-loopback broker-session hosts require authenticated controls.

   For `mecak8s`, append the same four broker flags to your valid agent invocation.
   Its Redis session store and session lease remain host-owned. Broker mode needs
   the complete tuple even with zero tools. Global mode rejects the tuple.
   With no MCP selection, profiles or tuple, the host leaves MCP disabled.

   Host broker settings contain only `mcp: {mode: broker}`. Move upstream route,
   OAuth and callback configuration to the broker's `profiles` and `callback_url`.
   Host-owned broker profiles fail with a value-free remedy. Direct/global
   configuration remains unchanged, including preregistered `secret_env`.
   The retired selector and transport registry JSON keys are rejected.

## Enroll and invoke through the host

Create a file-less session and copy its returned `session_id`:

```sh
curl --fail-with-body -H 'Content-Type: application/json' \
  --data '{"profile":"no-fs"}' http://127.0.0.1:8081/v1/sessions
```

Set `SESSION_ID` to that returned value, then connect without a request body:

```sh
curl --fail-with-body -X POST \
  "http://127.0.0.1:8081/v1/sessions/$SESSION_ID/workspace-enrollment/connect"
```

For a pending protected enrollment, open the returned browser URL and repeat
Connect to observe completion. The host saves the exact catalogue authority
before registering tools. A failed save returns an error and exposes no candidate
engine. The existing per-call presentation/recheck controls save catalogue adoption
and the dispatch fence before Resume, which accepts only retained original bytes.
Enrolled protected tools in this opt-in facade now have a private native
credential-readiness wrapper. Before dispatch it uses ToolHive's token
read/refresh lifecycle, never an upstream MCP tool call. An expired grant that
cannot be refreshed parks the exact original call and opens the **existing native
browser OAuth flow**. Recheck rediscovers the complete catalogue; the host saves
adoption and reruns ordinary tool permissions before Resume executes retained
bytes once. A failed save, denied permission, cancellation, or changed account
prevents dispatch.

Account continuity uses the native, validated provider/user/subject/client binding
for every configured provider, not a random token-session ID. Missing identity
information or a changed binding refuses continuation; explicitly disconnect and
enroll again to select another account for a new action. No identity proof is
fabricated and no credential is sent to the host.

Remote revocation of a still-valid access token is not detectable by this safe
preflight. A rejection/timeout after Ready stays conservative: it is never
converted into a replayable non-dispatch claim or blindly retried. Protected
wrappers require an authorization-presenting interactive host run; headless runs
retain the engine's fail-closed authorization policy.

Send a prompt to run the mock script (or your configured real model):

```sh
curl --fail-with-body -N -H 'Content-Type: application/json' \
  --data '{"text":"Run the enrolled tool"}' \
  "http://127.0.0.1:8081/v1/sessions/$SESSION_ID/prompt"
```

Restart the host with the same store to reopen the saved exact broker reference.
Restart the broker with its unchanged deployment/workload/storage configuration
to test protected replacement. Catalogue authority must be adopted durably again
before the host exposes replacement tools.

Withdraw an idle session's authority or delete the session:

```sh
curl --fail-with-body -X POST \
  "http://127.0.0.1:8081/v1/sessions/$SESSION_ID/workspace-enrollment/disconnect"
curl --fail-with-body -X DELETE \
  "http://127.0.0.1:8081/v1/sessions/$SESSION_ID"
```

Disconnect persists local withdrawal before remote cleanup, so cleanup failure
cannot republish old tools. It also cancels pending enrollment and invalidates its
old callback/observation; reconnect must create a fresh flow. Host pending intent
is cleared only together with saved withdrawal. Restarting or reopening the host
alone does not reenroll. To reconnect the **same** host session, explicitly POST
Connect again:

```sh
curl --fail-with-body -X POST \
  "http://127.0.0.1:8081/v1/sessions/$SESSION_ID/workspace-enrollment/connect"
```

Connect finishes cleanup against the saved old catalogue revision, verifies the
empty exact session snapshot, and saves that replacement revision as enrollment
intent before Begin. Cleanup/save failure blocks Begin; a changed remote
catalogue fails closed rather than implicitly adopting new authority. OAuth
reconnection requires a fresh browser enrollment. Reconnection never clears
attempt IDs or an unresolved invocation fence. Delete remains terminal.

## Verify offline

```sh
task test:broker-session-poc
git diff --check
```

The host proofs cover native anonymous enrollment and execution, save failures,
exact-reference restore, protected enrollment/replacement, durable withdrawal,
explicit anonymous/protected reconnection, cleanup/revision fences and delete.
`TestSessionBrokerProductionTLSNativeProfiles` reuses offline donor fixtures for
real signed workload JWT authentication, TLS gRPC/client, native Process, browser
OAuth callback, discovery and native invocation. It covers anonymous, protected,
mixed and mixed discovery failure, process replacement with fresh catalogue
fences, Redis readiness and exactly-once client closure. All upstream requests
stay on local fixtures; no live service is contacted.

`protected/native-expiry-refresh-success` expires an enrolled native access token
and proves donor `GetValidTokens` refreshes it for the same account: preflight stays
Ready without authorization/browser reenrollment or upstream MCP I/O, preserves
the catalogue revision, and the subsequent Invoke executes once with the refreshed
credential. No alternate Runtime is assembled.

Production per-call Resume is covered by
`protected/native-expiry-resume`: a signed workload goes through the actual TLS
listener and native Process, enrolls, expires its native access grant with refresh
revoked, parks without upstream MCP I/O, completes native browser OAuth, saves
host catalogue adoption, and dispatches once under the original call ID. The
upstream fixture checks durable adoption/fencing before executing. A broker
receipt accepts the exact original argument bytes and rejects alternate whitespace
for that ID, proving Resume used the retained original bytes rather than rebuilt
arguments. The same production path covers adoption-save failure, permission deny
on Resume, changed-account refusal, and cancellation with a stale browser callback.

`protected/remote-revocation-after-ready-conservative` covers the unsupported
remote-only revocation case: safe native readiness still reports Ready, but the
rejected dispatch produces neither a successful tool execution nor an invented
authorization/replay claim. `protected/lost-reply` verifies an uncertain execution
retains the durable fence against a second call with a new ID. Client controls
verify Invoke and Resume do not retry Unavailable/timeouts. Donor enrollment and
static-wrapper controls retain their original contracts; only SessionAPI catalogues
receive the new private readiness wrapper.

## Validate an existing local Kind fixture

These live checks are operator-run and were not deployed during this local
implementation. Use an existing disposable Kind cluster, an explicit kubeconfig
and context, and locally loaded host/broker images. Install `task`, Go, Helm,
`kubectl`, `jq`, `kind`, `ko` and your container runtime. Allow enough disk for the
three binaries and local images; do not remove operator data to make space.

1. Build and run the offline selection/query/status/authority proofs:

   ```sh
   df -h .
   task build:broker build:session-poc-hosts
   task test:broker-session-poc
   ```

2. Prepare `.scratch/broker-kind-values.yaml` for your existing namespace. Set
   `broker.sessionAPI.mode: OWNERLESS` and a stable `broker.sessionAPI.deployment`.
   Keep `mcp.servers` and `mcp.broker.callbackURL` as broker-owned chart inputs;
   the host ConfigMap contains selection only. Supply already provisioned TLS/CA
   Secrets, projected workload authentication, native TLS Redis credentials/CA
   and a reachable test MCP upstream. OAuth also needs the native KEK ring,
   preregistered/CIMD/DCR configuration and a browser-reachable HTTPS callback.
   Configure host OIDC and TLS for owner-scoped connector inspection. Anonymous
   metadata omits KEKs. Native sweeping uses `runtime.sweep_interval` (omission
   or zero selects the existing native default); retired transport keys have no
   compatibility fallback.

3. Render without contacting the cluster:

   ```sh
   helm template mecak8s deploy/helm/mecak8s --namespace mecatl \
     -f deploy/helm/mecak8s/values-kind.yaml \
     -f .scratch/broker-kind-values.yaml > .scratch/broker-kind-rendered.yaml
   ```

   Confirm SessionService-only broker config, separated mounts, singleton Recreate,
   complete host TLS/workload tuple, and retained Redis PVC behavior. A zero-MCP
   render contains no broker resources. After separate human deployment approval,
   install those same values with an explicit cluster context:

   ```sh
   helm upgrade --install mecak8s deploy/helm/mecak8s --namespace mecatl \
     --kubeconfig deploy/mecak8s-kind/kconfig.yaml --kube-context kind-mecatl-dev \
     -f deploy/helm/mecak8s/values-kind.yaml -f .scratch/broker-kind-values.yaml \
     --wait --timeout 4m
   task mecak8s:broker-session-check
   ```

   The check is executable and passive. It checks canonical config and workload
   readiness with explicit kubeconfig/context flags. Its offline fixture uses a
   fake `kubectl`; passing that test does not prove live Kubernetes readiness.

4. Use the host enrollment/invocation steps above with a test-owned session and
   authenticated host access. Keep caller authentication and host CA in a private
   curl config, never terminal arguments or verbose output. To add passive live
   connector inspection to the check:

   ```sh
   HOST_URL='<HOST_HTTPS_URL>' SESSION_ID='<TEST_SESSION_ID>' \
     CURL_CONFIG='<PRIVATE_CURL_CONFIG>' task mecak8s:broker-session-check
   ```

   Verify query projection contains only selected fields; denied target authority
   executes nothing; status is passive and truthful. Complete native OAuth and
   verify exact-original-call Resume once. Restart host/broker with unchanged
   deployment/workload/storage and confirm saved authority/fences, then test
   disconnect/reenrollment without widening carried restrictions. Leave existing
   sessions, credentials, Redis and PVCs intact. These invocation, callback and
   replacement checks remain live, unrun acceptance work.

## PoC contract choices

`CheckAuthorization` belongs to the canonical SessionService contract.
`ResumeToolWrapper` is an adapter client helper. OWNERLESS identifies the verified
workload/deployment partition, not a human owner. Empty broker profiles are
permitted with explicit selection and durable Redis; they publish no tools.

Exact-catalogue Disconnect remains a deliberate PoC limitation: stale cleanup
fails closed rather than adopting newer authority implicitly. This PoC has no
private replay-proof protocol: NotDispatched never grants transport replay
permission; timeouts/401s after admission cannot establish non-dispatch. The
fixed 30-day metadata lifetime is PoC-only, not a new credential lifetime or
production retention policy. Single-replica coordination, the global facade
mutex (one `SessionAPI.mu` across all sessions, held during native readiness I/O),
process-local flows/receipts, and existing persistence ordering are not redesigned
here. Reauthorization rotates native token-session/custody references; superseded
native rows remain subject to native retention rather than a new cleanup system.

## Next steps

See [the handoff](../../.scratch/SESSION_API_POC.md) for the test commands,
composition entrypoints and remaining delivery work.

## Troubleshooting

An unresolved saved invocation blocks subsequent broker dispatch even if the
model generates a new call ID. Do not retry an ambiguous operation: reconcile
its upstream outcome externally and create a new session when safe. The PoC has
no operator command to clear this fence and admits at most 64 attempted broker
calls per host session, including across disconnect/reconnect. This bounded
no-eviction attempt set and the broker's bounded receipts are deliberate no-replay
safety limits; neither is reset merely to admit more calls.

Only one active broker facade may own the namespace. HA, distributed takeover,
human-owner assertions and migration of existing donor sessions
are unsupported. Stable broker metadata has a fixed 30-day lifetime; native
credential custody keeps its existing fixed expiry. Before admitting new work,
the facade reaps expired/missing cached sessions and closes their native resources.
The 16 parked-flow cap counts active flows, not cancelled/expired tombstones;
tombstones are bounded and evicted old references fail closed. Protected recovery
retains its exact native handle and candidate catalogue across metadata save or
Commit cancellation, so retry does not create another native credential/attachment.
