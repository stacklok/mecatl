# GitHub Actions workflows for mecatl

Seven workflows live here. Every **third-party** action is **SHA-pinned** with a
`# vX.Y.Z` comment so a re-pointed tag from a compromised maintainer cannot
silently change what runs. Pins track the Stacklok house set used in
`a downstream consumer`. The lone exception is documented and deliberate: the reusable
`mecatequi-reusable.yml` references its **first-party same-repo** sibling actions
by **version tag** (`@v0.0.4`), not a SHA — see that section.

## `ci.yml` — push to `main` + every pull request

Runs on `pull_request` (**not** `pull_request_target`): PR code is untrusted and
must run without secrets. `pull_request_target` runs in the base repo's context
*with* secrets — combining it with a checkout of PR code is the classic
"pwn request" RCE pattern, so it is deliberately avoided.

Jobs (each least-privilege at `contents: read`, `timeout-minutes` set,
superseded runs cancelled via `concurrency`):

| Job | What it runs |
|-----|--------------|
| `build` | `go build ./...` |
| `test` | `go test -race ./...` |
| `lint` | `golangci-lint` (v2) + `go vet ./...` + `actionlint` (workflow lint, pinned via `go run`) + the reusable-workflow pin check + the empty-expression (action-templates) check + the mecatequi composite-action shell tests |
| `fuzz-smoke` | `task fuzz FUZZTIME=300000x` — short coverage-guided pass over the security-critical parsers (not the nightly deep fuzz); an iteration count, not a duration, so it can't race the fuzz coordinator's own deadline |

Go is provisioned by `actions/setup-go` from `go.mod` with the module cache
enabled; `GOTOOLCHAIN=local` prevents a surprise toolchain download.

## `e2e-live.yml` — nightly + manual + `e2e-live` PR label

Runs the **live** e2e suite (`task e2e` → `go test -tags e2e ./e2e/...`, see
`e2e/README.md`): real OpenRouter model calls, real (fractions-of-a-cent)
money. **Non-blocking** — it is not a required check; it exists to catch
live-provider regressions (prompt filters, API drift, model behaviour) the
offline suite cannot see.

Trigger model:

- `schedule` — nightly at 03:17 UTC.
- `workflow_dispatch` — on demand.
- `pull_request` — **only** when a maintainer applies the **`e2e-live`**
  label (`labeled` is in the activity types, so applying the label starts a
  run). The label gate lives in the job `if:` so an unlabelled PR never
  starts the job.

Secret: **`OPENROUTER_API_KEY`** (repo secret, set by the operator). Exposure
is layered:

1. `pull_request`, never `pull_request_target` — fork PR runs get no secrets
   even when labelled.
2. The job `if:` requires `github.repository == 'stacklok/mecatl'`, so forks'
   own scheduled/dispatched runs never reference the secret.
3. A guard step checks key availability and **skips cleanly** (green, with a
   notice) when it is absent — a labelled fork PR or an unconfigured repo
   never hard-fails.
4. The key is injected via `env` into exactly one step, never interpolated
   into a script body or argv.

Note the residual, accepted exposure: a **same-repo** labelled PR runs that
branch's code with the secret in its environment — the label is the
maintainer's explicit opt-in, and same-repo branch authors already have write
access.

Posture: `permissions: contents: read`; one global `concurrency` group
(`e2e-live`, cancel-in-progress) so overlapping dispatches never double the
spend or trip provider rate limits; `timeout-minutes: 30` (the suite runs ~2
minutes; the suite's own layered SpecTimeouts live inside a 60m `go test`
budget, so the job timeout is the cheap runner-billing backstop). On failure
the self-diagnosing transcripts (`.scratch/e2e-*/artifacts/**`, including
`mecated.log`) upload as a 7-day artifact, and the cumulative token/cost
ledger is appended to the job summary when present in the test output.

## `perf.yml` — push to `main` + every pull request

The **Phase 3** performance-regression gate + trend store of
`docs/adr/0019-perf-tracking.md`. Deliberately a **separate** workflow from `ci.yml`
because its two jobs need different permission postures. Runs on `pull_request`
(**not** `pull_request_target`) — PR code is untrusted; the only credential is the
automatic `GITHUB_TOKEN`, scoped per job.

