# ADR 0252 — Logical-agent JWT-SVID identity projection

- Status: Proposed
- Date: 2026-08-31
- Scope: I2 logical-agent subject, typed authority claim, constrained issuance, and independent verification
- Supersedes: none
- Superseded by: none

## Context

ADR 0251 provides the future combined broker with an explicit SPIFFE trust domain, an ES256/P-256 issuer, a canonical public bundle, an independent verifier, shell-less key custody, and restart-safe rotation. Mecatl already carries a durable `governance.CapabilitySet` and narrows it through the shared delegation seams, but it has no signed representation a downstream broker can independently verify.

The broader identity exploration previously mixed several axes: logical agent definition, runtime occurrence, owner or user, delegation history, run correlation, and workload holder. It also proposed richer resource and chain claims than mecatl can currently enforce without inventing a second policy language. I2 needs one small, honest assertion that I3 can later consume as actor evidence without making history an authorization source or moving user credentials into mecatl.

## Decision

I2 adds one root-internal, typed logical-agent JWT-SVID profile over ADR 0251. It implements constrained issuance and independent typed verification only. It adds no production mint RPC, agent-loop dependency, session persistence, Redis state, user exchange, vMCP route admission, or credential lookup.

### Logical principal

JWT-SVID `sub` identifies the durable logical agent definition workload, not a user, runtime occurrence, run, pod, holder, or broker replica:

```text
spiffe://<trust-domain>/mecatl/agent-definition/v1/<tier>/<slug>--<digest>
```

The closed tiers are `system`, `managed`, `driver`, `user`, and `project`. They are site-defined identity categories, not SPIFFE privilege levels. All are issuance-eligible, but a token may be minted only for an occurrence that trusted composition has authorized to assume the exact resolved definition. Merely naming or loading a definition is insufficient.

The exact definition name is non-empty, valid UTF-8, control-free, and at most 128 bytes. It is not case-folded or Unicode-normalized. A display slug retains lowercase ASCII `a-z0-9`, replaces other runs with `-`, trims them, falls back to `agent`, and is capped at 48 characters. The slug has no security meaning.

The authoritative suffix encodes all 256 bits of SHA-256 as RFC 4648 lowercase Base32 without padding. The hash input is:

```text
ASCII("mecatl-agent-definition-subject-v1")
|| 0x00
|| uint32be(len(tierBytes)) || tierBytes
|| uint32be(len(nameBytes)) || nameBytes
```

The complete URI must satisfy the SPIFFE ID grammar and 2048-byte maximum. Verification recomputes the complete subject from the signed tier and exact name and compares it byte-for-byte. A rename or tier change creates a new principal.

### Typed claim profile

All logical-agent data lives in one collision-resistant public claim:

```text
https://mecatl.dev/claims/logical-agent/v1
```

Its closed object contains exactly:

```text
definition_tier  required closed tier
definition_name  required exact resolved name
instance         optional accountability metadata
tools            required exact current tool names
```

`instance` is omitted when absent, never `null`, valid UTF-8, control-free, and at most 256 bytes. It never affects authorization. A later B4 integration must include it for an ordinary durable session-backed occurrence and may omit it only for an explicit non-session host.

`tools` is an exact, byte-preserving set of mecatl capability names. It has at most 256 entries, each non-empty, valid UTF-8, control-free, and at most 256 bytes, with at most 8 KiB aggregate name bytes. Issuance sorts byte-lexicographically and rejects duplicates; verification rejects non-canonical order, duplicates, or silent normalization. An empty array means no projected tool authority, never “inherit all.”

The v1 object is closed: unknown members and duplicate keys fail. Any additive field or semantic change requires a new major claim URI. Tokens containing multiple logical-agent profile versions fail rather than merge. Generic unrelated JWT claims do not grant authority; duplicate security-relevant top-level members fail.

V1 deliberately omits user/groups, resource restrictions, filesystem/direct-write posture, remaining delegation depth, delegation history, transaction/run correlation, raw arguments/content, downstream credentials, and holder binding.

### One authority proof

The source `governance.CapabilitySet` is authoritative. I2 constructs the requested tool-only projection and calls the existing `CapabilitySet.Contains` predicate. False refuses before signing. I2 defines no subset helper, alias expansion, prefix matching, case folding, normalization, or token-specific authority policy.

