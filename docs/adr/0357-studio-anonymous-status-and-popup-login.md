# ADR 0357 — Studio exposes coarse status and completes browser login in a popup

- Status: Proposed
- Date: 2026-09-24
- Scope: Studio's anonymous BFF response, browser OIDC callback, and session identity projection.

## Context

Studio serves its browser and API from one origin. The BFF holds the credential in sealed HttpOnly cookies and calls mecatl through the published SDK, as [ADR 0351](./0351-mecatl-studio-in-repo-web-ui.md) requires. Its detailed `/api/v1/runtime` response includes deployment and capability information and requires a browser session when OIDC is active. The current full-tab login redirects away from Studio, discarding drafts held in React component state. The auth gate also prevents a signed-out browser from seeing the connection banner, so a daemon outage resembles a sign-in problem.

Making the detailed runtime response public would disclose more deployment information than a signed-out browser needs. Studio's current content security policy permits same-origin scripts and blocks inline scripts. A callback message alone cannot establish authentication: another window can send a message, and a popup may close or lose its opener before delivery.

## Decision

Expose `GET /api/v1/status` without a session. Its entire successful JSON response is `connection: "checking" | "reachable" | "unavailable"` and `signInRequired: boolean`. An HTTP response proves the BFF is reachable. The connection value projects only the SDK's observed connection state; it neither runs a credentialed probe nor reads the compatibility snapshot. `unauthorized` counts as reachable because a daemon authentication rejection proves transport reachability. A transport failure counts as unavailable and takes precedence over sign-in in the banner. `incompatible` also counts as reachable at this coarse transport boundary; compatibility diagnosis remains behind authenticated routes. Serve the per-browser response without caching under the existing Host allowlist, security headers, and no-CORS policy. Keep `/api/v1/runtime`, feature routes, deployment data, and capabilities authenticated.

Open OIDC login synchronously from a browser action in a popup. A `flow=popup` login query is bound inside the existing sealed `studio_login` transaction. Keep the registered callback URL `STUDIO_PUBLIC_URL/api/v1/auth/callback` and the local `/oauth/callback` alias. A callback with a valid popup transaction returns a same-origin page whose external script works under Studio's content security policy. It posts only `{type: "studio.auth.result", result: "success" | "failure"}` to its opener with an exact same-origin target, then attempts to close. The opener accepts the message only from its active popup and its own origin, then fetches the BFF session again before treating the browser as signed in. A login without `flow=popup` retains its validated `return_to` redirect. A blocked, closed, or openerless popup leaves a manual new-tab or retry path in the original tab; login never requires a full-tab redirect.

Add an optional `email` to the existing authenticated `/api/v1/auth/session` response only when a verified OIDC ID token supplies a nonempty string of at most 254 UTF-8 bytes without C0 controls or DEL. Ignore an absent or invalid claim without failing login. The BFF retains an accepted email in the sealed access cookie and through refresh without a new ID token. A new same-subject ID token replaces the email claim, omitting it when absent or invalid; a different verified subject fails closed. Every authenticated OIDC response requires the existing opaque `account` key derived from a nonempty verified ID-token `sub`; reject a login without it and invalidate subjectless legacy sessions. The browser also clears user-scoped storage, query data, and any mounted draft if an accountless authenticated response reaches it. The raw OIDC subject remains in BFF-readable sealed credentials and transient server memory; browser-readable JSON, messages, URLs, storage, and audit events omit it. No extra issuer request or scope is introduced.

The public shell and banner render even for an initial anonymous session. A failed public-status fetch means BFF unavailable; an observed daemon outage takes priority over sign-in, followed by a failed session check, sign-in need, and neutral checking. The storage-health route is queried only after authentication. When a session expires, keep the current route and mounted in-memory draft while the user signs in again. A transient session-check failure also retains an already mounted workspace, shows a retryable verification outage, and pauses new mutations and streams until the session is checked again. After the same account returns, refetch reads. Require an explicit user retry for mutations and streams because their result may be uncertain. If a different account signs in, clear the prior account's browser data and query cache before showing its workspace.

## Consequences

The BFF gains one narrowly public route and an additive session field; generated OpenAPI and browser clients describe both. The status value is an observed connection state, so it can lag an outage until the SDK reports one. The sealed-cookie session model, PKCE transaction, CSRF check, origin and Host guards, and rate limit remain in force. The callback page needs a same-origin external script, a no-store response, and a no-referrer policy so an authorization code in its URL is not forwarded. Failure to deliver a popup message does not strand the user because the original tab rechecks its session on focus and offers another user-initiated login.

The single `studio_login` cookie continues to allow one current login transaction per browser cookie jar. Competing popup attempts can invalidate the older transaction; state validation fails closed and the affected tab can retry. Shell layout and banner styling are coordinated with #1844. Detailed runtime and settings projections remain owned by #1846. Browser journey infrastructure is coordinated with #1776.

## See also

- [Studio architecture](../architecture.md#mecatl-studio)
- [Studio public status and popup auth acceptance plan](../acceptance/studio-public-status-popup-auth.md)
- [ADR 0351 — Studio BFF and published SDK boundary](./0351-mecatl-studio-in-repo-web-ui.md)
- [Issue #1845](https://github.com/stacklok/mecatl/issues/1845)
