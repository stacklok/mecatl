/**
 * Sessions and runs over the mecatl TypeScript SDK: creation, inventory,
 * transcripts, the prompt/retry streams, and the run controls (approve,
 * cancel, steer, compact). No daemon route is spelled here — the SDK owns the
 * wire; this module owns the UI-facing shapes and Studio's error typing.
 */

import {
  audioPart,
  type Client,
  imagePart,
  type PromptInput,
  ProtocolError,
  type Run,
  type PromptPart as SdkPromptPart,
  ServerError,
  type Session,
  SessionBusyError,
  type SessionSnapshot,
} from "@stacklok-oss/mecatl-sdk";
import type { StreamEvent } from "@/features/agent/types";
import {
  type SessionInventoryPage,
  type SessionPermissionMode,
  type SessionSummary,
  type SessionTranscript,
  sessionInventoryFromResponse,
  sessionPermissionModeFromSdk,
  sessionPermissionModeToSdk,
  sessionTranscriptFromSdk,
  translateEvent,
} from "@/lib/protocol";
import { HarnessApiError } from "./errors";
import { getHarnessClient, harness, toHarnessError } from "./sdk";

// ── Session handles ─────────────────────────────────────────────────────────

/**
 * SDK Session handles, one per session id, so a run control does not
 * re-fetch the session snapshot on every call. Handles belong to the client
 * that minted them: a reset client (tests) drops the whole cache.
 */
let handleOwner: Client | undefined;
const handles = new Map<string, Promise<Session>>();

function handleCache(): Map<string, Promise<Session>> {
  const client = getHarnessClient();
  if (handleOwner !== client) {
    handles.clear();
    handleOwner = client;
  }
  return handles;
}

/**
 * Returns the cached SDK handle for a session, fetching it (one GET of the
 * snapshot, which also seeds the SDK's media-capability gate) the first time.
 */
export function harnessSession(
  sessionId: string,
  signal?: AbortSignal,
): Promise<Session> {
  const cache = handleCache();
  let handle = cache.get(sessionId);
  if (!handle) {
    handle = harness(() =>
      getHarnessClient().sessions.get(sessionId, { signal }),
    );
    cache.set(sessionId, handle);
    handle.catch(() => {
      if (cache.get(sessionId) === handle) cache.delete(sessionId);
    });
  }
  return handle;
}

/** Adopts a handle the SDK just minted (create / fork) into the cache. */
function adoptSession(session: Session): Session {
  handleCache().set(session.id, Promise.resolve(session));
  return session;
}

/** Drops a session's cached handle (after delete, or a stale-handle fault). */
function forgetHarnessSession(sessionId: string): void {
  handleCache().delete(sessionId);
}

/**
 * Runs an operation that starts a run on the session handle. An SDK handle
 * refuses a second concurrent run (`SessionBusyError`); a handle left busy by
 * an abandoned stream is replaced by a fresh one and the call retried once.
 */
async function withRunnableSession<T>(
  sessionId: string,
  operation: (session: Session) => Promise<T>,
): Promise<T> {
  const session = await harnessSession(sessionId);
  try {
    return await operation(session);
  } catch (error) {
    if (!(error instanceof SessionBusyError)) throw error;
    forgetHarnessSession(sessionId);
    return operation(await harnessSession(sessionId));
  }
}

// ── Creation ────────────────────────────────────────────────────────────────

/**
 * Creates a harness session. Session placement is server-owned (ADR 0291):
 * the create body carries no workspace — the browser never needs to know the
 * server's paths.
 *
 * `mode` is accepted at creation directly, so an accept-edits draft needs no
 * create-then-setMode dance.
 */
export async function createHarnessSession(
  mode: "default" | "plan" | "accept_edits" = "default",
  options?: { modelId?: string; providerId?: string; signal?: AbortSignal },
): Promise<string> {
  const session = await harness(() =>
    getHarnessClient().sessions.create(
      {
        mode: sessionPermissionModeToSdk(mode),
        // Omitted entirely on auto-routing so the daemon's own selection
        // applies. The daemon requires provider_id whenever model_id is set
        // (a bare model is ambiguous across providers).
        ...(options?.modelId
          ? {
              modelId: options.modelId,
              ...(options.providerId ? { providerId: options.providerId } : {}),
            }
          : {}),
      },
      { signal: options?.signal },
    ),
  );
  if (!session.id) throw new Error("harness returned no session id");
  return adoptSession(session).id;
}

