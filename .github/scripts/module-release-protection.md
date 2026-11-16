# Release protection settings reference

Use the contributor [module release procedure](../../docs/module-development.md#4-merge-and-release-dependencies-first)
for request, qualification, continuation, rollout, and recovery. This reference
covers settings and credential inspection only. Its commands do not authorize
live settings changes or permission extensions.

## Configuration and read access

Set `RELEASE_APP_ID` (numeric App ID) and `RELEASE_APP_BOT_ID` (numeric bot ID)
for module rollout, alongside the existing `RELEASE_APP_CLIENT_ID`. Before rollout,
root/SDK guards derive both numeric identities from the existing App: JWT-authenticated
`GET /app` binds the configured client ID to its App ID/slug, and public
`GET /users/{slug}[bot]` resolves the bot ID. Configured numeric IDs, when supplied,
must agree. Names or unauthenticated supplied IDs alone are not evidence.

Protected main-only preflights reuse the **existing** environment-scoped
`RELEASE_APP_PRIVATE_KEY` and client ID to sign a five-minute App JWT with OpenSSL
RSA/SHA-256. Key bytes travel over stdin; signing input travels over a pipe. Neither
the key nor JWT appears in command arguments, logs, outputs, artifacts, or durable
files. Only `GET /app` and `GET /repos/{owner}/{repo}/installation` receive the JWT;
redirects are refused. The latter endpoint proves this repository belongs to the
installation. Its account/target/App identities, suspension state, repository
selection and actual installed permissions are checked. The App must already have
Contents/write and Pull requests/write, with no Administration or Workflows rights.
No installation access token is minted during this inspection. There is **no new
human OAuth, user-access-token, or refresh lifecycle**.

For local metadata inspection, use existing human `gh` authentication (or `GH_TOKEN`)
and explicitly supply a short-lived `RELEASE_APP_JWT` through your normal secure
process environment mechanism. Also export the public `RELEASE_APP_CLIENT_ID`.
Do not put credential values in command arguments or shell history. This avoids
copying the App private key into developer checkouts. No App key is needed by
candidate jobs; the key is exposed only to the protected preflight and existing
token-minting action. Post-mint root/SDK writers consume a trusted runner-local
**non-secret** authority record, bound to repository, exact SHA, track and version;
they never load a key/JWT or accept arbitrary ref namespaces.

Routine `check` verifies the main-only environment's custom branch policy, effective
inherited tag rules and all six deployed workflow blobs. It uses `GH_TOKEN`/normal
`gh` authentication for these GETs. `RELEASE_SETTINGS_TOKEN` is an **optional** human
metadata reader override when normal access cannot read the environment or inherited
ruleset details; no secret of that name is required to exist. GitHub may withhold
`bypass_actors` from readers without ruleset write **access**. Missing actors are an
error, not an empty list. If the platform withholds required metadata, module
rollout remains blocked pending separately authorized read access. The checker
reports the failing endpoint; it never grants permissions, assumes protections,
or gives administration to the release App. Job `GITHUB_TOKEN` Actions/write, not
an App permission extension, handles module CI recovery/continuation.

`MODULE_RELEASES_ENABLED != true` stages root/SDK hardening independently: their
existing main-only `release` environment, authenticated App/installation identity,
canonical namespaces and exact human-merged PR provenance still apply, but they do
**not** depend on module-wide settings, reader credentials, numeric identity variables
or module workflow deployment. Enabling the feature activates the complete global
settings preflight for every shared-App path before write-token minting.

Keep the App key **only** in the main-only `release` environment. That environment
must use custom deployment branch policies with exactly `{name: main, type: branch}`;
protected-branches mode, wildcards and a tag named `main` are not equivalent. Preserve
reviewers, wait timers and stronger restrictions. Apply does not rewrite environments.

The explicit rollout-only `audit` additionally inspects secret **names/scope**, never
values: the App key must be environment-scoped and not available to arbitrary
branches through repository/organization secrets. It does not require any metadata
reader secret to exist. This deeper audit can require repository/organization Secrets
read and environment-secret metadata access that normal `GITHUB_TOKEN` lacks. Obtain
separate human authorization if necessary; it is not performed on routine releases.
Complete it (or an equivalent authorized administrative scope audit) before module
enablement. Unknown copies of a key under other names cannot be discovered through
GitHub's API. Runtime `check` is tag/environment/identity configuration evidence,
**not** an attestation that this rollout-only credential-scope audit was performed.

## Inspect, review, apply, verify

With metadata credentials provisioned through the usual secret mechanism, use a
trusted full checkout and export the public `RELEASE_APP_CLIENT_ID` value into the
local process environment (Actions supplies it from repository variables). Run:

```sh
python3 .github/scripts/module-release-protection.py diff --repository OWNER/REPO --app-id APP_ID --bot-id BOT_ID
# Explicit rollout credential-scope audit (may need separately authorized metadata access):
python3 .github/scripts/module-release-protection.py audit --repository OWNER/REPO --app-id APP_ID --bot-id BOT_ID
python3 .github/scripts/module-release-protection.py apply --repository OWNER/REPO --app-id APP_ID --bot-id BOT_ID
# The preceding apply is ALSO a dry run. Only after separate human authorization:
python3 .github/scripts/module-release-protection.py apply --confirm --repository OWNER/REPO --app-id APP_ID --bot-id BOT_ID
python3 .github/scripts/module-release-protection.py check --repository OWNER/REPO --app-id APP_ID --bot-id BOT_ID
```

For confirmed apply only, `GH_TOKEN` must be the **human administrator's** authorized
settings credential. The tool rejects bot users/non-admins. It only adds missing
rulesets and rereads them; it never patches/deletes existing or inherited rulesets,
updates/deletes tags, changes environments, or changes an App's permissions. Repeated
apply is a no-op. Existing managed-name rules that are insufficient require human
inspection/repair, not an automatic replacement. Diff/apply refuse to proceed past
unverifiable App/environment metadata; fix that diagnostic first.

Creation restrictions cover root `v*`, SDK `sdk/typescript/v*`, and every published
Go prefix in `module-publication-policy.json`, with only the verified App Integration
as a standing creation bypass. Separate active update/deletion restrictions have
**no bypass actors**, including no App/admin bypass. Stronger inherited or existing
rules remain effective; an inherited deny-all creation rule is not weakened to
make the App operational. Inherited restrictions can therefore require an admin's
separate rollout investigation even after configuration check succeeds.

The checker proves namespace containment for literal/`*`/`?`/`**` patterns with
GitHub's slash boundary, not just a finite set of sample tag names. Bracket/escape
patterns and rules with exclusions are conservatively not counted as complete
namespace coverage; explicit managed rules can supplement them without weakening
anything. Unsupported inherited source types fail closed and need administrator
inspection/tool support. This is configuration evidence, not a live enforcement
rehearsal or proof of how GitHub will treat an untested credential.

For enablement prerequisites and exceptional administrative recovery, follow the
[owning contributor procedure](../../docs/module-development.md#release-protection-setup).
Offline tests and settings inspection do not prove live enforcement.

The shared-App audit covers `create-release-{pr,tag}.yml`,
`create-sdk-typescript-release-{pr,tag}.yml`, `request-module-release.yml` and
`module-releases.yml`. `release.yml`'s Homebrew App and `mecatequi-reusable.yml`'s
caller-supplied publishing App use different credentials and are not modified.
