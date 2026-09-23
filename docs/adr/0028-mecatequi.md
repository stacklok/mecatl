# ADR 0028 — mecatequi: Single-Shot GitHub Action Runner

- Status: Accepted
- Date: 2026
- Scope: mecatequi binary contract, split-privilege job graph, trust boundary, and reusable workflow distribution
- Superseded by: [ADR 0327](./0327-self-repository-action-refs.md) hardcoded-ref pinning decision only

## Context

mecatequi is the headless single-shot mecatl runner: one prompt, one in-process engine, one terminal state, and three artifacts (a git diff patch, a summary JSON, and an optional event log). Running it inside GitHub Actions introduced a trust problem: issue text is attacker-controllable, and a prompt-injection attack must not gain write access to the repository. The cloud-native arc makes the mecatl process disposable via externalised state; mecatequi is the inverse — it makes mecatl a stateless one-shot where the forge is the store.

## Decision

A three-job split-privilege graph enforces a hard token boundary: acknowledge (issues:write only, no LLM key) posts an early signal; implement (contents:read, no write token) runs the agent; publish (write token, no agent code) applies the patch as data and posts the summary. The agent job can never push code, open a PR, or comment, because it holds no token that can. A reusable workflow_call workflow is the recommended adoption path, collapsing consumer boilerplate to roughly fifteen lines while preserving per-job permissions. The binary's contract — patch, summary JSON, exit code, flags — is frozen and additive-only.

## Consequences

Shipped at v0.0.3. The conversational v2 (multi-turn bot) is deferred pending cloud-native rehydration seam readiness. Current behaviour and the operator walkthrough live in docs/architecture.md and docs/usage.md. Status lives in [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md). The token boundary invariant must be preserved across both adoption paths; the reusable-pins CI gate enforces hardcoded sibling-action refs match the release tag on every cut.

---

`mecatequi` (`cmd/mecatequi/main.go`) is the headless, single-shot mecatl runner: one
prompt, one in-process engine, one terminal state, three artifacts (a working-tree git
diff, a machine-readable summary JSON, an optional durable event log), and an exit code.
This doc covers the **forge glue** that runs it inside GitHub Actions — composite actions, a
reusable `workflow_call` workflow (the recommended adoption path, §5.1), and a split-privilege
workflow template (the escape hatch) — and the trust model that keeps an agent driven by
untrusted issue text from doing damage.

The Go binary's contract is Pipeline 1 and is frozen. Everything here lives in `.github/`
and `docs/` and changes no Go.

> **Conscious additive-flag exception.** The frozen-contract rule is *additive-only*, not
> *immutable*: a NEW flag whose default is empty and whose absence is byte-identical to the
> prior behaviour can be added without breaking the contract or bumping a schema. `--instructions`
> (§4) is the first such case — the additive-flag analogue of the Summary JSON's additive-only
> rule. It is a cmd-local prompt-assembly knob, not a new `app.Config` field, and an empty
> value produces the exact pre-flag prompt string.

## 1. Framing: the inverse of cloud-native

The cloud-native arc (`docs/adr/0027-cloud-native.md`) makes the mecatl **process**
disposable: state (sessions, the event log, approvals) is externalised to a durable store
so a crashed or restarted process re-attaches and continues. The Action is the *inverse*
move. It makes mecatl a **stateless one-shot** whose state is the GitHub issue and the pull
request it opens — **the forge is the store**. There is nothing to re-attach to: the run
reads its task from the issue, produces a patch + summary, and exits. The issue thread and
the PR are the durable record; the process keeps none.

That inversion is why the v1 Action does not wire `Config.EventLog` or a session store: a
single-shot run has no second consumer to read its state back. The durable JSONL event log
mecatequi emits is an *artifact* (uploaded for forensics), not a rehydration source.

## 2. The split-privilege model

Both the reusable workflow (`.github/workflows/mecatequi-reusable.yml`) and the escape-hatch
template (`.github/workflows/mecatequi-example.yml`) are three jobs with a hard token
boundary:

- **`acknowledge`** runs **first**, gated on the same trigger as `implement`, holding
  `permissions: issues: write` only — **no** LLM key, **no** agent code, **no** contents
  write. It posts one early comment to the issue (*"🤖 mecatequi is working on this — see
  the run: …"* with the run URL). This guarantees a **durable issue-side signal for every
  triggered run**, even if everything downstream fails silently — the failure mode field
  data surfaced, where a skipped or failed `publish` left the issue author with no trace at
  all. Its only capability (commenting) is the minimum needed for that trace.
- **`implement`** (`needs: acknowledge`) runs the agent. It holds `permissions: contents:
  read`, **no** id-token, **no** write scope. Its only secret is the LLM key. If a
  prompt-injection in untrusted issue text hijacks the agent, the blast radius is the
  **rotatable LLM key** — the agent cannot push code, open a PR, or comment, because the
  job has no token that can.
- **`publish`** (`needs: [acknowledge, implement]`) holds the write token (`contents:
  write`, `pull-requests: write`, `issues: write`) but **runs no agent code**. It downloads
  the `implement` job's artifacts (**non-fatally** — a missing artifact must not abort the
  job before `publish.sh` runs) and applies the patch as **data** (`git apply`), then posts
  the summary as text. Its `if:` (`!cancelled() && needs.acknowledge.result == 'success'`)
  fires on implement **success and failure**, so a failed run still lands an honest
  terminal comment instead of silence; `publish.sh` reads a missing/empty `EXIT_CLASS` as
  setup-failure, and a failed `git push` / `gh pr create` comments *before* exiting
  non-zero (never a silent abort after deciding to open a PR).

> **The token boundary invariant:** the step that can write to GitHub never runs agent
> code; the step that runs agent code never holds a write token. Only `acknowledge` and
> `publish` hold a write scope (and `acknowledge` holds only `issues: write`); `implement`
> never gains issues/pull-requests/contents write.

This invariant holds across **both** adoption paths — the composite-vendored template
(`.github/workflows/mecatequi-example.yml`, the escape hatch) **and** the reusable
`workflow_call` workflow (`.github/workflows/mecatequi-reusable.yml`, the recommended path,
§5). A reusable workflow keeps per-job `permissions:`, so it preserves the boundary a single
composite action cannot: the `implement` job holds only the LLM key(s) at `contents: read`,
and the `publish` job holds the write token with no agent code. The **secret-scoping proof**:
the publish-token secrets are interpolated only in the `publish` job, so GitHub never
materialises them in `implement` — a called workflow's `GITHUB_TOKEN` can only be downgraded
from the caller's grant, never escalated, and each job declares its own scope.

A stronger variant, noted in the template, replaces the broad job `GITHUB_TOKEN` in
`publish` with a just-in-time GitHub App installation token scoped to exactly this repo's
contents + pull-requests + issues — removing the standing write grant entirely. v1 ships
the `GITHUB_TOKEN` form for zero-config copyability and documents the App-token upgrade; the
reusable workflow's publish-token interface (§5) wires the JIT App-token form directly.

## 3. Trust boundary

| Source | Trust | Handling |
|---|---|---|
| Issue/comment text | **Untrusted** (attacker-controllable) | Extracted by jq from `$GITHUB_EVENT_PATH` into a file (`extract-prompt.sh`), passed via `--prompt-file`, fenced by the binary's `--untrusted-prompt`. |
| Operator workflow config (posture, flags, model) | **Trusted** | Set by a maintainer in the workflow; passed as action inputs. |

**The author gate is trigger-based, NOT `author_association`-based.** GitHub's webhook
`author_association` is **unreliable for membership** — it reports an org MEMBER as
`CONTRIBUTOR` — so a gate that asserts `author_association ∈ {OWNER, MEMBER, COLLABORATOR}`
silently **skips legitimate runs**. The two deployments differ:

- **Private repo** (the live `.github/workflows/mecatequi.yml`): the **trigger is the
  gate**. Applying a label needs triage/write access and commenting is team-only, so
  GitHub's own permission model decides who can start a run; no `author_association` check
  is wired, and `author-gate.sh` is absent.
- **Public repo**: do **not** trust `author_association`. Add a dedicated permission-check
  gate **job** that calls the `collaborators/{user}/permission` API and gates
  `implement`/`publish` on its result.

`author-gate.sh` (which asserts `author_association`) ships in the **example template
only**, as a defense-in-depth illustration behind a possibly-edited `if:`; it is
deliberately not part of the live workflow's gate.

**Public-vs-internal posture.** On a public repo, treat the issue text as hostile: keep
`untrusted: "true"` and prefer `posture: "auto"` (allow-all with child injection-defence
ON) over `yolo`. On an internal repo the same defaults are still correct — the author gate
is the trust decision, not the posture.

## 4. The binary's stable contract

The Action codes against these frozen surfaces in `cmd/mecatequi`:

- **The patch** — `cmd/mecatequi/run.go` (`gitDiffPatch`). A `git diff HEAD` shows only
  tracked edits + deletions and would silently **lose the agent's new files**; mecatequi
  therefore appends a `git diff --no-index -- /dev/null <file>` new-file hunk for every
  untracked, non-ignored file (`git ls-files --others --exclude-standard`) so the patch
  `publish.sh` applies with `git apply` reproduces **modified, added, and deleted** files.
  `non_empty_diff` and `diff_bytes` therefore agree: a single new file makes the patch
  non-empty. The git env is scrubbed (`gitenv.Scrub`) and every git call carries
  `--no-ext-diff`, so no inherited `GIT_*` or repo-named external diff driver can run.