| Job | Trigger | Permissions | What it does |
|-----|---------|-------------|--------------|
| `perf-pr` | `pull_request` | `contents: read` | Builds the benchmarks; HARD-gates `allocs/op` via `perf/cmd/allocsgate` against the previous-main baseline (epsilon ≥ 1 alloc AND > 2 %); runs the scenario suite through `github-action-benchmark` with `fail-on-alert: true` but `auto-push: false`. **Fails a regressing PR; never writes the store.** |
| `perf-main` | `push` to `main` | `contents: write` | Same build, then pushes the trend data to `gh-pages` (`auto-push: true`): an advisory `go`-tool `ns/op`+allocs dashboard (`fail-on-alert: false`), the gated scenario suites, and the raw `bench.txt` baseline (rebase-retry push) the next PR fetches. Re-runs the allocs gate against the just-superseded baseline to flag a regression that merged. |

Mechanism: a deliberate **split** of two OSS tools — a tested allocs gate
([`perf/cmd/allocsgate`](../../perf/cmd/allocsgate/main.go)) over `task bench`, plus
`benchmark-action/github-action-benchmark` over the scenario KPIs. The gate decision
is allocsgate's epsilon comparison over the raw bench numbers (allocs are
deterministic — no significance test needed); it is FAIL-CLOSED on empty/corrupt
input. `benchstat` stays the LOCAL human A/B tool (see `task bench` help), not the CI
gate. The scenario JSON is reshaped by
[`perf/cmd/perfconvert`](../../perf/cmd/perfconvert/main.go) into two custom suites:
a *smaller-is-better* suite (`allocs_per_op`, `tokens_total`, `goroutine_delta`,
one `102%` threshold → scenario allocs gated at 2 %, safe because deterministic) and
a *bigger-is-better* suite (`cache_hit_rate`, `105%`, ONLY for the whitelist
`{single_session_long, team_fanout}` — the by-design-0 scenarios emit no point).
allocs/op is hard-gated; `ns/op` is advisory. The first PR before any baseline
exists skips the allocs gate green with a notice. See
`docs/adr/0019-perf-tracking.md` (Phase 3 — Status) for the full rationale.

**Live trend dashboard:** https://potential-barnacle-mvm429e.pages.github.io/dev/bench/
(sign in to GitHub with repo access; the random slug is GitHub's private-Pages
hostname, stable across builds; the chart is under `/dev/bench/`, the bare root 404s).

### PGO is orthogonal to the regression gate (Phase 4)

Profile-Guided Optimization (perf-tracking Phase 4) is a **build-time
optimization**, NOT part of any regression gate. No workflow logic changes for it.
The mechanism is just a reserved slot: once a real `cmd/mecated/default.pgo` exists,
every `go build` / `ko build` (here in `ci.yml` and in `release.yml`) auto-applies it
via the toolchain default `-pgo=auto` — no flag, no workflow edit. Today no profile
is committed (the offline one `task pgo:collect` produces is provisional and stays
under the gitignored `.scratch/pgo/`), so `-pgo=auto` is a no-op and the build is the
non-PGO build. PGO never gates a PR — it does not catch regressions, it shaves CPU —
so `perf.yml` is unaffected. See `docs/adr/0019-perf-tracking.md` (Phase 4 — Status).

## `release.yml` — `v*` tag push (+ `workflow_dispatch` with a `tag` input, for idempotently re-publishing an existing tag's artifacts)