The signed tool set may equal or narrow the source tools. I2 makes no claim about the omitted filesystem, direct-write, and delegation-depth dimensions; omission never means unlimited. A verified tool set is authority for that token only and must not be cached or unioned solely by logical subject.

### Constrained issue and verify operations

I2 adds typed siblings within `internal/identityissuer` and a constrained host-level mint method. They share ADR 0251's key custody, bundle, time, audience, and rotation substrate but do not expose arbitrary claims, JOSE headers, key selection, issuer, audience, TTL, timestamps, signing bytes, or caller-provided JWT ID.

Every mint receives a fresh 16-byte cryptographically random `jti`, encoded unpadded base64url. It is correlation metadata, not authority or replay prevention. I2 uses one fixed configured I3 audience and one fixed I1-bounded TTL. Compact tokens remain capped at 16 KiB.

Verification is one profile-specific security decision over the protected header, registered claims, signature, and typed v1 object. It does not verify one representation and then project authority from a second unverified parse. It pins ES256, selects a configured-bundle key by `kid`, rejects duplicate or malformed security fields, validates the local trust domain, exact singleton audience, bounded times, `jti`, closed schema, canonical subject, and canonical tools, and returns:

```text
trust domain
subject
tier
exact definition name
optional instance
fresh sorted unique tool slice
JWT ID
expiry
```

The result carries no raw compact token, generic claims map, or authorization verdict. Downstream policy evaluates a concrete request separately.

### Lifecycle and compatibility

Compact I2 JWTs are ephemeral and never enter session snapshots, event logs, diagnostics, prompts, model-visible results, or distributed broker state. Reminting the same logical definition preserves the subject and same-or-narrower tools while changing times, `jti`, signature, and possibly `kid`. During ADR 0251 rotation overlap, independent verifiers accept old and new unexpired tokens only while their keys remain in the bounded complete bundle.

When I2 is disabled, no I2 validation, issuer lookup, mint, verification, persistence, or diagnostic occurs, and existing stored bytes and agent behavior remain unchanged. When enabled, I2 returns a signed token or an error; it never fabricates an unsigned or anonymous identity.

B4 later owns trusted definition binding, eager invocation after final authority derivation, occurrence identity, restart reminting, and external-boundary outage posture. Current `session.Authority.DefinitionIdentity` is an untyped legacy label and is not sufficient to reconstruct all five tiers; B4 must carry the actual post-resolution typed identity rather than guess from session kind, relationship, model input, or raw agent name.

I3 later receives the I2 JWT-SVID as RFC 8693 `actor_token` with `actor_token_type=urn:ietf:params:oauth:token-type:jwt`, separately authenticates the user and presenter, authorizes the actor/user/client/target combination, and issues a different holder-bound outbound token. I2 does not contain RFC 8693 `act`; in I2 the logical agent is already `sub`.

## Consequences

I3 gains a real independently verifiable logical-agent credential whose current coarse tool authority cannot exceed mecatl's carried source. Project, user, driver, managed, and system definitions with the same display name remain distinct principals, while replicas or occurrences authorized for one definition may share its workload identity.

The token is a bearer credential until expiry. `jti` does not prevent replay, and removal of a runtime tool cannot revoke an already-issued token before its short TTL. I3/B4 must authenticate the presenter, fail closed at external boundaries, and independently resolve resources, routes, user authority, and holder binding.

The profile intentionally cannot express repository, path, argument, purpose, or transaction restrictions. Those require a demonstrated runtime vocabulary and shared containment rule; they are not inferred from tool names or ancestry.

## Superseded exploratory wording

For I2, this ADR supersedes the candidate shapes in `docs/agent-identity-model.md`, `docs/agent-identity-outbound.md`, and `docs/scoped-resource-grants.md` where `sub` identifies a concrete occurrence or where current authority is reconstructed from rich chain/resource claims. Those documents remain broader program context; this ADR is authoritative for the I2 v1 wire profile.

## See also

- [ADR 0251 — identity issuer substrate](./0251-identity-issuer-substrate.md)
- [ADR 0234 — authority evaluator port](./0234-authority-evaluator-port.md)
- [ADR 0027 — cloud-native architecture](./0027-cloud-native.md)
- [Agent identity model](../agent-identity-model.md)
- [Agent identity outbound](../agent-identity-outbound.md)
- [Scoped resource grants](../scoped-resource-grants.md)