- **Summary JSON** — `cmd/mecatequi/run.go` (`Summary`). Additive-only; the action reads
  `.stop_reason`, `.non_empty_diff`, `.diff_bytes`, and `.usage.total_tokens` from it. The
  summary is written to a **file** (`--out-summary <path>`), never `-`, so it never
  collides with logs and stays clean for `jq`.
- **Exit code** — `cmd/mecatequi/run.go` (`exitCode`) plus the setup-failure paths in
  `cmd/mecatequi/main.go`: `0` = clean terminal (incl. the honest non-completions
  `no_progress`/`budget`/`max_*`), `1` = run failure (model error, cancelled, no-approver
  cancel-on-ask, timeout), `2` = setup failure. The action maps these to the
  `exit-class` output (`clean`/`run-failure`/`setup-failure`) and **never fails its own
  step on a non-zero code** — the caller branches on `exit-class`.
- **Flags** — `cmd/mecatequi/flags.go` (`flags`). The action's inputs map onto the flags
  one-for-one; secrets are read from the environment, never a flag or an action input. One
  flag is **not** an `app.Config` knob: `--instructions` is a cmd-local prompt-assembly
  channel (`cmd/mecatequi/prompt.go` (`buildPrompt`)) that emits TRUSTED operator framing
  **outside** the untrusted-prompt fence — the symmetric counterpart to the harness's own
  untrusted-data warning. The action bakes in a default that asks the agent to write its
  final message as a PR description and to self-verify (build/lint/test) before finishing;
  an empty value omits the channel and the prompt is byte-identical to the pre-flag string.
  `appConfig` (`cmd/mecatequi/flags.go`) applies the shared `internal/cliconfig`
  `ProviderFlags` — the SAME credential/base-URL helper `mecated` and `mecatui` use — so
  mecatequi reads all three provider keys (`OPENAI_API_KEY` / `OPENROUTER_API_KEY` /
  `ANTHROPIC_API_KEY`) and registers all three `--*-base-url` flags. A present
  `OPENAI_API_KEY` flips the OpenAI provider on automatically (the same flip `mecated`
  does); for OpenRouter or Anthropic set the respective key plus `--default-provider`. This
  is why the live `.github/workflows/mecatequi.yml` delivers `OPENROUTER_API_KEY` at the
  job's `env:` and never needs an OpenAI-specific input.

### Action input → flag map

| Action input | Flag | Default |
|---|---|---|
| `prompt-file` (required) | `--prompt-file` | — |
| `untrusted` | `--untrusted-prompt` (when `true`) | `true` |
| `instructions` | `--instructions` (omitted when empty) | baked-in PR-description + self-verify framing |
| `setup-script` | composite pre-run step (not a binary flag) — runs before the binary | `""` (no hook) |
| `workspace` | `--workspace` | `${{ github.workspace }}` |
| `posture` | `--posture` | `auto` |
| `timeout` | `--timeout` | `40m` |
| `max-run-tokens` | `--max-run-tokens` (omitted when empty) | `""` |
| `max-turns` | `--max-turns` (omitted when empty) | `""` |
| `model` | `--model` | `""` |
| `default-provider` | `--default-provider` | `""` |
| `default-model` | `--default-model` | `""` |

Prefer `model` for newer/passthrough models; `default-model` is catalog-validated and
rejects ids not in the embedded snapshot. `model` is the per-session passthrough path.
| `openai` | `--openai` (when `true`) | `""` |
| `openai-base-url` | `--openai-base-url` | `""` |
| `guardrails-model` | `--guardrails-model` | `""` |
| `out-diff` | `--out-diff` | `$RUNNER_TEMP/mecatequi.patch` |
| `out-summary` | `--out-summary` | `$RUNNER_TEMP/mecatequi.summary.json` |
| `out-events` | `--out-events` | `$RUNNER_TEMP/mecatequi.events.jsonl` |

Outputs (kebab-case, GitHub Actions house style): `patch-path`, `summary-path`,
`events-path`, `summary-json` (compacted; best-effort and `$GITHUB_OUTPUT`-size-bounded —
read `summary-path` for anything large), `stop-reason`, `non-empty-diff`, `exit-class`.

