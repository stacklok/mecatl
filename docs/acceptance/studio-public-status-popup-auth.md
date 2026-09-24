# Studio public status and popup sign-in — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds a deliberately anonymous BFF contract and changes the browser OIDC callback trust boundary while preserving the authenticated runtime contract.
**Decision record:** [ADR 0357](../adr/0357-studio-anonymous-status-and-popup-login.md)
**Phase:** Studio design parity — sign-in and recovery
**Status:** proposed, 2026-09-24. Based on issue #1845, the #1779 design baseline, ADR 0351, repository behavior, and the directing human's status, fallback, retry, email, and identity decisions.
**Delivery:** Split. The new public and security interfaces need their own Plan / Interface review before implementation; the implementation PR will independently target `main`.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1845](https://github.com/stacklok/mecatl/issues/1845).
**Plan PR:** [#1860](https://github.com/stacklok/mecatl/pull/1860)
**Approved baseline:** absent until the Plan / Interface PR merges

An anonymous browser can distinguish a reachable Studio BFF, an unavailable daemon, and a required sign-in using only a coarse public status response. OIDC login completes in a same-origin popup while the original tab retains its route and draft. Detailed runtime data and feature APIs remain behind the existing session gate.

## Human decisions

- [x] Work class and delivery path. — Decision: Architectural under the security/trust and public API criteria; create ADR 0357 and open an independent Split Plan / Interface PR targeting `main`.
- [x] Public response. — Decision: `GET /api/v1/status` returns exactly `connection: "checking" | "reachable" | "unavailable"` and `signInRequired: boolean`; HTTP success establishes BFF reachability, while the existing auth-session response owns account and email.
- [x] Anonymous outage precedence. — Decision: a daemon authentication rejection proves reachability; a transport failure shows daemon unavailable even if sign-in is also required; a pending observation is checking.
- [x] Blocked or closed popup. — Decision: keep the original tab mounted and offer another user-initiated popup attempt or a manual new-tab sign-in link, then recheck the session on focus; no automatic same-tab redirect or draft serialization.
- [x] Expired-session retry. — Decision: automatically refetch reads after reauthentication, but require explicit user retry of mutations and streams because their outcomes may be uncertain.
- [x] Email source and identity boundary. — Decision: show only an optional verified OIDC ID-token `email` claim; make no userinfo request or scope change; retain the opaque `account` key and keep the raw subject inside BFF-readable sealed data, out of browser responses and audit logs.
- [x] Missing account identity. — Decision: reject an OIDC login without a usable ID-token `sub`, invalidate subjectless legacy OIDC sessions, and clear prior account-scoped browser state if an authenticated response lacks an opaque account key. No accountless OIDC session may render a workspace.
- [x] Existing callback compatibility. — Decision: bind popup mode in the sealed login transaction while keeping the registered callback URL, local alias, validated `return_to` redirect for ordinary login, and ADR 0351's sealed-cookie, SDK, and one-origin boundaries.

## Interface contract

- **gRPC / protobuf:** None — Studio adds no mecatl service, message, field, or generated Go contract.
- **Exported Go APIs / interfaces:** None — the change is confined to Studio's TypeScript browser, BFF, and contract packages.
- **Tool schemas:** None — browser authentication and BFF status add no model-facing tool.
- **CLI / config:** None — no flag or environment variable changes. Continue deriving callback origin and cookie `Secure` from `STUDIO_PUBLIC_URL`; keep the existing OIDC discovery, session-secret, rate-limit, and runtime-mode settings.
- **Events / persistence:** The existing sealed HttpOnly `studio_login` cookie gains an optional popup-mode marker, with absence meaning the current redirect flow; its 10-minute transaction lifetime is unchanged. The existing sealed HttpOnly `studio_access` cookie gains an optional email claim, with absence valid for old sessions and issuers without that claim; its 30-day maximum age and 3800-byte sealed-value cap remain. No new cookie, server-side session store, event, durable draft, or browser account key is added. Popup messages are ephemeral.
- **Security / authority:** `GET /api/v1/status` is the sole new anonymous exception under `/api/v1/*`; it returns no deployment, capability, credential, issuer, account, email, subject, error detail, or raw SDK status. It retains Host validation, security headers, no CORS, and `Cache-Control: private, no-store`. `/api/v1/runtime`, `/api/v1/settings/runtime`, `/api/v1/storage/health`, and every feature route remain session-gated in OIDC mode. An authenticated OIDC session requires a nonempty verified ID-token subject and its opaque account key; subjectless tokens and legacy sessions fail closed. Login keeps Authorization Code + PKCE, sealed cookies, state validation, rate limiting, same-origin mutation checks, and double-submit CSRF. The raw subject remains in BFF-readable sealed credentials and transient server memory; audit events omit it. A popup callback posts only the result enum to its exact origin; the opener verifies origin, source, and active attempt, then validates the session through the BFF. Callback HTML and script carry no code, state, credential, subject, email, or account key, are uncached, and use a no-referrer response policy.
- **Compatibility / migration:** Add `GET /api/v1/status` and optional `email` on authenticated `GET /api/v1/auth/session`; retain `{mode, status, account?}` for the existing modes, but require `account` whenever OIDC status is `authenticated`. Keep the existing opaque account-key derivation. Add optional `flow=popup` to `GET /api/v1/auth/login`; omission retains the ordinary redirect and validated `return_to` behavior. The issuer redirect URI remains `STUDIO_PUBLIC_URL/api/v1/auth/callback` (or the current local `/oauth/callback` alias); a valid popup transaction selects the new HTML callback response. Existing sealed sessions and transactions without the new optional fields remain readable if they have a usable subject; subjectless legacy sessions are invalidated. Regenerate OpenAPI and the browser client from source schemas in the implementation PR. No `/api/v1/runtime` field becomes public.

The new public HTTP response is exactly:

```ts
type PublicStatus = {
  connection: "checking" | "reachable" | "unavailable";
  signInRequired: boolean;
};
```

`GET /api/v1/status` returns `200 application/json` even when the daemon is unavailable. The BFF reads the SDK's already observed connection state without requesting or serializing the compatibility snapshot. `connecting` maps to `checking`; `online`, `unauthorized`, and `incompatible` map to `reachable` as transport facts; `reconnecting`, `offline`, or no runtime map to `unavailable`. `signInRequired` is true only in OIDC mode when this browser has an anonymous or expired session; it is false for an authenticated OIDC session and for `static` or `none` modes. The browser treats a failed status fetch as a BFF outage. `/api/v1/runtime` retains its authenticated boundary and existing `401`/`503` behavior.

`GET /api/v1/auth/session` may add `email?: string` only for an authenticated session with a verified ID-token claim that is a nonempty string of at most 254 UTF-8 bytes without C0 controls or DEL. Ignore an absent or invalid email claim without failing login. A refresh without a new ID token retains the sealed subject and email; a new same-subject ID token replaces the email claim, omitting it when absent or invalid. A refresh with a different verified subject fails closed. Authenticated OIDC responses always include the existing opaque hash of a nonempty issuer subject. A token or legacy sealed session without a usable subject is rejected or invalidated rather than returned as authenticated; the browser defensively clears account-scoped state on any authenticated response without an account key. Neither endpoint nor an audit event returns the raw subject. `GET /api/v1/auth/login?flow=popup&return_to=<same-origin-path>` opens synchronously from the initiating click. The sealed transaction chooses callback rendering; no browser-controlled callback query parameter does. A valid popup callback sends exactly `{type: "studio.auth.result", result: "success" | "failure"}` with `postMessage` to its own origin and closes. Its script is served from the same origin as an external asset under the existing CSP. Missing or mismatched state cannot clear an established session; the original tab can recover even when no callback message arrives.

For #1844, #1845 supplies the public-status query and this banner input and priority order; #1844 owns where the band appears and how it looks:

```ts
type StatusBannerInput = {
  publicStatus?: PublicStatus; // absent while the public fetch is pending
  publicStatusFailed: boolean;
  sessionCheckFailed: boolean;
  authenticated: boolean;
};
```

A failed public fetch shows BFF unavailable. Otherwise `connection: "unavailable"` shows daemon unavailable, even when sign-in is required. A failed session check shows session verification unavailable with an explicit retry. Then `signInRequired` shows popup sign-in and its retry/manual-new-tab recovery; `checking` is neutral. Only an authenticated view may add storage health from its gated route. The public shell and banner remain visible for initial anonymous, checking, and failed-session-check states. An existing workspace stays mounted through a transient session-check failure, with new mutations and streams paused until the session is verified again.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — an anonymous tab gets only connection and sign-in facts

Studio's [BFF boundary](../adr/0351-mecatl-studio-in-repo-web-ui.md) and current [session gate](studio-bootstrap.md) leave `/api/health` public and `/api/v1/runtime` authenticated. The new route gives a signed-out shell enough information to distinguish an outage from authentication without disclosing deployment details.

**Acceptance:**
- AC1.1: an anonymous `GET /api/v1/status` returns exactly the two declared fields with `200`, no-store caching, and the existing Host/CSP/no-CORS policy; a fetch failure is distinguishable from a successful BFF response.
  - verify: vitest:apps/server/src/routes/status.test.ts#YW5vbnltb3VzIHN0YXR1cyBleHBvc2VzIG9ubHkgY29hcnNlIGNvbm5lY3Rpb24gYW5kIHNpZ24taW4gZmFjdHM — `apps/server/src/routes/status.test.ts :: "anonymous status exposes only coarse connection and sign-in facts"`.
- AC1.2: SDK authentication rejection reports `reachable`; connecting reports `checking`; transport loss reports `unavailable` regardless of sign-in need; no compatibility snapshot or credentialed probe is used to answer the public route.
  - verify: vitest:apps/server/src/routes/status.test.ts#c3RhdHVzIHNlcGFyYXRlcyB0cmFuc3BvcnQgb3V0YWdlIGZyb20gYXV0aGVudGljYXRpb24gcmVqZWN0aW9u — `apps/server/src/routes/status.test.ts :: "status separates transport outage from authentication rejection"`.
- AC1.3: anonymous requests still receive `401` from `/api/v1/runtime` and feature/settings/storage routes; authenticated callers retain the full runtime snapshot and storage health remains authenticated.
  - verify: vitest:apps/server/src/app.test.ts#ZGV0YWlsZWQgcnVudGltZSBhbmQgZmVhdHVyZSByb3V0ZXMgcmVqZWN0IGFub255bW91cyByZXF1ZXN0cw — `apps/server/src/app.test.ts :: "detailed runtime and feature routes reject anonymous requests"`.

### Scenario 2 — popup login completes through the established OIDC callback

The [Studio bootstrap contract](studio-bootstrap.md) owns PKCE, cookie sealing, the one callback URL, state validation, and opaque account separation. Popup completion adds a constrained browser message without changing those controls or [ADR 0351's](../adr/0351-mecatl-studio-in-repo-web-ui.md) browser/BFF boundary.

**Acceptance:**
- AC2.1: a sign-in action opens `/api/v1/auth/login?flow=popup` synchronously in a popup; the original tab retains its pathname, search, hash, and in-memory draft while a successful callback completes the session.
  - verify: playwright:apps/web/e2e/auth-recovery.spec.ts#cG9wdXAgc2lnbi1pbiBwcmVzZXJ2ZXMgcm91dGUgYW5kIGRyYWZ0 — `apps/web/e2e/auth-recovery.spec.ts :: "popup sign-in preserves route and draft"`, using #1776's offline browser harness.
- AC2.2: a valid popup transaction returns a CSP-compatible same-origin callback page that posts only the declared result with an exact target origin and closes; the opener accepts only an exact active-popup message from its own origin and verifies the session with the BFF.
  - verify: vitest:apps/server/src/routes/auth.test.ts#cG9wdXAgY2FsbGJhY2sgcmV0dXJucyBhIGNvbnN0cmFpbmVkIENTUC1zYWZlIHJlc3VsdA — `apps/server/src/routes/auth.test.ts :: "popup callback returns a constrained CSP-safe result"`.
  - verify: vitest:apps/web/src/features/auth/auth-recovery.test.tsx#cG9wdXAgbWVzc2FnZXMgcmVxdWlyZSBvcmlnaW4gc291cmNlIGFuZCBhY3RpdmUgYXR0ZW1wdA — `apps/web/src/features/auth/auth-recovery.test.tsx :: "popup messages require origin source and active attempt"`.
- AC2.3: ordinary login still redirects to validated `return_to`; a missing, expired, or mismatched login state sets no new session and does not clear an existing one. The callback keeps sealed cookies, CSRF/origin guards, and audit redaction.
  - verify: vitest:apps/server/src/routes/auth.test.ts#ZGlyZWN0IGNhbGxiYWNrIGtlZXBzIHJlZGlyZWN0IGFuZCBpbnZhbGlkIHN0YXRlIHByZXNlcnZlcyBzZXNzaW9u — `apps/server/src/routes/auth.test.ts :: "direct callback keeps redirect and invalid state preserves session"`.
- AC2.4: `/api/v1/auth/session` adds the exactly validated optional email from a verified ID-token claim only when authenticated, requires the opaque account key for every authenticated OIDC response, and never exposes the raw subject in browser data or audit events; refresh without a new ID token retains the sealed identity and email, a new same-subject token replaces the email claim, and a changed subject fails closed. Missing or empty ID-token subjects reject login, and sealed subjectless legacy sessions are invalidated.
  - verify: vitest:apps/server/src/auth/service.test.ts#dmVyaWZpZWQgZW1haWwgY2xhaW0gc3RheXMgb3B0aW9uYWwgYW5kIHN1YmplY3Qgc3RheXMgc2VhbGVk — `apps/server/src/auth/service.test.ts :: "verified email claim stays optional and subject stays sealed"`.

### Scenario 3 — recovery and banners keep the workspace usable

The current [auth gate](../../apps/web/src/features/auth/auth-gate.tsx) unmounts the workspace when a session disappears, losing the [chat composer's](../../apps/web/src/features/chat/chat-composer.tsx) in-memory draft. #1845 owns recovery state and the banner data contract. #1844 owns the shell band and styling that render it; #1846 owns detailed runtime and settings projections. #1776 supplies the offline browser test infrastructure and `playwright:` resolver for `apps/`.

**Acceptance:**
- AC3.1: blocked, manually closed, openerless, or message-less popups leave the original tab and draft mounted, show retry and a user-initiated new-tab sign-in link with `noopener`, and recheck the session on focus or popup closure before showing a failure.
  - verify: playwright:apps/web/e2e/auth-recovery.spec.ts#YmxvY2tlZCBhbmQgY2xvc2VkIHBvcHVwcyBvZmZlciBhIHJlY292ZXJhYmxlIHNpZ24taW4 — `apps/web/e2e/auth-recovery.spec.ts :: "blocked and closed popups offer a recoverable sign-in"`, with popup blocking and no-opener fixtures.
- AC3.2: `401 unauthenticated` or `401 session_expired` moves the tab into a recoverable sign-in state without navigating away or unmounting its draft; after same-account login, reads refetch, while a mutation or stream resumes only after an explicit user action.
  - verify: vitest:apps/web/src/features/auth/auth-recovery.test.tsx#ZXhwaXJlZCBzZXNzaW9uIHByZXNlcnZlcyBhIGRyYWZ0IGFuZCBuZXZlciByZXBsYXlzIHdyaXRlcw — `apps/web/src/features/auth/auth-recovery.test.tsx :: "expired session preserves a draft and never replays writes"`.
  - verify: playwright:apps/web/e2e/auth-recovery.spec.ts#ZXhwaXJlZCBzZXNzaW9uIHJlcXVpcmVzIGV4cGxpY2l0IHJldHJ5IGZvciBhIHdyaXRl — `apps/web/e2e/auth-recovery.spec.ts :: "expired session requires explicit retry for a write"`, using #1776's offline issuer and daemon fixtures.
- AC3.3: same-account reauthentication preserves the route and draft; a different account or an authenticated response without a usable account key clears user-scoped browser storage, authenticated query data, and the mounted draft before any workspace is rendered for that identity.
  - verify: vitest:apps/web/src/features/auth/auth-recovery.test.tsx#c2FtZSBhY2NvdW50IGtlZXBzIGRyYWZ0IGFuZCBkaWZmZXJlbnQgYWNjb3VudCBjbGVhcnMgaXQ — `apps/web/src/features/auth/auth-recovery.test.tsx :: "same account keeps draft and different account clears it"`.
- AC3.4: the public shell and #1844 banner render for an initial anonymous tab; the declared banner input treats a failed status fetch as BFF unavailable, `connection: unavailable` as daemon unavailable, a failed session check as a retryable verification outage, then `signInRequired` as auth recovery; `checking` is neutral. Anonymous rendering requests no detailed runtime or storage health; storage health is shown only after authentication.
  - verify: vitest:apps/web/src/components/shell/connection-status-banner.test.tsx#cHVibGljIHN0YXR1cyBiYW5uZXJzIHByaW9yaXRpemUgb3V0YWdlcyBiZWZvcmUgc2lnbi1pbg — `apps/web/src/components/shell/connection-status-banner.test.tsx :: "public status banners prioritize outages before sign-in"`.
  - verify: vitest:apps/web/src/components/shell/connection-status-banner.test.tsx#YW5vbnltb3VzIGJhbm5lcnMgbmV2ZXIgcmVxdWVzdCBzdG9yYWdlIGhlYWx0aA — `apps/web/src/components/shell/connection-status-banner.test.tsx :: "anonymous banners never request storage health"`.
  - verify: playwright:apps/web/e2e/auth-recovery.spec.ts#YW5vbnltb3VzIHNoZWxsIHNob3dzIG91dGFnZSBiZWZvcmUgc2lnbi1pbg — `apps/web/e2e/auth-recovery.spec.ts :: "anonymous shell shows outage before sign-in"`, using #1776's offline daemon transport and authentication fixtures.
- AC3.5: a transient `/api/v1/auth/session` check failure never changes a previously mounted workspace into a sign-out frame or discards its route and in-memory draft; it shows the session-verification outage and retry, and pauses new mutations and streams until a successful check. Initial session-check failure keeps the public shell visible with retry and no authenticated data.
  - verify: vitest:apps/web/src/features/auth/auth-recovery.test.tsx#c2Vzc2lvbiBjaGVjayBmYWlsdXJlIGtlZXBzIHRoZSBtb3VudGVkIGRyYWZ0 — `apps/web/src/features/auth/auth-recovery.test.tsx :: "session check failure keeps the mounted draft"`.
  - verify: playwright:apps/web/e2e/auth-recovery.spec.ts#c2Vzc2lvbiBjaGVjayBvdXRhZ2Uga2VlcHMgdGhlIHNoZWxsIGFuZCByb3V0ZQ — `apps/web/e2e/auth-recovery.spec.ts :: "session check outage keeps the shell and route"`, using #1776's offline BFF failure fixture.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Shell band layout, navigation, and responsive styling | #1844 | #1845 supplies the status values, precedence, and auth-recovery actions; #1844 owns placement. |
| Provider/model, build, and detailed runtime schema changes | #1846 | Keep `/api/v1/runtime` and `/api/v1/settings/runtime` authenticated; coordinate any detailed-runtime overlap before either implementation PR changes it. |
| Browser-test infrastructure and general web unit coverage | #1776 | #1845 supplies named auth/status journeys; #1776 adds the offline harness, CI task, and Studio `playwright:` resolver. |
| Full-tab login fallback or persisted composer drafts | Later design decision | Retry and manual new-tab recovery preserve the mounted draft without new persistence. |
| New OIDC scopes, userinfo calls, or issuer-selection controls | Later auth contract | The optional email uses an already issued verified ID-token claim. |

## Definition of done

1. Source schema, generated OpenAPI/browser client, BFF tests, web state tests, and an adversarial security review prove the public-response allowlist, authenticated routes, callback message checks, cookie/CSRF/origin controls, and absence of the raw subject from browser data and audit logs.
2. #1776's offline Playwright harness and Studio proof resolver resolve the named popup, blocked/closed, and expired-session journeys; browser runs exercise desktop and mobile widths without live provider calls. Record route/draft and banner interaction evidence for #1779 review.
3. `task studio:check`, the #1776 browser task, `task docs`, `task site:build`, `task lint`, `task test:race`, and `task ac-trace-strict` pass on the implementation candidate; the offline demo remains green if runtime behavior outside Studio changes.
4. Update the [Studio architecture owner](../architecture.md#mecatl-studio) for implemented behavior and the [Studio deployment guide](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/studio.md) for browser sign-in and recovery in the implementation PR. The Plan / Interface PR keeps intended behavior here and in ADR 0357.
5. The implementation PR links this Plan / Interface PR and its merged commit, reports contract conformance and #1844/#1846 coordination, and passes `/panel-review` without ship blockers. Humans alone merge either PR.

## Deferred decisions and known risks

- The SDK connection status is an observed state; a new outage can appear after its next probe. The banner must not describe `checking` as a confirmed outage.
- One sealed `studio_login` transaction exists per browser cookie jar. Concurrent tabs can replace one another's login state; a mismatched callback fails closed and offers a fresh user-initiated attempt.
- An issuer can sever `window.opener` through browser isolation policy. The callback page offers a manual return instruction, and the original tab rechecks the session on focus.