Builds and publishes the `mecated` image and its supply-chain metadata, and a
sibling `publish-mecatui` job builds/publishes/signs/attests the `mecatui`
container image (issue #302) under `ghcr.io/<owner>/<repo>/mecatui` with the
same supply-chain story. The workflow defaults to `contents: read`; the two
publish jobs each elevate to exactly:

```yaml
permissions:
  contents: read       # checkout
  packages: write      # push image + SBOM to GHCR
  id-token: write      # OIDC: GHCR login, cosign keyless signing AND provenance
  attestations: write  # SLSA build provenance (actions/attest-build-provenance)
```

Flow:

1. **Build + push (ko)** — `ko build` straight from `./cmd/mecated` onto the
   digest-pinned distroless base in `.ko.yaml`, multi-arch
   (`linux/amd64,linux/arm64`), `--bare`, tagged `<version>` and `latest`.
   `KO_DOCKER_REPO=ghcr.io/${{ github.repository }}`. The image **digest** is
   captured so everything downstream signs the immutable artifact, not a tag.
2. **OIDC registry login** — `ko login ghcr.io` uses the job's `GITHUB_TOKEN`,
   scoped to this repo's packages by `packages: write`. No static registry
   credentials exist in repo secrets.
3. **SBOM** — `ko build --sbom=spdx` pushes an SPDX SBOM next to the image, and
   `anchore/sbom-action` regenerates an `spdx-json` file that is then attached
   as a **signed cosign attestation**.
4. **Keyless signing** — `cosign sign --yes <digest>`. The signing identity is
   this workflow's OIDC token (issuer
   `https://token.actions.githubusercontent.com`), recorded in the public Rekor
   transparency log. No long-lived signing keys.
5. **SLSA build provenance** — `actions/attest-build-provenance` generates a
   signed SLSA provenance predicate for the **image digest** (subject =
   GHCR repo + `sha256:...`, never a tag) and, with `push-to-registry: true`,
   stores it as an OCI referrer of the image. Keyless via the same Sigstore
   (Fulcio/Rekor) machinery — no keys. This binds *how and where* the artifact
   was built to the exact bytes that were published.

### Verifying a signed image (what a consumer runs)

Replace `<owner>/<repo>` and `<tag>`:

```sh
# Verify the keyless signature. The identity is the release workflow's ref.
cosign verify \
  ghcr.io/<owner>/<repo>:<tag> \
  --certificate-identity-regexp '^https://github.com/<owner>/<repo>/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# Verify the SBOM attestation (same identity flags).
cosign verify-attestation \
  ghcr.io/<owner>/<repo>:<tag> \
  --type spdxjson \
  --certificate-identity-regexp '^https://github.com/<owner>/<repo>/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# Pin to a digest for production pulls (verify prints the digest):
cosign verify ghcr.io/<owner>/<repo>@sha256:... <flags as above>
```

If you prefer an exact identity over the regexp, use
`--certificate-identity 'https://github.com/<owner>/<repo>/.github/workflows/release.yml@refs/tags/<tag>'`.

### Verifying SLSA build provenance

The provenance attestation is stored as an OCI referrer of the image and is
easiest to verify with the GitHub CLI, which knows the predicate type and the
expected Sigstore identity:

```sh
# Simplest: gh resolves the digest and checks the provenance was produced by
# this repo's workflow.
gh attestation verify \
  oci://ghcr.io/<owner>/<repo>:<tag> \
  --repo <owner>/<repo>
```

Or with cosign, pin to the digest and pass the same keyless identity flags used
for the signature (predicate type `https://slsa.dev/provenance/v1`):

