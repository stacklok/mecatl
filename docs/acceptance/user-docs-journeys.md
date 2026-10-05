# Public documentation journeys — acceptance plan

Mecatl's documentation should make three kinds of work easy to distinguish:
using an agent, operating the service that runs it, and building an application
with its engine or APIs. These readers share the same product behavior, but they
do not share the same setup tasks. A terminal user connecting to a team server
should not first have to learn how to deploy it. An operator securing a service
should not have to navigate a builder's extension-point catalogue. A Go builder
should encounter the engine as a library before being asked to choose a server.

The restructure establishes those journeys while keeping one canonical home for
shared capabilities. It also finishes the editorial consolidation already begun
on the restructuring branch. Moving pages into better categories is necessary,
but the work succeeds only when their content follows the reader's task:
duplicate explanations disappear, unique constraints survive, and detailed
operations remain available in focused guides.

This is a targeted restructure, not a rewrite of every page. The local tutorial,
cloud-native explanation, grouped capability index, generated references, and
much of the operational material are useful foundations. The remaining problems
are sequencing, ownership, and packaging of that material.

## Product and audience model

**People using Mecatl** start with their goal: work with an agent locally or
connect to a deployment supplied by their operator. `mecatui` is the current
terminal client, so its task pages belong under **Use Mecatl**. Naming the section
after the goal avoids requiring readers to recognize a binary name before they
know what it does. There is no need for a client subgroup until another client
has enough public material to justify one.

**Operators** own the service, credentials, durable state, access policy, and
execution environment. `mecated` is the lightweight starting point for a separate
server. The journey should then lead quickly to `mecak8s` for shared teams,
including small teams: its supplied durable state, coordination, routing, and
scaling defaults are the reason to choose it. The question is what the operator
needs to own and maintain, not simply whether they already want Kubernetes.
Shared deployments assume a trusted team. Documentation must explain current
ownership and placement controls without implying hostile tenant or per-user
filesystem isolation.

**Builders** start with the importable Go engine. Their sequence is a first
agent, the engine/session/run/environment model, production embedding, extension
points, and the compatibility promise. The TypeScript SDK follows as the client
application path, with direct gRPC or HTTP/SSE integration and CI automation
available as distinct workflows. Exact Go symbols belong on pkg.go.dev; this
site explains how to assemble and use them.

Studio is intended to be a first-class client for knowledge workers, but its external docs
serve operators. They explain evaluation, deployment, access, configuration,
upgrades, and troubleshooting. End-user guidance belongs in the product. Trimming
the existing external walkthroughs must preserve guidance continuity rather than
leave users with an instruction to wait for a future manual.

## Navigation and orientation

The introduction should first explain what Mecatl is and how its parts relate:
one agent loop with separately owned clients, execution, tools, model access,
identity, and durable state. Readers can use that loop through a client, operate
it as a service, or embed it in an application. Local and shared forms use the
same core. Only then should the introduction route readers to their journey.

The cloud-native explanation is part of product orientation, not builder-only
material. Keep its current architecture and explicitly separated roadmap. Link
it from moments where readers experience the separation: connecting a remote
client, deploying disposable workers, and deciding what an embedding application
must supply. Its strongest next step is the Kind tutorial's pod-replacement
exercise, which demonstrates session continuity rather than merely describing it.

The intended rendered structure is:

```text
Introduction
What is a cloud-native harness?
Use Mecatl
  Get started locally
  Connect to a remote deployment
  Work in the TUI, sessions, commands, and memory
  Customize and troubleshoot
  Install and verify the client
Deploy and operate
  Operating overview: mecated to shared-team mecak8s
  Try Mecatl on Kubernetes
  Run mecated and operate one instance
  Deploy mecak8s and maintain the deployment
  Configuration, access, execution, storage, and observability
  Deploy Studio
Build with Mecatl
  Go engine: tutorial → concepts → embedding → extension points → compatibility
  TypeScript SDK
  Direct API clients
  CI integration
Capabilities
  Get oriented
  Sessions and models
  Agent behavior and context
  Security and execution
  Runtime behavior
Reference
  Configuration and server CLI
  gRPC behavior and schema
  HTTP/SSE and TypeScript SDK
  Go API on pkg.go.dev
```

