## 15. Running mecatequi from GitHub Actions

`mecatequi` (the single-shot headless runner, `cmd/mecatequi`) runs one prompt against
an in-process engine and emits a working-tree git diff, a machine-readable summary JSON,
and an optional durable event log, then exits with a code derived from the run's terminal
state. Two adoption paths wire it into GitHub Actions safely: a **reusable `workflow_call`
workflow** (the recommended path — a ~15-line caller, no vendored scripts) and a hand-rolled
**example workflow** (the escape hatch — vendor it when you need to customise the job graph).
Both build on the same **composite actions**. The design rationale, the trust model, and the
(now fixed) secret-scrubbed agent shell live in `docs/adr/0028-mecatequi.md`; this section is
the operator walkthrough.

> The example workflow is a **template** — copy it into your own repo and review it. This
> repo does not run it against real issues (no `mecatequi` label, no configured secret).

### The factory invocation profile (scheduler-launched runs)

GitHub Actions is not the only launcher: a **scheduler** (e.g. titlani in the same
platform) launches mecatequi as a one-shot Kubernetes Job or local subprocess per work
item, with no human and no forge glue attached
([ADR 0082](../adr/0082-factory-mcp-wiring.md); the scheduler half is titlani's
run-contract ADR 0010). The profile such a launcher should use:

```sh
MCP_TEQUITL_TOKEN=<per-run token> MCP_VMCP_TOKEN=<per-run token> \
mecatequi \
  --posture auto \
  --untrusted-prompt --prompt-file /work/task.md \
  --timeout 30m \
  --mcp-server tequitl=https://tequitl.internal/mcp \
  --mcp-server vmcp=https://vmcp.internal/mcp \
  --out-summary=-
```

- **`--posture auto` is required for unattended runs.** The default `strict` asks on
  every mutating tool call, and a headless run has no approver — the run is CANCELLED
  on the first ask and exits 1. `auto` is the recommended unattended tier (allow-all,
  child injection-defense ON).
- **`--untrusted-prompt`** whenever the task body is externally sourced (an issue body,
  a task-graph node): the prompt is wrapped in the harness untrusted-data fence so the
  model treats it as data to act on, not instructions to obey.
- **`--timeout`** is the wall-clock backstop (orthogonal to `--max-run-tokens` /
  `--max-turns`); a timed-out run exits 1 with `stop_reason=cancelled`.
- **`--mcp-server <name>=<url>` + `MCP_<NAME>_TOKEN`** wire the run's MCP endpoints
  (repeatable). The scheduler injects a **short-lived per-run identity** as
  `MCP_<NAME>_TOKEN` (name upper-cased); the run presents it as
  `Authorization: Bearer …` to that server. The token is optional — an entry with no
  matching env var connects without auth. Names must match `[A-Za-z0-9_]+` and be
  case-insensitively unique (they derive the env var), and a token-bearing URL must
  be `https` (or `http` to loopback) so the bearer never travels cleartext off-host.
  The same flag + env convention works on `mecated` and `mecak8s` — but on the
  long-lived `mecak8s` daemon the token is read once at startup and shared across
  sessions, so per-run identity is a mecatequi property
  ([ADR 0082](../adr/0082-factory-mcp-wiring.md)).
- **`--mcp-server-insecure-http <name>`** (repeatable) is the EXPLICIT per-server
  opt-out of that https rule for in-cluster plain-http Services
  ([ADR 0090](../adr/0090-mcp-insecure-http-optin.md)): it lets the named server's
  bearer ride `http` to a non-loopback host — acknowledging the token travels
  **cleartext on the network path**, with network-layer controls
  (NetworkPolicy / namespace trust) plus the short-lived token as the operator's
  mitigations. It relaxes ONLY the http scheme gate, ONLY for that name, and is
  **order-independent** of where its `--mcp-server` appears on argv (a scheduler
  composing flags need not order them). Naming a server that is not registered — or
  whose URL is already `https` / loopback / non-http — fails startup loudly: a stale
  acknowledgment is an error, never silently inert. Example (an in-cluster
  NetworkPolicy-scoped Service):

  ```sh
  --mcp-server tequitl=http://tequitl.tenant-a.svc:9100/mcp \
  --mcp-server-insecure-http tequitl
  ```