```sh
cosign verify-attestation \
  ghcr.io/<owner>/<repo>@sha256:... \
  --type slsaprovenance1 \
  --certificate-identity-regexp '^https://github.com/<owner>/<repo>/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### SLSA provenance — implemented

Full **SLSA build provenance** is now generated for every published image via
GitHub's native `actions/attest-build-provenance`, keyed off the immutable image
digest (step 5 above). The native attestation was chosen over the
`slsa-framework/slsa-github-generator` container generator: it integrates as a
single hardened step in the existing publish job (only `attestations: write`
added), reuses the digest already captured, and stores a verifiable provenance
attestation with the image — no separate, separately-versioned reusable workflow
with its own permission/secrets contract. See <https://slsa.dev/>.

## `mecatequi-reusable.yml` — reusable `workflow_call` (the recommended adoption path)

The **reusable workflow** a consumer adopts mecatequi with — a ~15-line caller
instead of vendoring the whole split-privilege job graph plus the glue scripts
(which would drift from mecatl over time). Not triggered directly here; it is
called via `uses: stacklok/mecatl/.github/workflows/mecatequi-reusable.yml@<tag>`.

Same three jobs as the example (`acknowledge` / `implement` / `publish`) with the
same per-job permissions, so the **token boundary** is preserved — the agent job
holds only the LLM key(s) and no write token; the publish job holds the write
token and runs no agent code. A reusable workflow keeps per-job `permissions:`,
which a single composite action cannot.

It calls three **sibling composite actions** by **full path** —
`stacklok/mecatl/.github/actions/mecatequi-extract-prompt`, `…/mecatequi`, and
`…/mecatequi-publish` — because inside a `workflow_call` workflow a
`uses: ./.github/actions/…` (and any `run:` script) resolves to the **caller's**
checkout, not mecatl's; a full-path `uses:` is auto-fetched from mecatl instead.
Expressions are illegal in `uses:`, so those refs are **hardcoded version tags**
(`@v0.0.4`). These are first-party same-repo actions released together, so a tag —
not a SHA — is correct (the SHA-pin rule defends against third-party tag
re-pointing; we control both ends). **The release-process cost:** every tag must
bump these pins in the same tagged commit, or the tag ships pins pointing at the
previous version. `task lint:reusable-pins`
(`.github/actions/check-reusable-pins.sh`) asserts this mechanically, and CI runs
it. The third-party actions (`checkout`, `upload`/`download-artifact`,
`create-github-app-token`) stay SHA-pinned per the house set.

The **publish token** is the one consumer-specific interface: the workflow accepts
both a pre-minted `publish-token` and GitHub App creds (`publish-app-id` +
`publish-app-private-key`, the stronger JIT form) via `secrets:`, falling back to
the standing `GITHUB_TOKEN` with a `::warning::`. The three provider keys
(`openrouter-key` / `openai-key` / `anthropic-key`) are also `secrets:` (an
undefined one is the empty string, treated as absent). Full operator walkthrough:
`docs/usage.md` ("Adopting via the reusable workflow"); design + rationale:
`docs/adr/0028-mecatequi.md` §5.1.

## `mecatequi-example.yml` — EXAMPLE / TEMPLATE (the escape hatch; not run in this repo)

A **template**, not a live workflow: there is no `mecatequi` label and no secret
configured here, so the file is inert in this repo. It is the canonical
**split-privilege** pattern for running `mecatequi` (the single-shot headless runner,
`cmd/mecatequi`) from Actions. Copy it into your own repo and review before enabling.

Trigger: `issues` (`labeled`) with the `mecatequi` label, OR `issue_comment` (`created`)
whose body mentions `@mecatequi`. The job `if:` additionally requires
`author_association ∈ {OWNER, MEMBER, COLLABORATOR}`.

Two jobs with a hard token boundary — *the step that can write to GitHub never runs agent
code; the step that runs agent code never holds a write token*:

| Job | Permissions | What it does |
|-----|-------------|--------------|
| `implement` | `contents: read` (NO write, NO id-token) | Builds `mecatequi` from source, extracts the UNTRUSTED prompt via `jq` over `$GITHUB_EVENT_PATH` into a file (never an inline `${{ }}`), runs the agent with `--untrusted-prompt` + `--posture auto`, uploads the patch + summary + event log. Its only secret is the LLM key. |
| `publish` | `contents: write` + `pull-requests: write` + `issues: write` | Downloads the artifacts and applies the patch as DATA (`git apply`) → branch → commit → PR, or posts an honest failure comment on a non-clean run. Runs NO agent output as code. |

Three composite actions back both adoption paths: `.github/actions/mecatequi/` (build + run
the binary), `.github/actions/mecatequi-extract-prompt/` (wraps `extract-prompt.sh`), and
`.github/actions/mecatequi-publish/` (wraps `publish.sh`). The two glue scripts live inside
their wrapper actions as their single home (so the reusable workflow can reach them by full
path); `author-gate.sh` stays under `.github/actions/mecatequi/` as an example-only
defense-in-depth illustration. Every event-derived value crosses via `env:`/inputs, never
argv. Full design + the trust model + the `/proc`-exfiltration follow-up:
`docs/adr/0028-mecatequi.md`; the operator walkthrough: `docs/usage.md`.

## References

- GitHub Actions security hardening — <https://docs.github.com/en/actions/security-guides/security-hardening-for-github-actions>
- OIDC in GitHub Actions — <https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect>
- Preventing pwn requests — <https://securitylab.github.com/resources/github-actions-preventing-pwn-requests/>
- Sigstore / cosign — <https://docs.sigstore.dev/cosign/overview/>
- ko — <https://ko.build/>
- SLSA — <https://slsa.dev/>