/**
 * The parent session was mid-run: the daemon refuses to fork history from a
 * running/awaiting source (HTTP 412). Thrown as its own type so the thread UI
 * can say "wait for the current response" instead of a generic failure.
 */
export class ThreadSourceBusyError extends Error {
  constructor(detail: string) {
    super(detail || "The source session is still running.");
    this.name = "ThreadSourceBusyError";
  }
}

/** Forks `sourceSessionId` (`POST /v1/sessions/{id}/fork`), typing the 412. */
async function forkHarnessSession(
  sourceSessionId: string,
  options: { title?: string; modelId?: string; providerId?: string },
  signal?: AbortSignal,
): Promise<string> {
  let session: Session;
  try {
    session = await getHarnessClient().sessions.fork(
      sourceSessionId,
      {
        ...(options.title ? { title: options.title } : {}),
        ...(options.modelId
          ? { modelId: options.modelId, providerId: options.providerId }
          : {}),
      },
      { signal },
    );
  } catch (error) {
    if (error instanceof ServerError && error.status === 412) {
      throw new ThreadSourceBusyError(error.message);
    }
    throw toHarnessError(error);
  }
  if (!session.id) throw new Error("harness returned no session id");
  return adoptSession(session).id;
}

/**
 * Continues an existing chat on a different model. The daemon fixes a
 * session's provider/model at create, so a switch is a FORK: a new session
 * seeded from the source's history on the picked model, carrying the
 * source's title. A running/awaiting source answers 412
 * (ThreadSourceBusyError). Model omitted = the daemon's own routing/default.
 */
export async function forkHarnessSessionToModel(
  sourceSessionId: string,
  model: { modelId: string; providerId: string } | null,
  title: string,
  signal?: AbortSignal,
): Promise<string> {
  return forkHarnessSession(
    sourceSessionId,
    { title, ...(model ? model : {}) },
    signal,
  );
}

/**
 * Creates the daemon session backing a message thread: a fork of the parent
 * whose conversation is seeded from the parent's history, titled so the
 * sidebar reads "Thread: …". A running/awaiting parent answers 412, surfaced
 * as ThreadSourceBusyError.
 */
export async function createThreadHarnessSession(
  parentSessionId: string,
  title: string,
  signal?: AbortSignal,
): Promise<string> {
  return forkHarnessSession(parentSessionId, { title }, signal);
}

// ── Prompt / retry streams ──────────────────────────────────────────────────

/**
 * How long a live stream may go silent before Studio declares it dead. Two
 * minutes comfortably exceeds a slow tool call's quiet stretch while still
 * catching a daemon that went away without closing the socket.
 */
const STREAM_IDLE_TIMEOUT_MS = 120_000;

const STREAM_IDLE_MESSAGE = "Mecatl stopped sending updates for two minutes.";
const STREAM_TRUNCATED_MESSAGE =
  "The connection closed before Mecatl returned a final result.";