- **`--out-summary=-`, passed EXPLICITLY**, selects the stdout-compact summary mode:
  the run summary is emitted as a **single compact JSON line as the FINAL stdout
  line** (nothing follows it on stdout). The unset default (also stdout) keeps the
  human-friendly indented JSON — behavior unchanged for existing pipelines.
  **The guarantee is stdout-only**: a Kubernetes pod log merges stderr (diagnostics,
  the per-event trace, the verdict line) into the same stream, so a log-tailing
  consumer must take the **last line that parses as the `Summary` JSON**
  (`schema_version` present) — not blindly the last line of the pod log. Capturing
  stdout directly (subprocess pipe, or a container runtime that separates streams)
  makes the literal last line safe.
- **`--run-id` / `--task-ref` are deliberately NOT accepted.** mecatequi stays
  scheduler-agnostic: correlate a run via your own launch identity (Job name, pod
  labels) plus the `session_id` the Summary already carries.

### The composite action (`.github/actions/mecatequi`)

The action builds the binary from the action's **own** checkout (`go build -C
"$GITHUB_ACTION_PATH/../../.." ./cmd/mecatequi`, toolchain from the action's `go.mod`) — the
matlatl pattern. This works uniformly for self-use (`uses: ./.github/actions/mecatequi`) and
cross-repo (`uses: stacklok/mecatl/.github/actions/mecatequi@<tag>`, where GitHub checks the
tagged mecatl repo into `$GITHUB_ACTION_PATH`); see "Adopting mecatequi in another repo"
below. There is **no token and no `GOPRIVATE`** — the action source is the build input. It
then runs the binary with `--untrusted-prompt` by default, captures the exit code **without
failing the step**, and exposes the result as outputs. The LLM key is **not** an input: the
binary reads provider secrets from the environment, so the caller sets `OPENAI_API_KEY` (or
`OPENROUTER_API_KEY`, etc.) in the calling **job**'s `env` (job-level, not step-level — step
`env:` on a `uses:` step does not reach a composite action's internal steps; job `env:`
does).

**Inputs → flags:**

| Input | Flag | Default |
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
| `openai` | `--openai` (when `true`) | `""` |
| `openai-base-url` | `--openai-base-url` | `""` |
| `guardrails-model` | `--guardrails-model` | `""` |
| `subagent-ask-reviewer` | `--subagent-ask-reviewer` (omitted when empty) | `""` |
| `subagent-ask-reviewer-max-denies` | `--subagent-ask-reviewer-max-denies` (omitted when empty) | `""` |
| `pr-body-template` | `publish.sh` PR-body template (via `MQ_PR_BODY_TEMPLATE`) | `""` |
| `pr-title-template` | `publish.sh` PR-title template (via `MQ_PR_TITLE_TEMPLATE`) | `""` |
| `out-diff` | `--out-diff` | `$RUNNER_TEMP/mecatequi.patch` |
| `out-summary` | `--out-summary` | `$RUNNER_TEMP/mecatequi.summary.json` |
| `out-events` | `--out-events` | `$RUNNER_TEMP/mecatequi.events.jsonl` |

Prefer `model` for newer/passthrough models; `default-model` is catalog-validated and
rejects ids not in the embedded snapshot. `model` is the per-session passthrough path — it
accepts any model the provider serves.

The `subagent-ask-reviewer` / `subagent-ask-reviewer-max-denies` pair is **escape-hatch-only**
— it is exposed by the composite action but **not** surfaced by the reusable workflow, so reach
for it only when you hand-roll a workflow against the action directly.

**Outputs** (kebab-case): `patch-path`, `summary-path`, `events-path`, `summary-json`
(compacted JSON — best-effort and size-bounded by the `$GITHUB_OUTPUT` cap; read
`summary-path` for anything large), `stop-reason`, `non-empty-diff`, and `exit-class`
(`clean` / `run-failure` / `setup-failure`, derived from the captured exit code). Branch on
`exit-class`, not the raw code — and remember exit 0 is **not** "task accomplished": read
`stop-reason` and `non-empty-diff` to judge whether real work landed.

The action bakes in a default `instructions` value (TRUSTED framing, emitted **outside** the
prompt fence): write the final message as a PR description, and self-verify (run the repo's
build/lint/test until green) before finishing. For that self-verification to run, the agent
needs the repo's build/lint/test tools on PATH — so the action itself now **always provisions
`task` (go-task) + golangci-lint** in the same job, before the binary (Go is provisioned for
the binary build). That makes the **80% Go case work out of the box** on both adoption paths
(the reusable workflow and a direct action reference). For a project toolchain beyond that
(buf, protoc plugins, node, system packages), pass the **`setup-script`** input — operator
shell the action runs in the same job, before the binary. `setup-script` is **trusted operator
code** (a maintainer sets it), distinct from the untrusted prompt, and runs at `contents: read`
with no write token, so it cannot push or open a PR. Override the `instructions` input to
replace the framing wholesale; an empty value omits it.

### Adopting via the reusable workflow (recommended)

The reusable workflow (`.github/workflows/mecatequi-reusable.yml`, `on: workflow_call`) lets
a consumer adopt mecatequi with a **thin caller** instead of vendoring the whole
split-privilege job graph plus the glue scripts. It is the same three jobs
(`acknowledge` / `implement` / `publish`) with the same per-job permissions, so the token
boundary is preserved — the agent job holds only the LLM key(s) and no write token; the
publish job holds the write token and runs no agent code.

A minimal caller in the consuming repo:

```yaml
name: mecatequi
on:
  issues:
    types: [labeled]
  issue_comment:
    types: [created]
