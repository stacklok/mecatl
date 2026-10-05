# Public documentation journeys — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — establish reader journeys, canonical documentation ownership, and published URL continuity without changing runtime architecture.
**Decision record:** None — this is an editorial and navigation contract; runtime, API, deployment, and trust-boundary decisions remain unchanged.
**Phase:** Public documentation consolidation
**Status:** draft, 2026-10-05. Combined assessment of the two IA proposals and the existing restructuring branch; open choices are recorded below.
**Delivery:** Split. Review the remaining scope and content destinations before implementation.
**Expected tasks:** Deferred to orchestration after human decisions are resolved.

Readers can use Mecatl, operate a shared deployment, or build with its public APIs
without crossing into another audience's setup instructions. Shared behavior has
one canonical explanation; exact contracts remain reference material.

This plan captures the strongest combined proposal rather than either assessment
verbatim. It uses plan A's audience and ownership model, supplemented by plan B's
provider consolidation, model-routing split, server CLI reference, contextual
cloud-native links, and relocation of client tasks from operator runbooks.

## Baseline and existing work

The implementation evidence is `docs/user-docs-journey-restructure` at
`7a74b706d330b9d8c5f7a055527286a91617bf6c`. The assessments used `main` at
`b6784c42e7e2f4fca2b31fa1638a0c7472320b2c`; this plan is prepared against
`main` at `39dcc11ac97569e0889a7da2416400e48bea9df6`.
These are source snapshots, not claims of deployed behavior or current release availability.

The restructuring branch already separates operators from builders, introduces
grouped Capabilities, relocates the cloud-native explanation and client
installation, removes the What you get category, moves embedding and transport
choice into Building, expands the introduction, and adds redirects. Preserve and
reconcile that work with current main rather than repeating a mechanical move.

Remaining work covers remote-client sequencing, the operator progression and
runbook splits, a coherent Go track, preservation of unique merged content,
availability corrections, Studio scope, homepage routing, and reference gaps.
The snapshot is evidence only: no implementation or approval is implied by this plan.

Unchecked decisions keep this plan draft. Plan approval requires these choices to
be resolved in this document; implementation must not decide them implicitly.

## Human decisions

- [x] Overall route and record type — Decision: use a Bounded acceptance plan and separate Plan / Interface PR, with no ADR; the user authorized this route on 2026-10-05.
- [ ] Approve the target ownership and page-disposition contract below, including Use Mecatl, CI under Building, Brood Box public retirement, and stable capability URLs.
- [ ] Approve the proposed operator-guide split and page names under Scenario 3.
- [ ] Choose the server CLI reference strategy. Recommendation: generate `reference/server-cli.md` from trusted flag registration/help, distinguish binary-specific flags, and add a read-only freshness check; keep current flag inventories until the replacement is verified.
- [ ] Confirm the internal homes and current delivery status of native Kubernetes and Darwin qualification material. Recommended owners are the existing native-execution acceptance plan and microVM architecture guide; public procedures remain only for verified released paths.
- [ ] Approve Studio's transition: retain a short capability overview plus operator guidance externally; ensure replacement guidance for essential user tasks is accessible before removing their only usable instructions; a separate product follow-up may defer the trim but does not satisfy guidance continuity.
- [ ] Approve the homepage treatment: preserve the hero and small CTA set, route Kubernetes evaluation to the Kind tutorial, and replace the mixed deployment cards with audience routing below product orientation.

## Interface contract

- **gRPC / protobuf:** None — documentation explains existing services and wire behavior; messages, routes, and protocol semantics do not change.
- **Exported Go APIs / interfaces:** None — public Go symbols and compatibility guarantees are unchanged; symbol lookup links to pkg.go.dev.
- **Tool schemas:** None — tool names, arguments, results, and execution behavior are unchanged.
- **CLI / config:** None — existing flags, keys, defaults, and precedence remain unchanged. A documentation generator may read existing registration/help; it must not modify command behavior. Its strategy requires the human decision above.
- **Events / persistence:** None — session, event, ownership, and storage contracts remain unchanged.
- **Security / authority:** None — docs accurately describe existing trusted-team, ownership, placement, permission, and filesystem boundaries; no stronger isolation is introduced or promised.
- **Compatibility / migration:** Preserve existing published URLs with explicit slugs where ownership remains clear. For every moved or retired published route, map the old URL to its semantic destination in `website/vercel.json`. Update inbound anchors, preserve necessary anchor compatibility, and verify old and canonical routes. Directory names remain `mecatui/`, `operating/`, `building/`, `features/`, and `reference/`; navigation labels carry the audience language. No runtime data migration occurs.