Keep the navbar's small, deliberate set: Get started, Cloud-native harness,
Deploy, and Build, plus the existing community links. Capabilities and Reference
remain easy to reach through the sidebar and contextual links; they do not need
extra primary CTAs.

Preserve the homepage's product statement, educational CTA, immediate local-use
CTA, and prominent Kubernetes action. Below that orientation, route readers to
Use Mecatl, Deploy and operate, and Build with the Go engine. Replace the current
“Four deployment options” framing: a terminal client, Kubernetes service, CI
runtime, and embedded library answer different questions. The Kubernetes action
should land on the evaluation tutorial, with production deployment as its next
step, rather than sending a new reader into the full operational runbook.

Directory roots can remain `mecatui/`, `operating/`, `building/`, `features/`, and
`reference/`. Rendered labels express the reader model. Renaming those roots
would add URL work without improving these journeys.

## Baseline and existing work

The implementation evidence is `docs/user-docs-journey-restructure` at
`7a74b706d330b9d8c5f7a055527286a91617bf6c`. This plan is prepared against
`main` at `39dcc11ac97569e0889a7da2416400e48bea9df6`.
The branch already contains useful work. Its snapshot establishes what can be
retained; implementation must reconcile later upstream changes and recheck release availability.

The restructuring branch already separates operators from builders, introduces
grouped Capabilities, relocates the cloud-native explanation and client
installation, removes the What you get category, moves embedding and transport
choice into Building, expands the introduction, and adds redirects. Preserve and
reconcile that work with current main rather than repeating a mechanical move.

Remaining work covers remote-client sequencing, the operator progression and
runbook splits, a coherent Go track, preservation of unique merged content,
availability corrections, Studio scope, homepage routing, and reference gaps.

## Reader outcomes — six acceptance scenarios

### Scenario 1 — readers enter the right journey