permissions:
  contents: read
jobs:
  mecatequi:
    uses: stacklok/mecatl/.github/workflows/mecatequi-reusable.yml@<latest>
    permissions:
      contents: write
      pull-requests: write
      issues: write
    secrets:
      openrouter-key: ${{ secrets.OPENROUTER_CI_TOKEN }}
      # Publish token — wire ONE form (see the table). The JIT App-token form is strongest:
      publish-app-id: ${{ secrets.RELEASE_APP_ID }}
      publish-app-private-key: ${{ secrets.RELEASE_APP_PRIVATE_KEY }}
    with:
      label: ready-for-agent
      model: anthropic/claude-sonnet-4.6
      default-provider: openrouter
```

The caller grants the workflow the permissions its `publish` job needs (a called workflow's
token can only be **downgraded** from the caller's grant, never escalated), passes the
trigger label/mention as **inputs** (a reusable workflow can't reliably read the caller's
`vars.*`), and wires the secrets it has.

**The caller owns the `on:` triggers.** The reusable workflow declares only
`on: workflow_call` — it has **no** `issues` / `issue_comment` triggers of its own. The
caller's own `on:` block (the `issues: [labeled]` + `issue_comment: [created]` in the
example above) is what actually fires the run; the workflow's `if:` gates then match the
`label` / `mention` inputs. **Omit or mis-set those triggers and you get total silence** —
no run, no error — because nothing ever delivers an event to the reusable workflow.

**Pin the latest released tag, not `@v0.0.4`.** The `@v0.0.4` in every example here is
**illustrative**. Pin the **latest released tag** from the
[Releases page](https://github.com/stacklok/mecatl/releases) — a `uses:` ref that points at a
tag which does not exist (e.g. a reader landing between releases) fails with GitHub's generic
*"workflow not found"* error, which looks like a config bug but is just a stale ref.

**Secrets (`secrets:`)** — all optional; wire only what you use:

| Secret | Purpose |
|---|---|
| `openrouter-key` | `OPENROUTER_API_KEY` for the agent job. |
| `openai-key` | `OPENAI_API_KEY` for the agent job. |
| `anthropic-key` | `ANTHROPIC_API_KEY` for the agent job. |
| `publish-app-id` + `publish-app-private-key` | **Form 1 (strongest):** a JIT GitHub App installation token is minted in the publish job — no standing grant. |
| `publish-token` | **Form 2:** a pre-minted token (e.g. a fine-grained PAT) you manage. |
| *(none of the three)* | **Form 3:** the publish job falls back to the standing `GITHUB_TOKEN` grant with a `::warning::`. Zero-config, but the weakest form. |

An undefined provider secret is the **empty string**, which the binary treats as **absent**
(it registers a provider only for a non-empty key), so a caller wires only the provider(s) it
uses; `default-provider` forces the choice.

**If you set `default-provider`, wire THAT provider's key.** `default-provider` only *selects*
which provider to use — it does not supply a key. Set `default-provider: openrouter` but wire
only `openai-key`, and the run fails at startup with *"no LLM provider available"* (the
selected provider has no key, and the binary will not silently fall back to a different one).
The pairing is: `default-provider: openrouter` ⇒ `openrouter-key`; `default-provider: openai`
⇒ `openai-key`; `default-provider: anthropic` ⇒ `anthropic-key`. If you wire exactly one
provider key and omit `default-provider`, the binary auto-detects it, which is the simplest
correct setup.

**Setting up publish-token Form 1 (the recommended JIT GitHub App).** Form 1 mints a
short-lived installation token scoped to exactly this repo — no standing PAT to manage or
leak. To set it up:

1. **Create a GitHub App** (org or personal): *Settings → Developer settings → GitHub Apps →
   New GitHub App*. Give it a name; you can leave the homepage/webhook fields blank and
   **disable the webhook** (the App is used only for token minting, not event delivery).
2. **Grant repository permissions** — under *Permissions → Repository*, set **Contents:
   Read and write**, **Pull requests: Read and write**, and **Issues: Read and write**
   (these are exactly what `publish.sh` needs to push the branch, open the PR, and comment).
3. **Create a private key** for the App (*General → Private keys → Generate a private key*) —
   download the `.pem`.
4. **Install the App** on the target repo (*Install App → choose the repo*). Installation is
   what scopes the minted token to that repo.
5. **Wire the two secrets in the consuming repo**: store the App's **App ID** as the
   `publish-app-id` secret and the **`.pem` contents** as `publish-app-private-key`. The
   publish job mints the installation token from them in-job.

The minting itself happens inside the reusable workflow's `publish` job (via
`actions/create-github-app-token`); you only supply the two secrets.

**Inputs (`with:`)** — all optional with sane defaults: `label` (default `mecatequi`),
`mention` (default `@mecatequi`), `model`, `default-provider`, `posture` (default `auto`),
`max-run-tokens`, `max-turns` (per-run turn cap; empty uses the deployment default),
`timeout` (default `40m`), `openai-base-url` (for an OpenAI-compatible
endpoint), `guardrails-model` (checker model; configuring it OR a bound `guardrail` model slot enables guardrails, ADR 0046; empty + no slot disables), `setup-script`
(multi-line shell run before the binary to install a project toolchain beyond the always-on
`task` + golangci-lint — see the toolchain note above), `base-branch`, `pr-body-template`,
`pr-title-template`. The escape-hatch-only knobs (`default-model`,
`openai`, `subagent-ask-reviewer`) are deliberately **not** exposed by the reusable workflow —
a consumer that needs them vendors the example template instead.

**Org-access requirement.** Because `stacklok/mecatl` is private, the consuming org must
allow Actions to use its actions/workflows: **Settings → Actions → General → Access** on
`stacklok/mecatl` (or `gh api -X PUT
repos/stacklok/mecatl/actions/permissions/access -f access_level=organization`). No token or
PAT is configured in the consuming repo — the org setting is the only requirement.

**If that access is NOT enabled, the caller fails with a generic *"workflow not found"* /
access error** at the `uses:` resolution step — which reads like a typo in your YAML but is
actually the org setting. The fix is the **Access** setting above, not your caller workflow.
This is the same surface as the stale-tag failure mode (see "Pin the latest released tag"),
so check both when a `uses:` ref will not resolve.

**Create the trigger label first** (and set `label` / `mention` if you renamed them) — the
`labeled` trigger silently never fires for a label that does not exist.

If you need to customise the job graph itself — a custom permission-check gate job, an extra
approval stage, a different trigger — use the **vendor-the-directory escape hatch** below
instead.

### The example workflow (`.github/workflows/mecatequi-example.yml`)

The template is the **escape hatch** — the canonical **split-privilege** pattern you vendor
and edit when the reusable workflow's fixed job graph is not enough — *the step that can write to
GitHub never runs agent code; the step that runs agent code never holds a write token* —
plus an `acknowledge` job that guarantees the issue always carries a trace:

- **`acknowledge`** (`issues: write` only, no agent code, no LLM key): runs **first**,
  gated on the same trigger as `implement`, and posts an early *"🤖 mecatequi is working on
  this — see the run: …"* comment with the run URL. This guarantees a durable issue-side
  signal **even if everything downstream fails** — the failure mode where a skipped or
  failed publish left the issue author staring at silence.
- **`implement`** (`needs: acknowledge`; `contents: read`, no write, no id-token):
  checkout → `author-gate.sh` (defense-in-depth permission check, see the gate note below) →
  `extract-prompt.sh` writes the **untrusted** issue/comment text into a file via `jq`
  over `$GITHUB_EVENT_PATH` (never an inline `${{ }}`) → `uses: ./.github/actions/mecatequi`
  with `OPENAI_API_KEY` as the **only** secret, delivered at **job** level (step `env:` on a
  `uses:` step would not reach the composite's internal steps) → upload-artifact the
  patch/summary/events.
- **`publish`** (`needs: [acknowledge, implement]`; `contents: write` +
  `pull-requests: write` + `issues: write`): download-artifact (**non-fatal** — a missing
  artifact must not abort before `publish.sh` runs) → `publish.sh` applies the patch as
  **data** (`git apply`) → branch → commit → PR, or posts an honest failure comment.
  Its `if:` fires on implement **success or failure** (`!cancelled() &&
  needs.acknowledge.result == 'success'`) so a failed run still gets a terminal comment;
  `publish.sh` reads a missing/empty `EXIT_CLASS` as setup-failure, and a failed push /
  `gh pr create` comments before exiting non-zero (never a silent abort after deciding to
  open a PR). It runs no agent output as code.

Trigger: `issues` `labeled` with the `mecatequi` label, OR `issue_comment` `created`
mentioning `@mecatequi` on an issue (PR comments are excluded). Every external action is
SHA-pinned with a `# vX.Y.Z` comment, the workflow default is `permissions: contents:
read`, both job checkouts pin the immutable `github.sha` (so the patch applies onto the
tree it was diffed against), and a `concurrency` group keyed on the issue number prevents
overlapping runs.

**The author gate (read this — `author_association` is a trap).** The gate that keeps an
unauthorised author out is **trigger-based**, not `author_association`-based. GitHub's
webhook `author_association` is **unreliable for membership** — it reports an org MEMBER as
`CONTRIBUTOR`, so a gate that asserts `author_association ∈ {OWNER, MEMBER, COLLABORATOR}`
silently **skips legitimate runs**. The **live** workflow for this repo
(`.github/workflows/mecatequi.yml`) therefore removed that assertion entirely:

- For a **private repo**, the trigger **is** the gate: applying a label needs triage/write
  access and commenting is team-only, so GitHub's own permission model decides who can
  start a run. No `author_association` check is needed.
- For a **public repo**, do **not** trust `author_association`. Add a dedicated
  permission-check gate **job** that calls the `collaborators/{user}/permission` API and
  gates `implement`/`publish` on its result.

`author-gate.sh` (which asserts `author_association`) ships in the **example template
only** as a defense-in-depth illustration; it is deliberately absent from the live
workflow. See the [mecatequi design doc](../adr/0028-mecatequi.md).

The action and its scripts are meant to be **vendored** — copied into your repo and
reviewed, not referenced by tag — so you control exactly what runs. To enable it:

1. Copy `.github/workflows/mecatequi-example.yml` and the whole
   `.github/actions/mecatequi/` directory into your repo.
2. Add the `OPENAI_API_KEY` repository secret (the agent job's only secret).
3. **Create the `mecatequi` label FIRST.** You cannot apply a label that does not exist,
   and the `labeled` trigger **silently never fires** without it — so the label must exist
   before anyone tries to apply it, or the workflow simply does nothing with no error.
4. Review the posture (`auto` is the documented CI default) and decide whether to keep the
   broad `GITHUB_TOKEN` in the `publish` job or upgrade to a JIT GitHub App token (the
   stronger option — see `docs/adr/0028-mecatequi.md`).
5. Apply the `mecatequi` label to a test issue and watch the run.

**Configurable trigger label / mention.** The trigger label and comment mention default to
`mecatequi` and `@mecatequi`, but both are overridable via **repo Actions variables** so you
can rename them without editing the workflow: set `MECATEQUI_LABEL` and `MECATEQUI_MENTION`
under **Settings → Secrets and variables → Actions → Variables**. Both job gates
(`acknowledge` and `implement`) read the same variables, so they cannot drift. If you change
`MECATEQUI_LABEL`, create the new label first (step 3 above — the `labeled` trigger silently
never fires for a label that does not exist).

### Adopting mecatequi in another repo (by tag, without the reusable workflow)

> Most consumers should use the **reusable workflow** ("Adopting via the reusable workflow
> (recommended)" above) — a thin caller, no vendored scripts. This subsection covers the
> lower-level path of referencing the **composite action** by tag directly, for a workflow
> you hand-roll yourself.

The example workflow vendors the action (copies `.github/actions/mecatequi/` into your
repo). To instead **reference mecatl's published action by tag** — no vendored copy — point
`uses:` at the subdirectory action and pin a tag. GitHub checks the mecatl repo out at that
tag into the action path and the action builds the binary from it (the matlatl pattern), so
there is **no token and no `GOPRIVATE`** to manage:

```yaml
- name: mecatequi
  id: mecatequi
  uses: stacklok/mecatl/.github/actions/mecatequi@<latest>   # pin the latest released tag
  with:
    prompt-file: ${{ runner.temp }}/prompt.txt
    posture: auto