## Target ownership and page dispositions

The rendered order is Introduction, Cloud-native harness, Use Mecatl, Deploy and
operate, Build with Mecatl, Capabilities, and Reference. No speculative client
subgroup is added. Keep Get started, Cloud-native harness, Deploy, and Build as
the primary navbar links, with GitHub and Discord retained.

| Owner | Content and remaining disposition |
|---|---|
| `intro.md`, `cloud-native-harness.md` | Preserve component orientation and the clearly separated roadmap. Add concise current-architecture comparisons and contextual links; avoid a second deployment router. |
| `mecatui/` | Label Use Mecatl. Keep local tutorial and installation alternatives. Refocus `remote-servers.md` on operator-provided access; relocate TUI login and broker inspection here. |
| `operating/index.md` | Own the lightweight mecated to shared-team mecak8s progression and concise operating-model comparison. Absorb `choose-deployment.md` and redirect it to the overview. |
| `operating/mecated.md`, `operating/mecak8s.md` | Keep stable entry URLs and split operational tasks as specified below. CLI inventories move only when a complete reference exists. |
| `operating/settings.md`, `session-storage-operations.md` | Keep configuration-plane and local-storage procedures; trim repeated provider setup while retaining deployment-specific credential custody. |
| `operating/studio.md` | Refocus on evaluation, image verification, deployment, authentication, configuration, upgrade, and troubleshooting after the guidance-continuity decision. |
| `building/go/` | Named Go track: tutorial, concepts, embedding, extension points, compatibility, and pkg.go.dev. Preserve existing tutorial/embedding/extension URLs using slugs when moving into this track. Keep the offline demo optional after the first-agent tutorial. |
| `building/typescript-sdk/`, `building/grpc-http.md` | SDK follows Go. Transport choice remains client integration, separate from exact wire reference. Move spawned-daemon hosting detail here or into the existing local-daemon guide. |
| `building/ci/mecatequi.md` | Move CI integration from operators; retain CI-specific secret handling and redirect its old routes. |
| `features/` | Keep grouped Capabilities and existing slugs. Own shared agent loop, tools, permissions, schedules, memory, MCP, identity, and availability; persona pages introduce and link. |
| `features/sessions/choose-models.md` | Own model selection and shared provider setup. Extract aliases, slots, task routing, delegated choices, and routing diagnosis into `features/sessions/model-routing.md`. |
| `features/security-and-execution/mcp-client.md`, `mcp-oauth-and-credentials.md` | Complementary owners: runtime/configuration and credential/enrollment behavior. Remove duplicated complete explanations; retain cross-links. |
| `features/runtime/observability-and-resilience.md`, `operating/observability.md` | Separate shared diagnostic/resilience behavior from operator endpoint, collection, and deployment setup. Preserve the existing operator URL. |
| `reference/` | Exact configuration, wire and SDK contracts; add Go API lookup and the selected server CLI reference. Generated pages remain source-generated. |
| Brood Box `operating/mecatui.md` | Retire public discovery and redirect to Use Mecatl or client installation based on its reader intent; retain internal dependencies, images, and release artifacts. |
| Native execution and Darwin qualification | Retain only release-verified operator instructions publicly. Keep contributor qualification evidence in the confirmed internal owner; do not bulk-copy development history. |

Before implementation, resolve proposed operator names below and record a route
ledger in the implementation PR or ignored run state. The ledger includes every
keep/move/merge/split/retire action, final destination, published slug, redirect,
and changed inbound anchor. It is not a new public documentation page.

## In scope — six scenarios, in implementation order

### Scenario 1 — readers enter the right journey

Follow the [public authoring contract](../../user-docs/_README.md) and
[site navigation rules](../../website/AGENTS.md).

