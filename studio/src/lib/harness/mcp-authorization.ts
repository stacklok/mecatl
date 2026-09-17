/**
 * The mid-run MCP browser-authorization controls over the mecatl TypeScript
 * SDK (`session.mcpAuthorization(id)`): the live sign-in URL, and the
 * recheck/cancel control streams that carry the authorization's outcome plus
 * the continuation run when the parked tool call resumes.
 *
 * The run this phase belongs to is PARKED, not live: the daemon closed the
 * prompt stream on `authorization.required`, so the run's own controls
 * (cancel, steer) do not reach it — only these do.
 */

import type { StreamEvent } from "@/features/agent/types";
import { isPendingAuthorizationStatus, translateEvent } from "@/lib/protocol";
import { harness, toHarnessError } from "./sdk";
import { harnessSession } from "./sessions";

/**
 * Reads the live browser URL for a pending authorization
 * (`GET /v1/sessions/{id}/mcp-authorizations/{aid}/presentation`). The URL is
 * only ever fetched at the moment it is opened or copied — it never enters an
 * event, the transcript, or hook state. A 404 carries the daemon's own reason
 * (expired / no longer pending / …) verbatim on the typed HarnessApiError.
 */
export async function fetchMcpAuthorizationUrl(
  sessionId: string,
  authorizationId: string,
  signal?: AbortSignal,
): Promise<string> {
  const session = await harnessSession(sessionId, signal);
  const presentation = await harness(() =>
    session.mcpAuthorization(authorizationId).presentation({ signal }),
  );
  if (!presentation.url) {
    throw new Error(
      "The daemon returned no sign-in URL for this authorization.",
    );
  }
  return presentation.url;
}

/** What a recheck/cancel control stream settled on. */
export interface McpAuthorizationControlOutcome {
  /**
   * The authorization's status as the daemon last reported it on this stream
   * — `pending` when a recheck found the sign-in still incomplete (the stream
   * then carries no continuation and the run stays parked).
   */
  status: string;
  /** True when a continuation run streamed through to its terminal result. */
  sawResult: boolean;
}

/**
 * How long a control stream may go silent before Studio declares it dead —
 * the same bound the prompt relay uses (a continuation run is a normal run
 * whose tool calls may go quiet for a while).
 */
const CONTROL_IDLE_TIMEOUT_MS = 120_000;
const CONTROL_IDLE_MESSAGE = "Mecatl stopped sending updates for two minutes.";

/**
 * How long a control may take to produce its FIRST event. The daemon answers
 * a recheck/cancel synchronously (one status frame, then either the stream
 * closes or the granted continuation starts), so a first event that has not
 * arrived in 10 s means the transport dropped the stream — a stalled
 * port-forward, observed live. Without this bound a lost frame would leave
 * the in-flight guard set and stop the 3 s polling for good (the TUI's
 * `mcpAuthorizationFirstEventTimeout`). It bounds only the first read; a
 * long-running continuation is then governed by the idle timeout.
 */
export const CONTROL_FIRST_EVENT_TIMEOUT_MS = 10_000;
const CONTROL_FIRST_EVENT_MESSAGE =
  "Mecatl did not answer the authorization check within 10 seconds.";

async function readWithIdleTimeout<T>(
  read: Promise<T>,
  onTimeout: () => void,
  timeoutMs = CONTROL_IDLE_TIMEOUT_MS,
  message = CONTROL_IDLE_MESSAGE,
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      onTimeout();
      reject(new Error(message));
    }, timeoutMs);
  });
  try {
    return await Promise.race([read, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

async function relayAuthorizationControl(
  sessionId: string,
  authorizationId: string,
  control: "recheck" | "cancel",
  onEvent: (event: StreamEvent) => void,
  signal: AbortSignal | undefined,
): Promise<McpAuthorizationControlOutcome> {
  const session = await harnessSession(sessionId, signal);
  const handle = session.mcpAuthorization(authorizationId);
  // The stream's own controller lets the idle guard tear the SDK stream down
  // while still honouring the caller's abort.
  const streamAbort = new AbortController();
  const abortStream = () => streamAbort.abort(signal?.reason);
  if (signal?.aborted) abortStream();
  signal?.addEventListener("abort", abortStream, { once: true });
  const idle = () => streamAbort.abort();

  let status = "pending";
  let sawResult = false;
  try {
    const stream =
      control === "recheck"
        ? handle.recheck({ signal: streamAbort.signal })
        : handle.cancel({ signal: streamAbort.signal });
    const events = stream[Symbol.asyncIterator]();
    let first = true;
    for (;;) {
      const next = first
        ? await readWithIdleTimeout(
            events.next(),
            idle,
            CONTROL_FIRST_EVENT_TIMEOUT_MS,
            CONTROL_FIRST_EVENT_MESSAGE,
          )
        : await readWithIdleTimeout(events.next(), idle);
      first = false;
      if (next.done) break;
      for (const translated of translateEvent(next.value, sessionId)) {
        if (
          translated.type === "authorization" ||
          translated.type === "authorization_resolved"
        ) {
          status = isPendingAuthorizationStatus(translated.status)
            ? "pending"
            : translated.status;
        }
        if (translated.type === "run_result") sawResult = true;
        onEvent(translated);
      }
    }
  } catch (error) {
    throw toHarnessError(error);
  } finally {
    signal?.removeEventListener("abort", abortStream);
  }
  // A control stream legitimately ends without a result: a still-pending
  // recheck carries only the status frame, and a terminal status whose
  // continuation could not be attached is settled daemon-side. The caller
  // reads `status`/`sawResult` and decides — nothing here is a truncation.
  return { status, sawResult };
}

/**
 * Asks the daemon to re-inspect the authorization
 * (`POST /v1/sessions/{id}/mcp-authorizations/{aid}/recheck`, no body) and
 * relays every translated event of the outcome stream: the status frame, then
 * — when the sign-in completed — the continuation run through its result.
 */
export function recheckMcpAuthorization(
  sessionId: string,
  authorizationId: string,
  onEvent: (event: StreamEvent) => void,
  signal?: AbortSignal,
): Promise<McpAuthorizationControlOutcome> {
  return relayAuthorizationControl(
    sessionId,
    authorizationId,
    "recheck",
    onEvent,
    signal,
  );
}

/**
 * Abandons the authorization
 * (`POST /v1/sessions/{id}/mcp-authorizations/{aid}/cancel`, no body). The
 * parked tool call records a cancellation and the run usually continues, so
 * the same stream shape applies: the terminal status, then the continuation.
 */
export function cancelMcpAuthorization(
  sessionId: string,
  authorizationId: string,
  onEvent: (event: StreamEvent) => void,
  signal?: AbortSignal,
): Promise<McpAuthorizationControlOutcome> {
  return relayAuthorizationControl(
    sessionId,
    authorizationId,
    "cancel",
    onEvent,
    signal,
  );
}