The `mecatequi` composite action bakes in a default `--instructions` value (TRUSTED framing,
emitted **outside** the prompt fence) so every consumer gets it with zero config: write the
final message as a PR description, and self-verify (run the repo's build/lint/test and make
them green) before finishing. For that self-verification to actually run, the agent needs the
repo's build/lint/test tools on PATH — so the composite action itself now **always provisions
`task` (go-task) + golangci-lint** (v2.12.2, via `go install`) onto PATH in the same job,
before it runs the binary (Go is already provisioned for the binary build). That makes the
**80% Go case work out of the box** on **both** adoption paths — the reusable workflow and a
direct action reference — with no per-consumer toolchain steps. A project toolchain we cannot
guess (buf, protoc plugins, node, system packages) is installed via the **`setup-script`
input**: operator-supplied shell the action runs in the same job, before the binary. Override
the `instructions` input to replace the baked-in framing wholesale; an empty value omits it.

> **The `setup-script` trust posture.** `setup-script` is the **operator's own trusted code**
> — a maintainer sets it in the workflow — and is a different trust zone from the prompt body.
> The prompt (`prompt-file`) is attacker-controllable issue text wrapped in the untrusted-data
> fence; `setup-script` is a maintainer-authored build step. It runs in the **implement** job,
> which holds `contents: read` and **no GitHub write token**, so even a buggy or over-broad
> setup-script cannot push code or open a PR — the token boundary still bounds the blast radius.
> The reusable workflow threads it through as a `workflow_call` input wired into the implement
> job; the live `.github/workflows/mecatequi.yml` keeps its explicit `task` + golangci-lint
> steps (they are now redundant with the composite default, harmlessly so).

### Configurable PR-description formatting

`publish.sh` builds the PR title + body. A repo can override the **body** layout (and,
optionally, the **title**) with a template file of `{{placeholder}}` tokens, so different
repos get their own PR-description style. This is `publish.sh` + `action.yml` glue — no Go.

**Resolution order (in `publish.sh`):**

1. The `pr-body-template` input / `MQ_PR_BODY_TEMPLATE` env — a path **relative to the
   checkout** — if set and the file exists.
2. Else `.github/mecatequi/pr-body.md` in the checkout (the zero-config convention) if it
   exists.
3. Else the **built-in rich body** (caveat + "What the agent did" + "Files changed" + "Run"
   table + run link + `Closes #<n>`). **An absent template preserves today's behaviour
   byte-for-byte** — a default-path render is byte-equivalent to the prior body.

A missing **explicit** template path — or one that resolves **outside the checkout** (a
`../` traversal or an escaping symlink; the path is confined to `${MQ_WORKSPACE}` as
defense-in-depth, CWE-22) — logs a `::warning::` and falls back to the built-in body (it
never aborts the publish, and never reads an out-of-checkout file into the PR). The optional
`pr-title-template` / `MQ_PR_TITLE_TEMPLATE` controls the title the same way (same
confinement). Its **default** is now `<issue title> (#<n>)` — the triggering issue's title,
fetched READ-only via `gh issue view` in the **privileged publish job** (the agent job holds
no GitHub token, so the token boundary is preserved). The fetched title reaches the title and
the `{{issue_title}}` body placeholder only as a literal `jq --arg` value (the same
literal-delivery the attacker-controllable `{{what_agent_did}}` uses — never argv, env, eval,
envsubst, or sed), and the default title expands it only as a shell var into the
`gh pr create --title "…"` argv token, never eval'd, so shell metacharacters in an untrusted
title are inert. When the title cannot be fetched (transient API failure / unreachable issue)
the default falls back to the prior literal `mecatequi: changes for issue #<n>`. A rendered
title is flattened to one line — so use the **short** placeholders in a title
(`{{issue_ref}}`, `{{issue_title}}`, `{{stop_reason}}`, `{{branch}}`); prose placeholders like
`{{what_agent_did}}` or `{{summary_table}}` flatten to one unwieldy line.

**Placeholders (the documented allowlist):**

| Token | Value |
|---|---|
| `{{what_agent_did}}` | the model's `final_text` (its own account of the change) |
| `{{files_changed}}` | the name-status bullet list of changed files |
| `{{summary_table}}` | the run-metadata markdown table |
| `{{run_url}}` | link to the workflow run |
| `{{issue}}` | the issue number, bare (e.g. `123`) |
| `{{issue_ref}}` | the issue reference (e.g. `#123`) |
| `{{issue_title}}` | the triggering issue's title (fetched READ-only via `gh issue view`) |
| `{{stop_reason}}` | the terminal stop reason |
| `{{non_empty_diff}}` | whether the run left a diff (`true`/`false`) |
| `{{diff_bytes}}` | diff size in bytes |
| `{{total_tokens}}` | cumulative tokens for the run |
| `{{branch}}` | the head branch the PR is opened from |
| `{{base}}` | the base branch the PR targets |

An **unknown** `{{token}}` is left **intact** (the operator may want literal braces) — never
stripped, never an error. When there are no changed files, `{{files_changed}}` renders a
`_(no files changed)_` sentinel (parity with `{{summary_table}}`'s "no run summary"
fallback), so a template author's `## Files changed` header is never left dangling over an
empty value.

