/**
 * The ONE daemon client Studio holds: the mecatl TypeScript SDK
 * (`@stacklok-oss/mecatl-sdk`) connected to the same-origin `/api/mecatl`
 * proxy, which injects the daemon credential server-side (rule 3). Every
 * daemon capability the UI surfaces goes through this client — no module
 * under `src/lib/harness` speaks the daemon's HTTP routes by hand.
 *
 * The client is created lazily on first use and kept for the page's lifetime:
 * it owns the compatibility probe, the connection-status heartbeat, and the
 * reconnecting durable watch. `fetch` is resolved per call rather than bound
 * at construction so a test's `vi.stubGlobal("fetch", …)` takes effect on an
 * already-created client.
 */

import {
  AuthenticationError,
  type Client,
  connect,
  MecatlError,
  PromptValidationError,
  ServerError,
} from "@stacklok-oss/mecatl-sdk";

import { HarnessApiError } from "./errors";

/** The proxy path the SDK's `/v1/...` routes are appended to. */
const HARNESS_BASE_URL = "/api/mecatl";

let client: Client | undefined;

/** Returns the shared SDK client, creating it on first use. */
export function getHarnessClient(): Client {
  client ??= connect({
    baseUrl: HARNESS_BASE_URL,
    credentials: "same-origin",
    // Resolved per call, never bound at construction (see the module note).
    fetch: (input, init) => globalThis.fetch(input, init),
  });
  return client;
}

/**
 * Closes and forgets the shared client. Tests call this between cases so a
 * stubbed transport never leaks across them; production never needs to.
 */
export async function resetHarnessClient(): Promise<void> {
  const current = client;
  client = undefined;
  for (const listener of [...resetListeners]) listener();
  await current?.close().catch(() => undefined);
}

const resetListeners = new Set<() => void>();

/**
 * Registers a listener for `resetHarnessClient()`, so a subscription that
 * outlives one call — the connection-status store a banner mirrors — can
 * re-attach to the replacement client instead of staying bound to the
 * forgotten one. Returns the unsubscribe.
 */
export function onHarnessClientReset(listener: () => void): () => void {
  resetListeners.add(listener);
  return () => {
    resetListeners.delete(listener);
  };
}

/**
 * Codes whose raw detail deserves plainer user-facing framing. Kept here,
 * next to the one place SDK errors are translated, so every caller sees the
 * same words.
 */
const codeFraming: Record<string, string> = {
  draining: "The daemon is restarting — try again in a moment.",
  session_leased_elsewhere:
    "Another client is driving this chat right now — try again when its run finishes.",
};

/**
 * The raw RFC 9457 problem body the SDK keeps on an HTTP error's `cause`.
 * Two things live ONLY there: a `code` outside the SDK's registry (the
 * proxy's `oidc_login_required` / `oidc_session_expired` /
 * `oidc_idp_unavailable`, a 403's code — the SDK narrows those to
 * "unknown"), and a 401's own words (the SDK collapses every 401 into
 * `AuthenticationError("Authentication failed")` before reading the body).
 */
function problemFromCause(error: MecatlError): {
  code: string;
  message: string;
} {
  const problem = error.cause;
  if (!problem || typeof problem !== "object") return { code: "", message: "" };
  const body = problem as Record<string, unknown>;
  const text = (key: string) =>
    typeof body[key] === "string" ? (body[key] as string) : "";
  return {
    code: text("code"),
    message: text("detail") || text("title") || text("error"),
  };
}

/**
 * Translates an SDK failure into Studio's typed `HarnessApiError`, which UI
 * flow control branches on by stable machine `code` (never message prose).
 *
 * - A `ServerError` carries the daemon's RFC 9457 problem: its HTTP status and
 *   `code` cross verbatim (`stale_run_control`, `proposal_conflict`, …). A
 *   code the SDK does not register (the proxy's `oidc_*` codes) is read off
 *   the raw problem body instead of degrading to "".
 * - An `AuthenticationError` (every HTTP 401) is typed on the problem body's
 *   own `code` and words — the proxy's "sign in again from Settings" copy,
 *   the daemon's `unauthenticated` — so the offline banner can name the
 *   cause; the SDK's generic "Authentication failed" is the fallback.
 * - Any other `MecatlError` (transport, protocol, unsupported feature,
 *   incompatible server) keeps the SDK's own code under status 0 — the UI
 *   still branches on `code`, and the message stays the SDK's own words.
 * - A non-SDK error is returned unchanged.
 */
export function toHarnessError(error: unknown): unknown {
  if (error instanceof HarnessApiError) return error;
  // A prompt the SDK refused to BUILD (a media part the session's modalities
  // reject, an oversized part, a bad MIME type) never left the browser: it
  // keeps its typed `reason` so the composer can say exactly what to fix.
  if (error instanceof PromptValidationError) return error;
  if (error instanceof ServerError) {
    const raw = problemFromCause(error);
    const code = error.code === "unknown" ? raw.code : error.code;
    return new HarnessApiError(
      error.status ?? 0,
      code,
      codeFraming[code] ?? error.message,
    );
  }
  if (error instanceof AuthenticationError) {
    const raw = problemFromCause(error);
    const code = raw.code || error.code;
    return new HarnessApiError(
      error.status ?? 401,
      code,
      codeFraming[code] ?? (raw.message || error.message),
    );
  }
  if (error instanceof MecatlError) {
    return new HarnessApiError(error.status ?? 0, error.code, error.message);
  }
  return error;
}

/**
 * Runs one SDK call and rethrows any failure as Studio's typed error. The
 * shape every harness module uses: `return harness(() => client.x.y(...))`.
 */
export async function harness<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (error) {
    throw toHarnessError(error);
  }
}

/**
 * Narrows an SDK "the server lacks this route" failure — a 404 problem or an
 * `unsupported_feature` — so a caller can degrade to "older daemon" instead
 * of erroring. Every other failure is rethrown, already translated.
 */
export function isUnsupportedByDaemon(error: unknown): boolean {
  if (error instanceof HarnessApiError) {
    return error.status === 404 || error.code === "unsupported_feature";
  }
  if (error instanceof ServerError) return error.status === 404;
  return error instanceof MecatlError && error.code === "unsupported_feature";
}
