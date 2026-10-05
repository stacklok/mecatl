# Qualify native Kubernetes execution

**What this covers:** native execution mock/live fixtures, credential isolation, and sanitized evidence.

**Prerequisites:** [Architecture overview](../architecture.md) and an explicitly owned development environment.

**Follow-on:** [Operator guides](../../user-docs/operating/index.md).

The repository keeps real-provider qualification separate from the default mock
suite. It is explicit opt-in, uses an already-owned retained Kind cluster, and is
not part of `task test`:

```sh
MECATL_EXECUTION_CREDENTIAL_FILE=/absolute/path/to/provider-key \
MECATL_EXECUTION_QUAL_STATE=/absolute/path/to/owned-state \
task e2e:k8s:execution:live
```

The credential file must be a private regular file (no group or other access).
A trusted helper loads it only at runtime and creates a run-scoped Kubernetes
Secret through the API; it never renders the value into a manifest or reads the
Secret back. Only the `mecak8s` harness container receives the OpenRouter key.
The execution provider, controller, OIDC fixture, and executor workloads do not.
The task first runs the deterministic mock qualification, then runs one bounded
real-model coding smoke against `https://openrouter.ai/api/v1`. It verifies
positive token usage, required file and shell tool calls, file contents, and a
successful `go test` independently through the typed gRPC execution service.
Finally it restores the mock deployment and removes only its run-scoped Secret;
the owned cluster and execution workspaces remain for inspection.

### Run the native qualification in GitHub Actions

After independent review of the exact candidate, a maintainer can dispatch the
existing `e2e-live.yml` workflow in `stacklok/mecatl`:

```sh
gh workflow run e2e-live.yml --repo stacklok/mecatl --ref <REVIEWED_BRANCH_OR_TAG> \
  -f native_execution=true -f expected_sha=<FULL_REVIEWED_COMMIT_SHA>
```

`native_execution` defaults to `false`. Setting it to `true` runs only the native
job, avoiding duplicate paid inference in the ordinary live jobs. Schedules and
labelled PR runs retain their ordinary behavior and never select this job.
`expected_sha` is optional, but supply it for reviewed qualification: checkout
uses the dispatch event SHA, verifies exact equality before credential staging,
and records that SHA in the job summary. A moved branch fails the check.

The trust gates are maintainer dispatch, the `stacklok/mecatl` repository check,
and access to the existing `OPENROUTER_API_KEY` repository secret. There is no
GitHub environment-approval gate. Review the workflow and all executed source
before dispatch; the SHA check establishes identity, not code safety. Checkout
retains no Git credentials, the token has read-only contents access, and the
native job uses no build cache.

The same runner first completes the production Kind+Calico qualification without
a provider key or automatic cluster deletion. Only a successful production step
allows credential staging; a production ownership record alone is insufficient.
The workflow stages the key in a new 0700 CI directory and 0600 file, then removes
it from the environment before `live.sh` validates the retained owned state,
reruns mock qualification, and invokes the trusted credential loader. A missing
secret fails qualification rather than skipping successfully. The 100-minute job
has explicit step ceilings: setup 9 minutes, production 50, credential staging 1,
live 25, cleanup 10, and status/artifact reporting 3, leaving 2 minutes of overhead.
Production and live subprocess groups receive TERM after 49 and 24 minutes,
respectively, then KILL after a 15-second grace. These stage ceilings are
intentionally smaller than the sum of all subordinate build, rollout, and test
bounds. Hitting one fails qualification; it does not prove completion. Per-agent
token limits remain unchanged and are not a hard dollar cap.

The live phase preserves the qualified cluster's local-path storage helper and
security configuration; rebuilding the executor image does not replace the
storage provisioner's helper image.

The live script attempts to restore mock configuration and delete its Secret by
recorded UID using an independent cleanup context. A signal or stage deadline can
interrupt that attempt. An always-run workflow step has a separate 10-minute
ceiling to remove the CI-created credential file and delete the owned cluster.
Production publishes its state path and cluster identity after collision checks
and before cluster creation; live and cleanup use those fixed step outputs, not
the mutable local `current` pointer. Cleanup validates the recorded owner, name,
namespace, profile, container label, private kubeconfig, and context against that
identity. Ownership drift fails cleanup without selecting another cluster.
Failure before kubeconfig creation also fails cleanup without deleting a cluster.
Cleanup is bounded best effort: hard cancellation or runner loss can prevent it.
Hosted-runner disposal is the fallback, not evidence that cleanup succeeded.

Artifacts are retained for seven days: a size-bounded `live-summary.json` when
available and a sanitized `qualification-status.txt`. On live failure,
`live-diagnostics.jsonl` captures the real process before mock restoration and
Secret cleanup. If this capture is missing or incomplete, or production fails,
workflow cleanup collects separate `production-diagnostics.jsonl` evidence before
cluster deletion. `diagnostics-status.txt` distinguishes pre-restoration and
fallback collection outcomes. Collection failure warns and still attempts cleanup.

Each collector has a 45-second bound, 5-second API deadlines, and a 1 MiB output
limit. Resource evidence contains known condition/reason classes, deletion and
UID-match booleans, known finalizers, Pod phases, container exit reasons,
lease-expiry status, quota key names, and event reason counts. Log evidence passes
through a strict JSON allowlist in memory; only create stages, fixed reason
classes, elapsed milliseconds, call counts, and hashed session correlation survive.
Raw logs, stacks, PKI, kubeconfigs, Secret receipts, Helm values, resource manifests,
transcripts, and surrounding scratch directories are excluded from artifacts.

The fixture enables `--log-level=debug`; native-execution debug diagnostics use
JSON. Create stages distinguish handler entry after authentication, session-ID
storage probing, remote Ensure, Attach polling, engine construction, persistence,
reference publication, and HTTP response construction. Polling logs its first
state, state changes, and final count rather than every retry. The live test first
checks authenticated HTTP reachability separately, then reports when create
headers were sent and a response started. A last `begin` stage without a matching
completion identifies the boundary to investigate; it does not establish the root
cause. Check live, diagnostics, and cleanup outcomes before treating the recorded
commit as qualified.


## Qualify the mock and network fixtures

The focused qualification task is `task e2e:k8s:execution`. It requires Go,
`ko`, Docker or rootless Podman, Kind, Helm, and `kubectl`. Optional toolbox use
requires explicit `MECATL_EXECUTION_DEV_TOOLBOX` and
`MECATL_EXECUTION_K8S_TOOLBOX` values. The task uses a synthetic OIDC issuer and
the mock model provider, creates a unique state directory and Kind cluster, and
retains both for inspection. CI invokes
`task e2e:k8s:execution:production` and automatically removes only its uniquely
owned cluster. The production target is the home for the enforcing-CNI network
and lifecycle fixture; until that fixture passes, Kind qualification is not
NetworkPolicy isolation evidence.

## Related guidance

- [Native execution acceptance plan](../acceptance/native-kubernetes-execution.md).
- [Operator deployment](../../user-docs/operating/mecak8s.md).