**Why a careful substitution mechanism (the security point).** `{{what_agent_did}}` is the
model's `final_text` — **agent-authored from untrusted issue text**. The substitution
(`render_template` in `publish.sh`, a small `python3` pass) is therefore:

- **Literal** — values are read from a JSON file (built with `jq --arg`, never argv, never
  the process env) and inserted via a `re.sub` *callable*, so a value containing `& \ /`,
  backticks, `$(...)`, or `{{...}}` is inserted **verbatim**, never interpreted. No naive
  `sed s///` (breaks on `& / \`), no `eval`, no `envsubst` against the env (would expand any
  `$VAR` an attacker put in `final_text`).
- **Single-pass** — one scan of the template; each `{{token}}` is resolved once against a
  fixed dict, and a value that itself contains `{{run_url}}` is **not** re-expanded.
- **Display-only** — the result is written to a `gh ... --body-file` and posted as PR text;
  it is never executed.

**The trust caveat is non-negotiable.** `publish.sh` **always force-prepends** the
`⚠️ Agent-authored from the issue text — review carefully before merging.` line above
whatever the template renders, so a custom template can never drop the safety warning. It is
not a placeholder — **do NOT include your own ⚠️ caveat line in the template, or you get a
duplicate.**

**Issue linkage.** The built-in default keeps `Closes #<n>`. A custom template controls its
own linkage (use `{{issue_ref}}` with `Closes`/`Refs`); `publish.sh` does **not** force-append
`Closes` when a template is used.

**Scope.** Templating applies to the **PR-create body + the re-run body refresh only**. The
failure / no-change / de-dup comment paths are unchanged.

**Example template + activation.** `.github/mecatequi/pr-body.md.example` ships a copyable
template (it is `.example` so this repo keeps the built-in default until someone opts in).
**Rename it to `.github/mecatequi/pr-body.md`** to activate it (resolution step 2 — no
workflow change), or point `MQ_PR_BODY_TEMPLATE` at any path. The live workflow keeps the
built-in default.

## 5. Distribution decision

**The matlatl pattern: build from the action's own checkout — single path, no auth.** The
composite action (`.github/actions/mecatequi/action.yml`) builds the binary from the
action's **own** source tree (`go build -C "$GITHUB_ACTION_PATH/../../.." ./cmd/mecatequi`,
with the toolchain provisioned from `${{ github.action_path }}/../../../go.mod`,
`GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`). This is exactly how `stacklok/matlatl`'s
composite works, and it is **uniform** across both ways the action is referenced:

- **Self (this repo).** `uses: ./.github/actions/mecatequi` → `$GITHUB_ACTION_PATH` is the
  local workspace's action directory, so it builds the checked-out tree (HEAD). mecatl's own
  live workflow uses this — it dogfoods HEAD with no tag dependency.
- **Cross-repo.** `uses: stacklok/mecatl/.github/actions/mecatequi@<tag>` → GitHub checks
  the **whole mecatl repo out at that tag** into `$GITHUB_ACTION_PATH`, and the build uses
  that tagged source. **No mecatl checkout** is required in the consuming repo.

Because the action source itself is the build input, there is **no token, no `GOPRIVATE`,
and no PAT** — the same zero-credential posture as matlatl. The action lives at
`.github/actions/mecatequi/`, so the repo root (where `go.mod` + `cmd/mecatequi` live) is
`$GITHUB_ACTION_PATH/../../..`. Acquisition stays two isolated composite steps ("Set up Go" +
"Acquire mecatequi binary") so the signed-asset swap below remains a contained change.

**Cross-repo reference.** A consumer references the subdir action and pins a tag (or a SHA):

```yaml
- uses: stacklok/mecatl/.github/actions/mecatequi@v0.0.3   # pin the latest released tag
  with:
    prompt-file: ${{ runner.temp }}/prompt.txt
    posture: auto
```

`owner/repo/path@ref` is the GitHub syntax for an action that lives in a repository
subdirectory; the `@<ref>` IS the version (there is no version input — matlatl has none
either). The **only** requirement is the org setting that lets GitHub Actions use actions
from the org's internal/private repositories (Settings → Actions → General → Access for
`stacklok/mecatl`, or `gh api -X PUT
repos/stacklok/mecatl/actions/permissions/access -f access_level=organization`). mecatl's own
workflow keeps `uses: ./.github/actions/mecatequi` (local build, always HEAD).

**Versioning: ALPHA, `v0.0.x`.** The first published tag is `v0.0.1`. Tags are cut by a
release process separate from this design (the orchestrator), not by editing the action.