```

`owner/repo/path@ref` is the GitHub syntax for an action that lives in a repository
subdirectory; pin it to an alpha `v0.0.x` tag (the first published tag is `v0.0.1`) or a
full commit SHA — the `@<ref>` is the version (there is no version input). What a consumer
needs:

- **Org access to mecatl's actions.** Because `stacklok/mecatl` is private, the org must
  allow Actions to use its actions: **Settings → Actions → General → Access** on
  `stacklok/mecatl` (or `gh api -X PUT
  repos/stacklok/mecatl/actions/permissions/access -f access_level=organization`). No token
  or PAT is configured in the consuming repo — the org setting is the only requirement.
- **`OPENROUTER_API_KEY`** (or `OPENAI_API_KEY`, etc.) — the LLM key, set as a repository
  secret and injected at the **agent job's** `env` level (not on the `uses:` step).
- **Create the trigger label first** (and, if you renamed it, set `MECATEQUI_LABEL` /
  `MECATEQUI_MENTION`) — see the enable steps above.

Honest cost: the action builds the binary from source on each run (no cached release asset
yet). A signed, checksummed binary / container is the later speed optimization — see
`docs/adr/0028-mecatequi.md` §5.

### Customising the PR description (templates)

By default `publish.sh` writes a rich built-in PR body (caveat + "What the agent did" +
"Files changed" + "Run" table + run link + `Closes #<n>`). To use your own style, supply a
template file of `{{placeholder}}` tokens. Resolution, in order:

1. The `MQ_PR_BODY_TEMPLATE` env on the `Publish` step (a path relative to the checkout).
2. Else `.github/mecatequi/pr-body.md` in the checkout — **the zero-config convention**.
3. Else the built-in body (absent template = today's behaviour, unchanged).

Easiest activation: copy the shipped `.github/mecatequi/pr-body.md.example`, edit it, and
**rename it to `.github/mecatequi/pr-body.md`** — no workflow change needed. An optional
`MQ_PR_TITLE_TEMPLATE` overrides the PR title (same placeholders). The **default** title is
`<issue title> (#<n>)` — the triggering issue's title, fetched READ-only via `gh issue view`
in the privileged publish job (the agent job holds no GitHub token), falling back to the prior
`mecatequi: changes for issue {{issue}}` literal when the title can't be fetched. In a title, use
the **short** placeholders (`{{issue_ref}}`, `{{issue_title}}`, `{{stop_reason}}`,
`{{branch}}`) — prose ones like `{{what_agent_did}}` or `{{summary_table}}` flatten to one
unwieldy line.

Placeholders: `{{what_agent_did}}`, `{{files_changed}}`, `{{summary_table}}`, `{{run_url}}`,
`{{issue}}`, `{{issue_ref}}`, `{{issue_title}}`, `{{stop_reason}}`, `{{non_empty_diff}}`, `{{diff_bytes}}`,
`{{total_tokens}}`, `{{branch}}`, `{{base}}`. An unknown `{{token}}` is left intact.

Two things to know: the `⚠️ Agent-authored — review carefully before merging.` caveat is
**always** force-prepended (a template cannot drop it — so do **not** add your own ⚠️ caveat
line in the template, or you get a duplicate), and **issue linkage is yours** in a custom
template — use `{{issue_ref}}` with `Closes`/`Refs` (the built-in default uses `Closes`).
Substitution is literal and single-pass over agent-authored (untrusted) values — see
`docs/adr/0028-mecatequi.md` for the mechanism and the full rationale.

### Injection safety (why it is built this way)

Untrusted issue text never appears in a `${{ }}` interpolation inside a `run:` body or as
an argv token. Extraction is `jq` over the event JSON file into a file; the file reaches
the binary via `--prompt-file`; the binary fences it. Every event-derived value
(author association, issue number, paths) is passed via `env:`. The produced patch is
applied as data, never executed. **Defense in depth:** every agent-facing Bash shell runs
with a secret-scrubbed environment (the harness's
provider/auth/forge credentials are dropped before the shell sees them), so a hijacked
agent cannot `echo $OPENROUTER_API_KEY` / `cat /proc/self/environ` to exfiltrate them; and
the workflow still bounds the blast radius by holding only the rotatable LLM key (no write
token) in the agent's job. See `docs/adr/0028-mecatequi.md` §6.