The introduction and cloud-native explanation already have much of the right
shape on the restructuring branch. Preserve that orientation. The remaining
work is to align homepage routing and contextual links with it, so readers can
move from understanding the product to experiencing it without another decision
tree. The [public authoring contract](../../user-docs/_README.md) and
[repository requirements](../../AGENTS.md#change-routing) govern placement and verification.

**Acceptance:**
- AC1.1: The sidebar exposes Use Mecatl, Deploy and operate, and Build with Mecatl as peers; grouped Capabilities and Reference are reachable without expanding the primary navbar CTA set.
  - verify: inspection — rendered sidebar, navbar, and section indexes establish reader reachability and ordering.
- AC1.2: Orientation explains client, engine, execution, and durable-state relationships before routing; the homepage uses the approved audience treatment and the Kubernetes CTA reaches the Kind tutorial.
  - verify: inspection — follow homepage and introduction links in a rendered preview and compare them with the resolved human decision.
- AC1.3: The cloud-native explanation distinguishes current architecture from roadmap and links to the session pod-replacement exercise; remote, Kubernetes, and embedding guides link back at relevant moments.
  - verify: inspection — follow both directions of these links and verify roadmap wording against current shipped behavior.

### Scenario 2 — local and remote users complete client tasks

The local tutorial is the clearest existing journey: one installation choice,
a visible first result, and links to deeper tasks. Preserve that happy path and
keep installation alternatives in a focused guide.

The remote guide still reverses the primary task. It begins with running
`mecated` on the same machine, requiring a server install, provider key, and
workspace before explaining an operator-provided connection. Lead instead with
the address, authentication method, and optional CA material the operator gives
the user. Explain the server-owned workspace and model access, then show token
and OIDC connection. Keep the two-process exercise as optional material in the
server journey or a separate tutorial.

TUI login and broker-catalogue inspection currently embedded in the Kubernetes
runbook are client tasks. Their complete workflows belong here; operators need
configuration and a link to what their users do. Follow the
[local tutorial](../../user-docs/mecatui/getting-started.md) and the
[repository's public documentation boundary](../../AGENTS.md#change-routing).

**Acceptance:**
- AC2.1: Local first use chooses one recommended installation path; alternatives and archive verification remain a focused client guide rather than a universal prerequisite.
  - verify: inspection — follow the tutorial and installation links without entering operator navigation.
- AC2.2: Remote connection starts with an operator-provided address, authentication, and optional CA material; it requires no server installation, provider key, or local server workspace. A two-process exercise is optional operator/tutorial material.
  - verify: inspection — follow token and OIDC connection paths from client navigation and inspect prerequisites.
- AC2.3: Client guidance owns TUI login and broker-catalogue inspection; server runbooks link to it rather than repeat those workflows.
  - verify: inspection — compare the client and operator treatments for ownership and unique deployment-specific content.

### Scenario 3 — operators find focused deployment and maintenance tasks

The restructuring branch gives operators a top-level home, but its overview
still presents `mecated`, `mecak8s`, and CI as parallel deployment options. Its
chooser still asks about Kubernetes without persistent volumes. Replace those
routers with a concise operating-model comparison and the intended progression
from a lightweight server to the supplied shared-team deployment. CI and Go
embedding should be contextual links into Building.

The server pages need a workflow split, not simply shorter files. The Kubernetes
page combines startup, a draft execution provider, topology, identity, TLS, MCP,
trusted context, failover, and scaling. The daemon page mixes its on-ramp with an
exhaustive flag inventory, custom multi-replica composition, and spawned-daemon
hosting. Preserve the operational depth while making a reader's next task easy
to find.

For `mecated`, keep the current entry URL as the lightweight on-ramp and add
`operating/mecated/secure-and-expose.md`, `configure-providers-and-storage.md`,
and `operate-instance.md`. Custom multi-replica composition remains secondary to
`mecak8s` in the instance guide. Move spawned-daemon hosting to builder/client
integration. For `mecak8s`, keep its supported deployment entry and add
`operating/mecak8s/state-and-execution.md`, `identity-and-client-access.md`,
`observe-and-troubleshoot.md`, and `scale-recover-and-upgrade.md`. Place global
MCP and trusted-context mounting with deployment configuration, gateway identity
with access, and event follower sizing with observation. These names are proposed
for review; the final split should follow tasks rather than source subsystems.

The shared CLI surface also needs a home. Prefer a generated server CLI reference
that distinguishes `mecated` and `mecak8s`; a curated reference is an explicit
alternative for review. Keep flags needed by each workflow in its guide. Do not
remove the current inventories until their unique defaults and constraints have
a verified destination.

Release availability and experimental status are different questions. The public
native-provider instructions explicitly describe a draft candidate, and the
microVM page includes Darwin source qualification and `.scratch` development
commands. Verify current delivery evidence, retain supported operating procedures,
and keep contributor qualification in its confirmed internal owner. Do not move
an unreleased procedure into a new public page and call the problem solved.

Studio's page likewise needs an audience correction: retain a short explanation
of what users receive and a complete operator journey. Its detailed chat,
appearance, and review-settings walkthroughs can be trimmed when essential
replacement guidance is accessible in the product.

Use the [existing deployment material](../../user-docs/building/deployment/index.md),
[native execution contract](native-kubernetes-execution.md), and
[repository boundaries](../../AGENTS.md#implementation-boundaries) to preserve operational facts.

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

The branch moves embedding and transport choice out of Deployment, and the
builder overview leads with Go. Navigation still lacks a coherent Go track,
however, and the former engine/session concept page now redirects to an embedding
guide that does not replace its clear explanation of the objects a builder holds.
Restore that conceptual sequence rather than treating the redirect as proof that
its knowledge survived.

The production embedding guide should build on the first-agent tutorial, focusing
on lifecycle, persistence, adapters, and integration choices. It still repeats
substantial minimum wiring and mixes optional reflection, monorepo development,
and guardrail contracts. Give unique integration detail its appropriate owner.
Similarly, retain the public compatibility promise while moving snapshot updates,
architecture-gate maintenance, and intentional-break contributor procedures out
of the external API-stability page.

Place TypeScript after the Go sequence. Keep transport choice distinct from exact
wire reference, make CI integration discoverable as a builder workflow, and keep
the clone-based offline demo optional. Add direct pkg.go.dev destinations in the
Go track and Reference rather than maintaining another symbol catalogue.

Availability corrections must use delivery evidence. The published SDK v0.4.0
exports Deno, although the connection guide still calls it unreleased and
excluded from v0.1.0. Correct the stale statement; do not retire released material.
The [engine compatibility promise](../../engine/COMPATIBILITY.md) and
[repository dependency rules](../../AGENTS.md#implementation-boundaries) remain unchanged.

**Acceptance:**
- AC4.1: A named Go track leads from first agent to engine/session/run/environment concepts, embedding, extension points, compatibility, and pkg.go.dev; SDK and remote API integration follow it. The offline demo is optional.
  - verify: inspection — traverse the rendered builder track and compare restored concept coverage with the deleted baseline page.
- AC4.2: Embedding builds on the tutorial; custom-policy and guardrail implementation belongs in extension guidance, and repository snapshot maintenance and development procedures belong in contributor owners.
  - verify: inspection — compare full source and destination treatments to preserve unique integration constraints without repeating the tutorial.
- AC4.3: CI integration is discoverable under Building; spawned-daemon lifecycle belongs to client integration. Deno documentation reflects published package exports rather than the stale v0.1.0 exclusion.
  - verify: inspection — verify release metadata and package exports, then follow CI, local-daemon, and Deno documentation links.

### Scenario 5 — consolidation preserves one complete shared explanation

The branch removes the What you get category and relocates much of its content
to Capabilities. That establishes the right ownership, but removal alone does
not demonstrate consolidation. Compare both former treatments and preserve their
unique constraints. The permissions merge is a concrete warning: a short
evaluator summary survives, but Cedar setup and its verified-ownership requirement
need a complete destination. The deleted engine concepts need the same attention.

One canonical shared page should explain each capability. Persona pages can show
the controls their readers use and then link to it; custom Go interfaces and
operator-specific setup remain valid separate tasks. Consolidate provider setup
without erasing credential-custody differences between local use, Kubernetes
Secrets, workload identity, and CI secrets. Split model routing from selection
because aliases, task slots, delegation, and routing diagnosis form a substantial
workflow of their own.

Keep MCP runtime/configuration and OAuth/credential guidance complementary, with
clear ownership rather than a forced giant merge. Soul belongs with skills and
commands; memory should link to it. Scheduled tasks retain claiming, recovery,
and delivery limits in one place. Observability has two legitimate reader needs:
shared runtime behavior and resilience belong in Capabilities, while endpoint
setup, collection, and deployment diagnosis belong in Operating.

The [authoring contract](../../user-docs/_README.md) should describe these owners,
and [repository review requirements](../../AGENTS.md#change-routing) require
preserving verified knowledge rather than merely reducing source lines.

**Acceptance:**
- AC5.1: Every former What you get topic has a verified owner. The merge audit accounts for unique concepts, constraints, availability, and operational examples, including delegated authority and Cedar setup/ownership requirements; removed pages have semantic redirects.
  - verify: inspection — compare both source treatments topic by topic with final owners and the route ledger, rather than relying on file removal or line counts.
- AC5.2: Provider setup, permission semantics, schedule claiming/recovery, soul, MCP configuration/credentials, and identity have one complete shared treatment. Model routing is distinct from selection; operator credential custody and custom interfaces remain audience-specific.
  - verify: inspection — cross-read each owner and its supporting pages for duplicate complete explanations and missing unique constraints.
- AC5.3: Shared resilience remains discoverable in Capabilities while operators find collection/setup in Operating; the authoring contract names the final owners, availability/roadmap boundary, CI placement, Studio scope, and generated reference ownership.
  - verify: inspection — compare the authoring contract with the rendered structure and page responsibilities.

### Scenario 6 — published readers retain reachable destinations

The branch already preserves many capability URLs with explicit slugs and adds
redirects for moved deployment and capability content. Retain that work. A better
sidebar does not require renaming every published URL, and a generic hub redirect
is not an adequate replacement for a specific explanation.

For each split or merge, record the old route, semantic destination, and affected
anchors. Follow the old link as well as the new journey. A production build can
catch broken links, but it cannot prove that a redirect helps the reader or that
the surviving page preserves the former content. Preview URL checks, an orphan
audit, and manual reader journeys complement the mechanical gates.

The [published URL policy](../../website/AGENTS.md#published-url-changes) and
[documentation verification requirements](../../AGENTS.md#change-routing) apply
throughout the restructure, including retired Brood Box discovery. Its public
retirement does not authorize removing internal dependencies or release artifacts.

**Acceptance:**
- AC6.1: Every public page is reachable from the sidebar or an intentional contextual link. Each changed published route resolves to its preserved URL or semantic redirect destination; redirect chains, inbound anchors, and sidebar positions are accounted for.
  - verify: inspection — audit the route ledger against slugs and Vercel rules, audit orphan pages and sidebar collisions, and verify old and canonical URLs on a Vercel preview.
- AC6.2: Generated references remain current and source-generated; current behavior claims are verified against shipped surfaces. No published task depends on draft engineering qualification or a speculative capability.
  - verify: inspection — check source/generator ownership and release evidence; run applicable SDK/reference freshness checks and the production site build.

## Implementation sequence and review checkpoints

Start by reconciling the restructuring branch with current main and comparing
removed or merged content against its destination. Record page dispositions and
URL/anchor mappings before further moves. This first checkpoint protects unique
knowledge and avoids treating previous branch work as either disposable or complete.

Next finish orientation and remote-client sequencing. Then establish the operator
progression and approved runbook split, keeping the CLI reference replacement
alongside the daemon trim. Finish the Go track and focused capability merges,
including model routing, before the final Studio, homepage, and reference pass.
Studio trimming remains dependent on guidance continuity, not the timing of
unrelated navigation work.

Review local use, remote use, lightweight `mecated`, shared-team `mecak8s`, Go
embedding, TypeScript integration, and CI as actual linked paths. For consolidation,
review topic coverage and unique constraints; raw line counts are not a success
metric. Keep implementation changes reviewable by topic and preserve the existing
branch's useful work.

## Page disposition summary

| Owner | Content and remaining disposition |
|---|---|
| `intro.md`, `cloud-native-harness.md` | Preserve component orientation and the clearly separated roadmap. Add concise current-architecture comparisons and contextual links; avoid a second deployment router. |
| `mecatui/` | Label Use Mecatl. Keep local tutorial and installation alternatives. Refocus `remote-servers.md` on operator-provided access; relocate TUI login and broker inspection here. |
| `operating/index.md` | Own the lightweight mecated to shared-team mecak8s progression and concise operating-model comparison. Absorb `choose-deployment.md` and redirect it to the overview. |
| `operating/mecated.md`, `operating/mecak8s.md` | Keep stable entry URLs and split operational tasks as described in the operator scenario. CLI inventories move only when a complete reference exists. |
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

## Human decisions

- [ ] Approve the target ownership and page-disposition contract, including Use Mecatl, CI under Building, Brood Box public retirement, and stable capability URLs.
- [ ] Approve the proposed operator-guide split and page names in the operator journey.
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

Implementation is complete when the existing restructuring work is reconciled
with current main and all six reader journeys have been reviewed. The
implementation PR records topic-preservation and URL-audit evidence, links the
approved plan commit, and reports conformance or separately approved amendments.

Run `task docs`, `task site:build`, `git diff --check`, applicable SDK/CLI freshness
checks, and `task ac-trace-strict` for the landed candidate. Independently review
content ownership and reader journeys. Runtime gates apply if implementation
scope expands into runtime code; editorial acceptance uses inspection rather
than invented Go tests.

## Known risks

Large moves can obscure lost constraints: compare both source pages before deletion.
Release status can drift: verify it at implementation time, especially native
execution, Darwin, and SDK runtimes. Static link checks do not establish reader
success or live redirect behavior: retain rendered journey and preview URL checks.
Any newly discovered material decision requires an explicit plan amendment.

## Review status

**Contract:** human-reviewed/v2
**Work classification:** Bounded — establish reader journeys, canonical documentation ownership, and published URL continuity without changing runtime architecture.
**Decision record:** None — this is an editorial and navigation contract; runtime, API, deployment, and trust-boundary decisions remain unchanged.
**Phase:** Public documentation consolidation
**Status:** draft, 2026-10-05. Remaining documentation restructure; open choices are recorded in Human decisions.
**Delivery:** Split. Review the remaining scope and content destinations before implementation.
**Expected tasks:** Deferred to orchestration after human decisions are resolved.