**Residual / honest cost.** Each run builds the binary from source (no cached release
artifact). A future release job can publish a checksummed, cosign-signed `mecatequi` binary
or a container (mirroring the `mecated` image's keyless-signing flow in
`.github/workflows/release.yml`); swapping the action to consume it is then a one-step change
— drop the two acquisition steps and add a download + `cosign verify` + checksum-check, since
the run step already reads `$RUNNER_TEMP/mecatequi` regardless of how it got there. That
signed-binary / container path is the later speed optimization; this pipeline deliberately
does **not** add it yet.

### 5.1 Reusable workflow (`workflow_call`) — the recommended adoption path

Referencing the composite action by tag (above) still leaves a consumer to **vendor the
whole split-privilege job graph** — the three jobs plus the glue scripts. That is ~800 lines
in a real adoption, ~540 of them byte-for-byte copies of `publish.sh` + `extract-prompt.sh`
that will **drift** from mecatl. The reusable workflow
(`.github/workflows/mecatequi-reusable.yml`, `on: workflow_call`) collapses that to a
**~15-line caller**:

```yaml
jobs:
  mecatequi:
    uses: stacklok/mecatl/.github/workflows/mecatequi-reusable.yml@v0.0.3
    secrets:
      openrouter-key: ${{ secrets.OPENROUTER_CI_TOKEN }}
      publish-app-id: ${{ secrets.RELEASE_APP_ID }}
      publish-app-private-key: ${{ secrets.RELEASE_APP_PRIVATE_KEY }}
    with:
      label: ready-for-agent
      model: anthropic/claude-sonnet-4.6
      default-provider: openrouter
```

It is the same three jobs (`acknowledge` / `implement` / `publish`) with the same per-job
permissions, so the token boundary is preserved exactly (§2). The hand-rolled template
`.github/workflows/mecatequi-example.yml` remains the **escape hatch** for a consumer that
needs to customise the job graph (a custom author-gate job, an extra approval stage, a
different trigger).

**Why the sibling actions use full-path refs (the wrinkle that drives the design).** Inside
a workflow called as `uses: owner/repo/.github/workflows/X.yml@ref`, a `uses:
./.github/actions/Y` resolves to the **caller's** checkout, not mecatl's — and a `run:`
script likewise executes against the caller's checkout. So a naive reusable workflow cannot
reach mecatl's `publish.sh`/`extract-prompt.sh` at all. The fix is two-fold: the glue scripts
are **wrapped as composite actions** (`.github/actions/mecatequi-extract-prompt/` and
`.github/actions/mecatequi-publish/`, each script's single home — they were *moved*, not
copied, so there is no second copy to drift), and the reusable workflow references all three
sibling actions by **full path** `stacklok/mecatl/.github/actions/<name>@<tag>`. GitHub
**auto-fetches** a `uses:` action from mecatl at that ref, so there is still no consumer
vendoring and no mecatl checkout in the consumer.

**Hardcoded-ref pinning + the release-process cost.** Expressions are **illegal** in `uses:`,
so the sibling-action ref must be a **hardcoded literal tag** (`@v0.0.3`), not an expression.
These are **first-party same-repo** actions, so they pin to the **version tag**, not a SHA:
the supply-chain SHA-pin rule defends against a *third-party* action whose tag a compromised
maintainer could re-point, but these live in this repo and are released together — a SHA would
be impossible to write before the release commit exists (a bootstrap chicken-and-egg) and no
stronger than the tag (we control both). The cost is real: **every release tag must bump these
pins in the same tagged commit**, or the tag ships pins pointing at the previous version
(version skew — a consumer on `@vNEW` would silently run the `vOLD` actions). The
`lint:reusable-pins` Taskfile target (`.github/actions/check-reusable-pins.sh`) makes that
mechanical — it asserts every `stacklok/mecatl/.github/actions/*@<tag>` pin equals the current
release tag, and it runs in CI. (Third-party actions — `checkout`, `upload`/`download-artifact`,
`create-github-app-token` — stay SHA-pinned with a `# vX.Y.Z` comment per the house set.)