The [repository documentation requirements](../../AGENTS.md#change-routing)
require canonical ownership and applicable documentation verification.

**Acceptance:**
- AC1.1: The sidebar exposes Use Mecatl, Deploy and operate, and Build with Mecatl as peers; grouped Capabilities and Reference are reachable without expanding the primary navbar CTA set.
  - verify: inspection — rendered sidebar, navbar, and section indexes establish reader reachability and ordering.
- AC1.2: Orientation explains client, engine, execution, and durable-state relationships before routing; the homepage uses the approved audience treatment and the Kubernetes CTA reaches the Kind tutorial.
  - verify: inspection — follow homepage and introduction links in a rendered preview and compare them with the resolved human decision.
- AC1.3: The cloud-native explanation distinguishes current architecture from roadmap and links to the session pod-replacement exercise; remote, Kubernetes, and embedding guides link back at relevant moments.
  - verify: inspection — follow both directions of these links and verify roadmap wording against current shipped behavior.

### Scenario 2 — local and remote users complete client tasks

Retain the happy path in the [local tutorial](../../user-docs/mecatui/getting-started.md).

The [repository documentation requirements](../../AGENTS.md#change-routing)
require canonical ownership and applicable documentation verification.

**Acceptance:**
- AC2.1: Local first use chooses one recommended installation path; alternatives and archive verification remain a focused client guide rather than a universal prerequisite.
  - verify: inspection — follow the tutorial and installation links without entering operator navigation.
- AC2.2: Remote connection starts with an operator-provided address, authentication, and optional CA material; it requires no server installation, provider key, or local server workspace. A two-process exercise is optional operator/tutorial material.
  - verify: inspection — follow token and OIDC connection paths from client navigation and inspect prerequisites.
- AC2.3: Client guidance owns TUI login and broker-catalogue inspection; server runbooks link to it rather than repeat those workflows.
  - verify: inspection — compare the client and operator treatments for ownership and unique deployment-specific content.

### Scenario 3 — operators find focused deployment and maintenance tasks

Use the [deployment documentation](../../user-docs/building/deployment/index.md)
and [native execution plan](native-kubernetes-execution.md) as baseline evidence.

Proposed split: keep `operating/mecated.md` as the lightweight on-ramp and use
`operating/mecated/secure-and-expose.md`, `configure-providers-and-storage.md`,
and `operate-instance.md` for sustained operation. Custom multi-replica
composition remains secondary to mecak8s and belongs in `operate-instance.md`.
Keep `operating/mecak8s.md` as the supported deployment entry and add
`operating/mecak8s/state-and-execution.md`, `identity-and-client-access.md`,
`observe-and-troubleshoot.md`, and `scale-recover-and-upgrade.md`.
Global MCP and trusted-context mounting belong with configuration in
`state-and-execution.md`; gateway identity belongs with identity and access.
These page names require approval above, not inference during implementation.

The [repository documentation requirements](../../AGENTS.md#change-routing)
require canonical ownership and applicable documentation verification.

**Acceptance:**
- AC3.1: The operator overview presents mecated as a lightweight start and mecak8s as the recommended shared-team destination, including small teams. CI and embedding are contextual links to Building, not peer long-running deployments.
  - verify: inspection — follow both operator paths and verify the concise comparison replaces the duplicated decision tree.
- AC3.2: Supported prerequisites, startup, identity/TLS, storage/execution, observation, scaling, failover, recovery, and upgrades are discoverable through the approved workflow split. The CLI reference preserves all current unique flag information and binary-specific differences before inventories are removed.
  - verify: inspection — compare source guides and flag registration/help against destinations; run a generator freshness check if generation is selected, or verify curated reference completeness against current help if it is not; follow each operator workflow in the rendered site.
- AC3.3: Shared-deployment guidance describes trusted teams and current ownership, placement, and filesystem limits. Public tasks contain only verified released procedures; internal qualification retains its confirmed owner.
  - verify: inspection — compare claims with current composition, release artifacts, architecture, and the resolved material-retirement decisions.
- AC3.4: Studio externally serves operators and has a short client-capability overview; necessary user guidance has an accessible home under the approved transition.
  - verify: inspection — follow deployment/access/troubleshooting tasks and account for each removed user workflow's replacement guidance.

### Scenario 4 — builders progress through Go, SDK, and CI integration

Preserve the external compatibility promise in
[engine compatibility](../../engine/COMPATIBILITY.md).

The [repository documentation requirements](../../AGENTS.md#change-routing)
require canonical ownership and applicable documentation verification.

**Acceptance:**
- AC4.1: A named Go track leads from first agent to engine/session/run/environment concepts, embedding, extension points, compatibility, and pkg.go.dev; SDK and remote API integration follow it. The offline demo is optional.
  - verify: inspection — traverse the rendered builder track and compare restored concept coverage with the deleted baseline page.
- AC4.2: Embedding builds on the tutorial; custom-policy and guardrail implementation belongs in extension guidance, and repository snapshot maintenance and development procedures belong in contributor owners.
  - verify: inspection — compare full source and destination treatments to preserve unique integration constraints without repeating the tutorial.
- AC4.3: CI integration is discoverable under Building; spawned-daemon lifecycle belongs to client integration. Deno documentation reflects published package exports rather than the stale v0.1.0 exclusion.
  - verify: inspection — verify release metadata and package exports, then follow CI, local-daemon, and Deno documentation links.

### Scenario 5 — consolidation preserves one complete shared explanation

Apply the [documentation review rules](../development-process.md#documentation-change-review).

The [repository documentation requirements](../../AGENTS.md#change-routing)
require canonical ownership and applicable documentation verification.

**Acceptance:**
- AC5.1: Every former What you get topic has a verified owner. The merge audit accounts for unique concepts, constraints, availability, and operational examples, including delegated authority and Cedar setup/ownership requirements; removed pages have semantic redirects.
  - verify: inspection — compare both source treatments topic by topic with final owners and the route ledger, rather than relying on file removal or line counts.
- AC5.2: Provider setup, permission semantics, schedule claiming/recovery, soul, MCP configuration/credentials, and identity have one complete shared treatment. Model routing is distinct from selection; operator credential custody and custom interfaces remain audience-specific.
  - verify: inspection — cross-read each owner and its supporting pages for duplicate complete explanations and missing unique constraints.
- AC5.3: Shared resilience remains discoverable in Capabilities while operators find collection/setup in Operating; the authoring contract names the final owners, availability/roadmap boundary, CI placement, Studio scope, and generated reference ownership.
  - verify: inspection — compare the authoring contract with the rendered structure and page responsibilities.

### Scenario 6 — published readers retain reachable destinations

Follow the [published URL policy](../../website/AGENTS.md#published-url-changes).

The [repository documentation requirements](../../AGENTS.md#change-routing)
require canonical ownership and applicable documentation verification.

**Acceptance:**
- AC6.1: Every public page is reachable from the sidebar or an intentional contextual link. Each changed published route resolves to its preserved URL or semantic redirect destination; redirect chains, inbound anchors, and sidebar positions are accounted for.
  - verify: inspection — audit the route ledger against slugs and Vercel rules, audit orphan pages and sidebar collisions, and verify old and canonical URLs on a Vercel preview.
- AC6.2: Generated references remain current and source-generated; current behavior claims are verified against shipped surfaces. No published task depends on draft engineering qualification or a speculative capability.
  - verify: inspection — check source/generator ownership and release evidence; run applicable SDK/reference freshness checks and the production site build.

## Out of scope

- Runtime behavior, public APIs, CLI/config semantics, persistence, deployment defaults, and stronger tenant isolation.
- Removing Brood-derived runtime dependencies, binaries, images, charts, or release jobs.
- New Studio product guidance implementation; record any necessary product follow-up separately.
- A first-party generated Go symbol reference, renamed directory roots, or a manual sidebar.
- Promise of roadmap delivery dates, exhaustive rewriting, and raw line reduction as a success metric.
- Implementation, publication of public content, or merging this PR during plan preparation.

## Definition of done

Plan preparation is complete when this draft is indexed, passes the acceptance-plan
checker and `task docs`, and is available in a draft Plan / Interface PR. Resolve
every Human decisions item before marking it proposed; human merge supplies the
approved baseline.

Implementation is complete when:

1. Reconcile the existing restructuring work with current main, stage only intended changes, and preserve unrelated work.
2. Complete all six scenario inspections and record evidence in the implementation PR, including the topic-preservation and URL audits.
3. Run `task docs`, `task site:build`, `git diff --check`, applicable SDK/CLI freshness checks, and `task ac-trace-strict` for the landed candidate. Runtime gates apply if implementation scope expands into runtime code; documentation-only changes do not require invented Go tests.
4. Independently review content ownership and reader journeys. The implementation PR links the merged plan and approved commit and reports conformance or separately approved amendments.

## Known risks

Large moves can obscure lost constraints: compare both source pages before deletion.
Release status can drift: verify it at implementation time, especially native
execution, Darwin, and SDK runtimes. Static link checks do not establish reader
success or live redirect behavior: retain rendered journey and preview URL checks.
Any newly discovered material decision requires an explicit plan amendment.
