# Module publication policy reference

[`module-publication-policy.json`](module-publication-policy.json) is the
hand-maintained release-policy registry. It adds publication eligibility, tag
namespaces, and verification metadata to the module membership in
[`go.work`](../../go.work). Dependency relationships come from each module's
`go.mod`; exact release versions come from the requested batch.

Use the contributor [module release procedure](../../docs/module-development.md#4-merge-and-release-dependencies-first)
for preview, request, publication, and recovery.

## Format

The file is a JSON object with these fields. Its format is defined by the Python
release tooling; there is no separate JSON Schema document.

| Field | Type | Meaning |
| --- | --- | --- |
| `schema` | Integer | Policy format version; must be `1`. Independent of module versions and API compatibility majors. |
| `namespace` | String | Go module-path base. Each module's declared identity must equal this value plus its directory, except root, which uses the base alone. |
| `modules` | Object | Map of repository-relative module directories to policy entries. `"."` identifies root. Keys must match the candidate's `go.work` membership exactly. |

Each module entry has these fields:

| Field | Type | Required | Meaning and enforcement |
| --- | --- | --- | --- |
| `role` | String | Always | Classification from the role table below. Unknown roles fail validation. |
| `tag_prefix` | String | Published roles | Prefix before the version, for example `engine/` produces `engine/v0.5.0`. Must match the module directory plus `/`, with any terminal Go major directory such as `/v2` removed. Used to derive tags and protection patterns. |
| `policy` | String | Published roles | Repository-relative owning compatibility document. Its existence is checked and its path is shown for review; its prose is not interpreted as an automated SemVer decision. |
| `smoke` | String | Published roles | Go fixture filename under `module-consumers/`, beside this policy. Its existence is checked; public proof runs it as `smoke_test.go` in a fresh consumer module. |
| `changelog` | String | Optional | Repository-relative owning changelog. Qualification requires a recognized classification heading when supplied. API snapshot changes also require a change to this file with an API-change classification. |
| `api` | String | Optional | Repository-relative API snapshot directory. Changes under it trigger the API/changelog checks and require at least a minor version increase over the previous release. Human review still owns breaking-change and major-version judgment. |

Roles determine eligibility and describe the module's purpose:

| Role | Eligible for publication | Meaning |
| --- | --- | --- |
| `public` | Yes | Public Go module. |
| `generated` | Yes | Generated Go module. |
| `support` | Yes | Transitive implementation module. Public proof uses a same-repository-path consumer so Go `internal` imports are legitimate; publication does not make internal packages a public extension API. |
| `application` | No | Root application release track. |
| `runtime-artifact` | No | Runtime/artifact module outside independent Go publication. |
| `test` | No | Test-only module. |

The role labels do not select separate release pipelines for generated and public
modules. Published roles use the shared qualification and proof machinery, with
the support consumer-path exception above. Omit publication fields from excluded
entries: the protection checker collects every entry containing `tag_prefix`.

## Tooling consumers

- [`module-release.py`](module-release.py) validates membership, identities, roles,
  tag prefixes, and required files. Preview reports the owning policy and proposed
  dependency-pin updates; qualification performs standalone and API/changelog
  checks; public proof exercises the selected consumer fixture against the exact
  downloaded version. Engine qualification also runs its existing API freshness
  gate. Unknown workspace members and stale policy entries fail validation.
- [`module-release-github.py`](module-release-github.py) uses that validation for
  reviewed requests and release transitions. It includes the owning policy,
  changelog, and API paths when detecting relevant source changes that need human
  review.
- [`module-release-protection.py`](module-release-protection.py) derives protected
  Go tag patterns from `tag_prefix`, alongside separate application and SDK
  patterns. Policy changes can therefore require live protection changes.

The policy and fixtures load beside the running trusted release scripts; module
manifests and source load from the exact candidate commit. The candidate does not
supply a replacement policy to the running controller.

## Maintenance

Maintainers edit this file in a normal reviewed source PR when adding, removing,
or moving a workspace module, changing publication eligibility, or moving its
compatibility document, changelog, API snapshots, or fixture. The file is not
generated and does not change for routine version releases.

Keep policy entries consistent with `go.work` and module identities. For a newly
publishable module, include its compatibility document and consumer fixture in
the change. Verify classification with `module-release.py inventory --candidate
<FULL_MAIN_COMMIT_SHA>` as described in the contributor procedure, and exercise
policy changes with `task test:module-release-integration`.

Before publishing under a new tag prefix, review the effective protection diff,
apply any separately authorized settings changes, and verify coverage and the
required enforcement rehearsal under the
[release protection setup](../../docs/module-development.md#release-protection-setup).
Merging a policy edit alone does not install GitHub rulesets.