**The empty-expression load break + the regression guard (issue #70).** `v0.0.2` shipped with
an empty `${{ }}` placeholder buried in the `mecatequi-extract-prompt` action's `description:`
prose. GitHub evaluates expression placeholders in the **parsed** scalars of an `action.yml`
(name, description, input defaults, output values) — but **not** inside YAML `#` comments — and
an *empty* `${{ }}` is a syntax error there ("An expression was expected"). The action therefore
failed to **load**, and because a `uses:` action that won't parse aborts the step, **every
consumer run died before the agent even started**. The fix is a fail-closed regression guard:
`check-action-templates.sh` (`task lint:action-templates`, run in CI under `lint:actions`) scans
every parsed YAML scalar in the workflows and composite actions and **rejects a live empty
`${{ }}`** placeholder. It exists because `actionlint` does **not** load composite action
manifests at all, so it never caught the original break. (Relatedly, `publish.sh` now guards each
early `gh issue comment` — a failed acknowledgement post emits a `::error::` annotation instead
of aborting the publish job before its terminal exit.)

**The publish-token interface (the one real interface decision).** App-token minting is
consumer-specific (the consumer's own GitHub App id/key), so it cannot be hidden behind a
default. The reusable workflow accepts **both forms** via `secrets:`, and the consumer wires
whichever they have:

```yaml
on:
  workflow_call:
    secrets:
      # Provider keys (all optional — wire the one you use; an undefined secret is the empty
      # string, which the binary treats as absent).
      openrouter-key:           { required: false }
      openai-key:               { required: false }
      anthropic-key:            { required: false }
      # Publish token — BOTH forms, all optional:
      publish-token:            { required: false }   # form 2: a pre-minted token
      publish-app-id:           { required: false }   # form 1: JIT App-token (stronger)
      publish-app-private-key:  { required: false }
```

The `publish` job resolves `GH_TOKEN` in precedence order: a **minted GitHub App installation
token** (form 1 — the stronger JIT posture, minted in-job when `publish-app-private-key` is
present) → a **pre-minted `publish-token`** (form 2) → the **standing `GITHUB_TOKEN`** grant
(form 3, zero-config but a broad standing grant). Form 3 emits a `::warning::` nudging the
operator toward form 1 or 2. The resolved token is written to `$GITHUB_ENV` (not a step-level
`env:` on the `uses:` step) because a `$GITHUB_ENV` write reaches a composite action's internal
step whereas step-level env on a `uses:` step does not — the same delivery rule the LLM key
follows.

**Form 1 setup (the GitHub App).** Form 1 has no zero-config path because the App is the
consumer's own: create a GitHub App, grant it **contents: read & write**, **pull-requests:
read & write**, and **issues: read & write** on the target repo, generate a private key,
install the App on the repo, then store the App ID in the `publish-app-id` secret and the
private-key `.pem` in `publish-app-private-key`. The operator walkthrough (the click path) is
in `docs/usage.md` ("Setting up publish-token Form 1").

**Passthrough inputs vs. escape-hatch-only.** The reusable workflow exposes the per-session
passthrough knobs (`model`, `default-provider`, `posture`, `max-run-tokens`, `max-turns`,
`timeout`, `openai-base-url`, `guardrails-model`, `setup-script`, the `base-branch` + PR-template
knobs). It deliberately
does **not** expose `default-model` (catalog-validated — `model` is the passthrough), `openai`
(provider is auto-detected from the present key), or `subagent-ask-reviewer` (an autonomous-
approval capability — a deployment decision better made by editing the action than a workflow
input). A consumer that needs those vendors the escape-hatch template. Note also that
`default-provider` only *selects* a provider; the matching provider key must be wired or the
run fails *"no LLM provider available"* (documented in `docs/usage.md`).

The `@v0.0.3` in the examples above is **illustrative** — a consumer pins the latest released
tag (an example tag that does not yet exist resolves to GitHub's generic *"workflow not
found"*, the same surface as the missing org-Actions-access setting).

**Three named provider secrets.** The reusable workflow declares `openrouter-key`,
`openai-key`, and `anthropic-key` (all `required: false`); the `implement` job sets all three
at job-level `env:` (`OPENROUTER_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`). An
undefined secret materialises as the **empty string**, which the binary treats as **absent**:
the provider registry registers a provider only when its key resolves non-empty
(`internal/app/registry.go` (`providerKey`)), so an empty key is a no-op. A caller therefore
wires only the provider(s) it uses; the binary auto-detects from the present key, and
`default-provider` forces it.

**Trigger label/mention are inputs, not repo variables.** A reusable workflow cannot reliably
read the **caller** repo's `vars.*`, so the trigger label (default `mecatequi`) and comment
mention (default `@mecatequi`) are `workflow_call` inputs the caller passes.

## 6. The `/proc`-exfiltration gap — FIXED (the secret-scrubbed agent shell)

This **was** a real environment-exfiltration gap (security review "Finding B"). It is now
**closed at the engine layer**: every agent-facing Bash shell runs with the harness's
credentials scrubbed out of its environment.

**The gap (what it was).** The main-session command runner inherited `os.Environ()`
unscrubbed. Under posture `auto` (or looser) the model could run a Bash tool call like
`echo $OPENROUTER_API_KEY` or `cat /proc/self/environ` and read any secret in the process
environment. This is a **Bash** path, not a `Read` path — `Read` is confined to the
workspace by the osfs adapter, but the shell inherited the full environment, so the prompt
fence did not contain it: a successfully-injected agent with shell access could exfiltrate
the LLM key. The "hardened" sandboxed runners (read-only subagent / team-member /
force-copy) were no safer — `gitenv.Scrub` only ever dropped `GIT_*`/`PAGER`, never secrets.

**The fix (engine-layer, `internal/adapter/envscrub` + `internal/app`).** A new stdlib-only
leaf `internal/adapter/envscrub` (`envscrub.Scrub`) computes the agent shell's environment
as `os.Environ()` MINUS the harness's credentials, via a **precise denylist**:

- the EXACT credential variable names the harness reads — the provider keys
  (`OPENAI_API_KEY` / `OPENROUTER_API_KEY` / `ANTHROPIC_API_KEY`), the websearch keys
  (`WEBSEARCH_API_KEY` / `BRAVE_API_KEY` / `EXA_API_KEY`), the auth tokens
  (`MECATL_AUTH_TOKEN` / `MECATL_DRIVER_AUTH_TOKEN`), and the forge tokens
  (`GH_TOKEN` / `GITHUB_TOKEN`); PLUS
- a conservative secret-SHAPED name pattern as defence-in-depth (`*_API_KEY` / `*_TOKEN` /
  `*_SECRET` / `*_PASSWORD` / `*_PASSWD` / `AWS_*` / `AZURE_*` /
  `GOOGLE_APPLICATION_CREDENTIALS`).

A **denylist** (not an allowlist) is deliberate: a coding agent runs `go build`/`go test`/
`git`, which need `PATH`, `HOME`, `GOPATH`, `GOCACHE`, `GOMODCACHE`, `TMPDIR`, `LANG` and an
open-ended toolchain set — an allowlist would silently break a build the moment a tool
needed a var nobody enumerated. The scrub keeps the whole toolchain and removes only
credentials.

It is wired into the existing `osfs.WithCommandEnvList` seam for **every** agent-adjacent
shell, one policy:

- `buildCommandRunner` (the MAIN session, the runner posture `auto`/`yolo` exposes) —
  secret-scrubbed only (operator hooks/pager still honoured);
- `newHardenedCommandRunner` (read-only subagent / team-member / force-copy branches) and
  `gitSnapshot` and the forker's fork-time git — `gitenv.Scrub(envscrub.Scrub(os.Environ()))`,
  i.e. secret-scrub first, then git-neutralise.

**Residual.** The post-run diff/artifact git invocations in `cmd/mecatequi` are NOT
agent-facing (their output never reaches the model), so they are out of this scope. The
security oracle is `internal/app/command_runner_secret_scrub_test.go` (mutation-tested:
reverting the scrub makes it fail) plus `internal/adapter/envscrub/envscrub_test.go`.

**v1 workflow mitigation (still in force, defence-in-depth).** The `implement` job holds
**only** the LLM key and **no GitHub write token**, so even if a future regression reopened
the read, the blast radius would be the **rotatable LLM key**, not repository write access.

## 7. Deferred: conversational v2

A multi-turn, conversational mecatequi (a bot that holds a thread across comments) is
deferred. It needs two things v1 lacks:

- **Snapshot fidelity** — the run state would have to survive between comment events,
  which means a durable session store the next invocation rehydrates from (the cloud-native
  rehydration seam, `docs/adr/0027-cloud-native.md`).
- **A driver-store-as-artifact backend** — the externalised store could be backed by a
  driver (`docs/adr/0005-driver-seams.md`) whose storage is a workflow artifact or a repo branch,
  so the forge remains the store across turns.

v1 is one-shot on purpose: it is the simplest thing that is safe, and it defers the state
machinery until a conversational use case justifies it.

## 8. Usage

The operator-facing walkthrough — the action input/output table, the reusable-workflow
caller, and the example-workflow copy-and-review steps — lives in `docs/usage.md` ("Running
mecatequi from GitHub Actions"). The reusable workflow is
`.github/workflows/mecatequi-reusable.yml` (the recommended adoption path, §5.1); the
hand-rolled escape-hatch template is `.github/workflows/mecatequi-example.yml`, and their
place alongside the other workflows is documented in `.github/workflows/README.md`.

---

*Part of the [design docs](../design/README.md). Related: [Cloud-native arc: disposable process, externalized state, durable record](0027-cloud-native.md)
(the inverse — disposable process, externalized state), [Driver seams — ports, the gRPC driver protocol, and conformance](0005-driver-seams.md) (the
store seam a conversational v2 would build on), [Unattended / allow-all posture (the "YOLO mode" question)](0022-allow-all-posture.md)
(the `auto`/`yolo` posture the CI run selects).*