async function readWithIdleTimeout<T>(
  read: Promise<T>,
  onTimeout: () => void,
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      onTimeout();
      reject(new Error(STREAM_IDLE_MESSAGE));
    }, STREAM_IDLE_TIMEOUT_MS);
  });
  try {
    return await Promise.race([read, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

/** One staged media attachment, as the composer stages it (base64 inline). */
export interface PromptPart {
  kind: "image" | "audio";
  mime_type: string;
  /** Standard base64 inline bytes. */
  data: string;
}

/** Standard base64 → bytes, browser-safe (no Buffer). */
function base64ToBytes(data: string): Uint8Array {
  const binary = atob(data);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

function toSdkPart(part: PromptPart): SdkPromptPart {
  const options = { bytes: base64ToBytes(part.data), mimeType: part.mime_type };
  return part.kind === "audio" ? audioPart(options) : imagePart(options);
}

/** Text plus staged media as one SDK prompt (a media-only prompt is legal). */
function toPromptInput(
  text: string,
  parts: readonly PromptPart[],
): PromptInput {
  if (parts.length === 0) return text;
  return [{ kind: "text", text }, ...parts.map(toSdkPart)];
}

/** Per-stream hooks a caller may observe beyond the translated events. */
export interface StreamHooks {
  /** Fires once the daemon has accepted the run and named it (ADR 0249). */
  onRunStarted?: (runId: string) => void;
}

/**
 * Streams one prompt turn, invoking `onEvent` per translated event.
 *
 * Two guards make a dead run fail loudly instead of hanging as "Done.":
 * each read races the idle timeout (which also aborts the SDK stream, so the
 * handle is released), and a stream that closes without a terminal `result`
 * throws — the daemon always ends a run with one.
 */
export async function streamHarnessPrompt(
  sessionId: string,
  text: string,
  parts: PromptPart[],
  onEvent: (event: StreamEvent) => void,
  signal?: AbortSignal,
  hooks?: StreamHooks,
): Promise<void> {
  const prompt = toPromptInput(text, parts);
  await relayRun(
    sessionId,
    (session, runSignal) => session.run(prompt, {}, { signal: runSignal }),
    onEvent,
    signal,
    hooks,
  );
}

/**
 * Drives the failed-step retry (`POST /v1/sessions/{id}/retry`, ADR 0239):
 * no request body — the daemon re-drives the recorded failed step itself, so
 * nothing is re-sent — and the run streams exactly like a prompt's. An
 * ineligible session answers 409 with the stable code
 * `failed_step_retry_ineligible` (a HarnessApiError here).
 */
export async function retryHarnessRun(
  sessionId: string,
  onEvent: (event: StreamEvent) => void,
  signal?: AbortSignal,
  hooks?: StreamHooks,
): Promise<void> {
  await relayRun(
    sessionId,
    (session, runSignal) => session.retry({}, { signal: runSignal }),
    onEvent,
    signal,
    hooks,
  );
}

/** The shared run relay: start, translate every event, idle timeout, and the
 *  terminal-result guard — one discipline for prompt and retry. */
async function relayRun(
  sessionId: string,
  start: (session: Session, signal: AbortSignal) => Promise<Run>,
  onEvent: (event: StreamEvent) => void,
  signal: AbortSignal | undefined,
  hooks: StreamHooks | undefined,
): Promise<void> {
  // The run's own controller lets the idle guard tear the SDK stream down
  // (releasing the handle) while still honouring the caller's abort.
  const runAbort = new AbortController();
  const abortRun = () => runAbort.abort(signal?.reason);
  if (signal?.aborted) abortRun();
  signal?.addEventListener("abort", abortRun, { once: true });
  const idle = () => runAbort.abort();

  let sawResult = false;
  try {
    const run = await readWithIdleTimeout(
      withRunnableSession(sessionId, (session) =>
        start(session, runAbort.signal),
      ),
      idle,
    );
    hooks?.onRunStarted?.(run.id);
    const events = run[Symbol.asyncIterator]();
    for (;;) {
      const next = await readWithIdleTimeout(events.next(), idle);
      if (next.done) break;
      for (const translated of translateEvent(next.value, sessionId)) {
        if (translated.type === "run_result") sawResult = true;
        onEvent(translated);
      }
    }
  } catch (error) {
    if (signal?.aborted) throw error;
    // The SDK reports a stream that ended without its terminal result as a
    // ProtocolError; Studio keeps its own words for that one failure.
    if (!sawResult && error instanceof ProtocolError) {
      throw new Error(STREAM_TRUNCATED_MESSAGE);
    }
    throw toHarnessError(error);
  } finally {
    signal?.removeEventListener("abort", abortRun);
  }
  if (!sawResult) throw new Error(STREAM_TRUNCATED_MESSAGE);
}

// ── Run controls ────────────────────────────────────────────────────────────

export type HarnessApprovalVerdict = "allow_once" | "allow_always" | "deny";

/** Every control names its run (ADR 0249); a control with no run to name is
 *  refused the same way the daemon refuses one whose run already ended. */
function staleRunControl(): HarnessApiError {
  return new HarnessApiError(
    409,
    "stale_run_control",
    "The run this control belongs to is no longer known.",
  );
}

/**
 * Resolves a parked permission ask with the daemon's three-way verdict:
 * allow_once, allow_always (persists a permission rule), or deny.
 *
 * `runId` scopes the verdict to ONE run (ADR 0249): the daemon refuses with
 * 409 `stale_run_control` if that run already ended, so a stale approval
 * dialog can never act on the session's NEXT run (e.g. a schedule fire into
 * the same session).
 */
export async function respondToHarnessApproval(
  sessionId: string,
  askId: string,
  verdict: HarnessApprovalVerdict,
  runId: string,
): Promise<void> {
  if (!runId) throw staleRunControl();
  const session = await harnessSession(sessionId);
  await harness(() => session.controls(runId).resolveAsk(askId, verdict));
}

/**
 * Cancels the named in-flight run (ADR 0249): a 409 `stale_run_control`
 * means that run already ended — the desired outcome — and the session's
 * NEXT run is left untouched. Best-effort either way: cancel is
 * fire-and-forget, and a run whose id is not yet known cannot be named.
 */
export async function cancelHarnessRun(
  sessionId: string,
  runId: string,
): Promise<void> {
  if (!runId) return;
  try {
    const session = await harnessSession(sessionId);
    await session.controls(runId).cancel();
  } catch {
    // Fire-and-forget by contract.
  }
}

/** The daemon's verdict on a steer request. `accepted`/`appended` mean the
 *  text will be injected into the in-flight run at the next turn boundary;
 *  `too_late` means the run is past injection and the caller keeps the text
 *  (send it as a normal prompt instead). */
export type HarnessSteerOutcome = "accepted" | "appended" | "too_late";

/**
 * Injects a message into a session's in-flight run (ADR 0252). `messageId` is
 * the client-minted correlation id: the stream's later `steer` echo names the
 * id of the LAST message merged into the drained bundle, and the client
 * splits its ordered pending list on that watermark.
 *
 * The steer is always STRICT (ADR 0249): the daemon refuses with 409
 * `stale_run_control` (a HarnessApiError here) when `expectedRunId` already
 * ended, instead of PROMOTING the text into a fresh follow-up run behind
 * Studio's back — the caller keeps the text and requeues it, exactly the
 * too_late outcome.
 *
 * `parts` carries staged image attachments in the same shape as the prompt
 * (ADR 0251) — a media-only steer is legal.
 */
export async function steerHarnessRun(
  sessionId: string,
  text: string,
  messageId: string,
  options: { expectedRunId: string; parts?: PromptPart[] },
): Promise<{ outcome: HarnessSteerOutcome; messageId: string }> {
  if (!options.expectedRunId) throw staleRunControl();
  const session = await harnessSession(sessionId);
  const ack = await harness(() =>
    session
      .controls(options.expectedRunId)
      .steer(toPromptInput(text, options.parts ?? []), { messageId }),
  );
  return { outcome: ack.outcome, messageId: ack.messageId || messageId };
}

/**
 * Retracts the whole pending steer bundle of the named run (the daemon
 * models one bundle per run, not per-message retraction). `none_pending`
 * means nothing was waiting — the bundle already drained into the run, none
 * was ever sent, or no run is known to hold one.
 */
export async function cancelHarnessSteer(
  sessionId: string,
  runId: string,
): Promise<"retracted" | "none_pending"> {
  if (!runId) return "none_pending";
  const session = await harnessSession(sessionId);
  const ack = await harness(() => session.controls(runId).cancelSteer());
  return ack.outcome;
}

/**
 * Manually compacts a session's conversation (ADR 0244): true means the
 * model history was rewritten (refetch the transcript), false means there
 * was nothing to compact. 412 while the session is active/awaiting; gate the
 * affordance on the `manual_compaction` capability.
 */
export async function compactHarnessSession(
  sessionId: string,
): Promise<boolean> {
  const session = await harnessSession(sessionId);
  return harness(() => session.compact());
}

// ── Session inventory / snapshots / transcripts ─────────────────────────────

/** One page of the session inventory. */
async function fetchSessionInventoryPage(
  cursor: string,
  pageSize: number,
  signal?: AbortSignal,
): Promise<SessionInventoryPage> {
  const response = await harness(() =>
    getHarnessClient().sessions.list(
      { $typeName: "mecatl.v1.ListSessionsRequest", pageSize, cursor },
      { signal },
    ),
  );
  return sessionInventoryFromResponse(response);
}

/**
 * Walks the session inventory to completion, bounded so a pathological store
 * cannot loop the UI forever. Only a COMPLETE walk may be used to conclude a
 * session is gone — a partial page proves nothing about absent rows.
 */
export async function fetchAllSessions(
  signal?: AbortSignal,
  maxPages = 25,
): Promise<{ sessions: SessionSummary[]; complete: boolean }> {
  const sessions: SessionSummary[] = [];
  let cursor = "";
  for (let page = 0; page < maxPages; page += 1) {
    const result = await fetchSessionInventoryPage(cursor, 100, signal);
    sessions.push(...result.sessions);
    if (!result.nextCursor) return { sessions, complete: true };
    cursor = result.nextCursor;
  }
  return { sessions, complete: false };
}

/**
 * Renames a session. Returns the daemon's clamped title echo, which the UI
 * adopts rather than assuming its input survived unmodified.
 */
export async function renameHarnessSession(
  sessionId: string,
  title: string,
): Promise<string> {
  const session = await harnessSession(sessionId);
  const snapshot = await harness(() => session.rename(title));
  return snapshot.title?.value || title;
}

async function fetchSnapshot(
  sessionId: string,
  signal?: AbortSignal,
): Promise<SessionSnapshot> {
  const session = await harnessSession(sessionId, signal);
  return harness(() => session.snapshot({ signal }));
}

/** Reads a session's current permission mode from its snapshot. */
export async function fetchHarnessSessionMode(
  sessionId: string,
  signal?: AbortSignal,
): Promise<SessionPermissionMode> {
  const snapshot = await fetchSnapshot(sessionId, signal);
  return sessionPermissionModeFromSdk(snapshot.mode);
}

/**
 * Changes a session's permission mode. The daemon echoes the updated session;
 * the echoed mode is returned so the UI adopts the daemon's word rather than
 * assuming its input took. The daemon refuses a mid-turn change (the
 * aggregate rejects it while running/awaiting), which surfaces here as a
 * thrown HarnessApiError.
 */
export async function setHarnessSessionMode(
  sessionId: string,
  mode: SessionPermissionMode,
): Promise<SessionPermissionMode> {
  const session = await harnessSession(sessionId);
  const snapshot = await harness(() =>
    session.setMode(sessionPermissionModeToSdk(mode)),
  );
  return sessionPermissionModeFromSdk(snapshot.mode);
}

/** Physically deletes a session's snapshot and sidecars. */
export async function deleteHarnessSession(sessionId: string): Promise<void> {
  const session = await harnessSession(sessionId);
  try {
    await harness(() => session.delete());
  } finally {
    forgetHarnessSession(sessionId);
  }
}

/**
 * Reads the authoritative message-level transcript. This is the store's
 * snapshot, so it covers scheduler-tick fires whose conversation never
 * reached the durable event log, and it works identically in external mode.
 */
export async function fetchSessionTranscriptMessages(
  sessionId: string,
  signal?: AbortSignal,
): Promise<SessionTranscript> {
  const session = await harnessSession(sessionId, signal);
  const transcript = await harness(() => session.transcript({ signal }));
  return sessionTranscriptFromSdk(transcript);
}

/**
 * The provider+model a session actually resolved to (the snapshot's
 * `resolved_model` echo, ADR 0244): the effective model label and the
 * context window the context meter is measured against. Null when the daemon
 * reports none (older daemon / unresolved model).
 */
export interface HarnessResolvedModel {
  providerId: string;
  modelId: string;
  contextWindow: number;
}

/** Session-snapshot detail Studio consumes beyond the mode. */
export interface HarnessSessionDetail {
  resolvedModel: HarnessResolvedModel | null;
  /** The server capabilities echo when the daemon stamps one on the session
   *  (B1.4), in the SDK's camelCase projection; older daemons omit it — fall
   *  back to the compatibility document. */
  capabilities: Record<string, unknown>;
}

export async function fetchHarnessSessionDetail(
  sessionId: string,
  signal?: AbortSignal,
): Promise<HarnessSessionDetail> {
  const snapshot = await fetchSnapshot(sessionId, signal);
  const resolved = snapshot.resolvedModel;
  return {
    resolvedModel: resolved
      ? {
          providerId: resolved.providerId,
          modelId: resolved.modelId,
          contextWindow: Number(resolved.contextWindow) || 0,
        }
      : null,
    capabilities: snapshot.capabilities ? { ...snapshot.capabilities } : {},
  };
}
