# ADR 0332 — Mecatui local provider enrollment preserves existing credential custody

- Status: Proposed
- Date: 2026-09-12
- Scope: local mecatui provider setup, API-key persistence, provider-default persistence, and passive status

## Context

Mecatl already supports keyed built-in providers, operator-defined providers with `api_key` or
`none` authentication, ToolHive, and identity-bound native endpoint OIDC, including the existing
native login introduced by ADR 0329. A local user can configure supported providers through
environment variables or files, but file-based setup still requires editing `settings.yaml` and
`auth.yaml`. A guided operation improves that path while turning a previously read-only credential
adapter into a writer and creating an unavoidable partial-commit boundary between credentials and
non-secret defaults.

A new vault or alternate precedence would fragment established deployment behavior. Treating two
files as transactionally atomic would be dishonest across supported filesystems. An active status
probe would spend network/provider authority merely to inspect local configuration. Native endpoint
OIDC has encrypted identity-bound custody, ToolHive owns its credential lifecycle, and the existing
`openai-codex` OAuth-shaped auth record is not an API key; none can be folded into a plaintext-key
writer without weakening those boundaries.

## Decision

Add an explicit line-oriented local `mecatui llm setup` operation and enrich no-target passive
`mecatui llm status`; do not add remote enrollment or a TUI/model-driven secret flow. Reuse the
composition-owned provider definitions, credential precedence, resolver, model metadata, and
default selection. API-key mutation is a closed operation for the four keyed built-ins (`openai`,
`anthropic`, `openrouter`, `opencode`) and already-configured custom providers whose resolved auth
method is exactly `api_key`. Preserve unrelated OAuth-shaped records. Native endpoint selection may,
after explicit consent, call the existing host login operation; ToolHive remains an external
handoff. Neither lifecycle consumes or migrates `--auth-file` content.

Keep API keys as owner-only plaintext records in existing `auth.yaml`. Environment credentials
continue to win exactly as they do at startup, including OpenRouter's own-key precedence and its
environment-only `OPENAI_API_KEY` compatibility fallback; custom IDs remain file-only. Before
secret entry, show the provider's static console guidance and disclose that an API/developer key is
distinct from a consumer subscription, same-UID and agent-Shell readability, and possible provider
charges. Use the existing terminal password reader without echo, restore terminal state on every
return supported by it, and reject values over 8 KiB immediately after acquisition. This is an
accepted-value bound, not a false claim that `x/term.ReadPassword` preallocates a bounded buffer.
Never put key material in argv, output, diagnostics, prompts, backups, temp names, or errors.

Introduce one narrow targeted API-key writer beside the strict bounded reader. Its internal API is
context-cancelable and returns one of four stable states: `no_op`, `not_applied`, `durable`, or
`replacement_applied_durability_unknown`. A no-op or fully file-and-directory-synced replacement
returns nil. Every error before successful rename, including a five-second cooperative-lock timeout
or an unknown/mismatched target re-read, is `not_applied`. Every error after successful rename is
`replacement_applied_durability_unknown`; it does not claim rollback, observed final content, or
crash durability. The same outcome vocabulary applies to the focused settings writer.

The credential boundary is the canonical managed parent plus its leaves, not every ancestor and not
an arbitrary filesystem-containment framework. Resolve conventional platform aliases such as
`/home` → `/var/home`; normal existing ancestors, including root-owned `/` and `/home`, need not be
current-UID `0700`. The canonical credential parent must be a current-UID non-link directory at
`0700`; auth and stable lock leaves must be current-UID non-link regular files at `0600`. Anchor
leaf operations to an opened canonical parent and use no-follow opens. The conventional `mecatl`
directory may be created at `0700` below an existing canonical user config directory. An explicit
missing auth file may be created only after path-specific confirmation in an already-existing safe
parent; do not recursively create arbitrary explicit parents. Do not tighten existing modes or
create backups. Reject mutation on platforms that cannot prove these properties.

Under the context-cancelable cooperative lock, re-read and validate current bytes, write and sync a
same-directory owner-only temp, then immediately before rename compare the target's identity and
content with the locked read. A mismatch or inability to compare is `not_applied`. Rename and sync
the directory for a durable outcome. This detects outside changes only up to the comparison; it is
not CAS against arbitrary POSIX writers. A same-UID non-cooperating process can still race between
comparison and rename, and no universal race-free API is invented. Before any prompt, resolve auth
and settings targets to physical paths and reject them if they are the same file.

Persist add/replace as separately confirmed ordered operations: credential first, settings second.
Removal that would strand the active default is the necessary ordering exception: commit a
confirmed replacement default durably first, then remove the old key. There is no portable
multi-file transaction and no retry or blind rollback. Any ambiguity stops subsequent mutation and
optional startup. Reports identify the sanitized operation/provider and its exact state; passive
status/re-read is the remedy. Thus a failed settings invocation after a durable key write cannot be
summarized globally as “default unchanged,” and a failed key removal after a durable default change
must say the default moved while the key may remain or may have been replaced durably-unknown.

No-target status is deterministic, human-oriented output rather than a JSON API. It reports local
configuration, effective and shadowed credential-file source without values/fingerprints, selected
default/model, and `not checked`. Writable keyed rows remain distinct from `none required`, native,
and ToolHive classifications. It performs no network request, refresh, browser launch, paid
inference, provider construction, or credential-store creation. Explicit native and ToolHive status
retain their existing target behavior and reject `--auth-file` rather than silently ignore it.
Dynamic path/provider/model metadata uses existing sanitized projections.

Built-in suggestions remain a bounded friendly projection of existing defaults and catalog metadata,
not another support list. Manual non-empty unverified model selection is always available even when
the catalog has suggestions. Setup does not claim unknown native/custom models are tool-capable.
Aliases resolve through the actual resolver. Optional startup explicitly carries the chosen auth
path and re-reads ordinary composition/settings state; it never uses a stale wizard snapshot,
performs an implicit paid check, or falls back to the conventional auth file behind the operator.

## Consequences

A newcomer can reach a working local session through one explicit CLI flow while deployments keep
the same files, precedence, provider adapters, and model selectors. Existing configuration is not
rewritten by inspection or reuse. Operators receive exact per-operation outcomes when an
environment value shadows a file replacement or only part of an ordered workflow completed.

The plaintext same-UID residual risk remains and is disclosed at entry. The cooperative lock does
not provide atomic exclusion from arbitrary same-UID writers, and post-rename directory-sync failure
has necessarily uncertain crash durability. These are inherited/local-filesystem limitations in the
proposed contract, not detailed risks already approved independently; Plan / Interface PR merge is
the approval checkpoint.

The implementation maintains two focused preserving writers rather than one generic transaction.
Status is passive and cannot prove that a credential or model is accepted. Optional active checks,
first-run offers, OpenRouter OAuth/key minting, and Gemini remain separate: OpenRouter needs its own
PKCE/callback/copy-code security gates, while Gemini needs independent replay-compatibility proof.

## See also

- [ADR 0016](./0016-multi-provider.md) — composition-owned provider registry
- [ADR 0238](./0238-operator-defined-llm-providers.md) — custom providers and `auth.yaml` separation
- [ADR 0329](./0329-native-llm-endpoint-gateway-credentials.md) — existing native encrypted endpoint lifecycle
- [Acceptance plan](../acceptance/mecatui-local-provider-setup.md)
- [Provider architecture](../architecture/providers.md)
