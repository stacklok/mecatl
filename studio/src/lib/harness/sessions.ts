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
  isPendingAuthorizationStatus,
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
import type { SessionToolProfile } from "@/lib/tool-profile";
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
  options?: {
    modelId?: string;
    providerId?: string;
    /** A reasoning-effort tier (low…max); ""/absent omits the field so the
     *  operator's `--reasoning-effort` default applies. */
    reasoningEffort?: string;
    /** The tool profile (ADR 0291): "no-fs" attenuates the session to the
     *  file-less catalog (no Shell/Read/Edit/Write…); ""/absent omits the
     *  field so the deployment default applies and the ordinary create body
     *  stays byte-identical. Fixed at create — there is no switch. */
    profile?: SessionToolProfile;
    signal?: AbortSignal;
  },
): Promise<string> {
  const session = await harness(() =>
    getHarnessClient().sessions.create(
      {
        mode: sessionPermissionModeToSdk(mode),
        ...(options?.profile ? { profile: options.profile } : {}),
        // Omitted entirely on auto-routing so the daemon's own selection
        // applies. The daemon requires provider_id whenever model_id is set
        // (a bare model is ambiguous across providers).
        ...(options?.modelId
          ? {
              modelId: options.modelId,
              ...(options.providerId ? { providerId: options.providerId } : {}),
            }
          : {}),
        ...(options?.reasoningEffort
          ? { reasoningEffort: options.reasoningEffort }
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
  options: {
    title?: string;
    modelId?: string;
    providerId?: string;
    /** A reasoning-effort tier for the fork; ""/absent omits the field. A
     *  fork accepts an effort WITHOUT a model (the source's model carries). */
    reasoningEffort?: string;
    /** An opaque worktree selector from ListWorktrees (ADR 0291): the fork
     *  is rooted at that worktree instead of inheriting the source's
     *  placement. ""/absent omits the field. */
    worktreeSelector?: string;
  },
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
        ...(options.reasoningEffort
          ? { reasoningEffort: options.reasoningEffort }
          : {}),
        ...(options.worktreeSelector
          ? { worktreeSelector: options.worktreeSelector }
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
  return forkHarnessSessionToSelection(
    sourceSessionId,
    { model },
    title,
    signal,
  );
}

/**
 * What a mid-chat switch changes: the model (null/absent = the daemon's own
 * routing/default), the reasoning-effort tier (""/absent = the operator
 * default), or both. An effort switch alone keeps the source's model.
 */
export interface HarnessForkSelection {
  model?: { modelId: string; providerId: string } | null;
  reasoningEffort?: string;
}

/**
 * Continues an existing chat on a different model and/or reasoning-effort
 * tier — the same FORK as `forkHarnessSessionToModel` (the daemon fixes both
 * at create), carrying the source's title. A running/awaiting source answers
 * 412 (ThreadSourceBusyError).
 */
export async function forkHarnessSessionToSelection(
  sourceSessionId: string,
  selection: HarnessForkSelection,
  title: string,
  signal?: AbortSignal,
): Promise<string> {
  return forkHarnessSession(
    sourceSessionId,
    {
      title,
      ...(selection.model ? selection.model : {}),
      ...(selection.reasoningEffort
        ? { reasoningEffort: selection.reasoningEffort }
        : {}),
    },
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

/** The title a fork-as-is copy gets: the source's (or a plain floor) + " (copy)",
 *  so the two otherwise identical rows read apart in the list. */
function forkCopyTitle(sourceTitle: string): string {
  return `${sourceTitle || "Untitled chat"} (copy)`;
}

/**
 * Forks a chat AS-IS (the TUI's `f` on /sessions): a copy seeded from the
 * source's history on the same model, provider, effort and placement — the
 * body carries the title alone, so the daemon's own routing/defaults for the
 * source apply unchanged. The source stays in the list. A running/awaiting
 * source answers 412 (ThreadSourceBusyError).
 */
export async function forkHarnessSessionCopy(
  sourceSessionId: string,
  sourceTitle: string,
  signal?: AbortSignal,
): Promise<string> {
  return forkHarnessSession(
    sourceSessionId,
    { title: forkCopyTitle(sourceTitle) },
    signal,
  );
}

/**
 * Continues an existing chat in ANOTHER worktree of the repository: a fork
 * seeded from the source's history, carrying its title, rooted at the
 * worktree the opaque `worktreeSelector` names (from ListWorktrees, ADR
 * 0291 — never a path). A stale selector is refused with a
 * `placement_selector_*` HarnessApiError; a running/awaiting source answers
 * 412 (ThreadSourceBusyError). The model and effort carry over unchanged.
 */
export async function forkHarnessSessionToWorktree(
  sourceSessionId: string,
  title: string,
  worktreeSelector: string,
  signal?: AbortSignal,
): Promise<string> {
  return forkHarnessSession(
    sourceSessionId,
    { title, worktreeSelector },
    signal,
  );
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
  // A run parked on a browser authorization (authorization.required with no
  // later resolved) closes the prompt stream WITHOUT a result by design: the
  // run continues over the authorization control stream, so that close is a
  // normal end here, not a truncation.
  let parkedOnAuthorization = false;
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
        if (translated.type === "authorization") {
          parkedOnAuthorization = isPendingAuthorizationStatus(
            translated.status,
          );
        } else if (translated.type === "authorization_resolved") {
          parkedOnAuthorization = false;
        }
        onEvent(translated);
      }
    }
  } catch (error) {
    if (signal?.aborted) throw error;
    // The SDK reports a stream that ended without its terminal result as a
    // ProtocolError; Studio keeps its own words for that one failure — unless
    // the run parked on an authorization, which is how that stream ENDS.
    if (!sawResult && error instanceof ProtocolError) {
      if (parkedOnAuthorization) return;
      throw new Error(STREAM_TRUNCATED_MESSAGE);
    }
    throw toHarnessError(error);
  } finally {
    signal?.removeEventListener("abort", abortRun);
  }
  if (!sawResult && !parkedOnAuthorization) {
    throw new Error(STREAM_TRUNCATED_MESSAGE);
  }
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
): Promise<HarnessRunCancelOutcome> {
  if (!runId) return "stale";
  try {
    const session = await harnessSession(sessionId);
    await harness(() => session.controls(runId).cancel());
    return "cancelled";
  } catch (error) {
    // Never throws (fire-and-forget by contract); the outcome tells the
    // caller whether the run was still live to be cancelled.
    if (
      error instanceof HarnessApiError &&
      error.code === "stale_run_control"
    ) {
      return "stale";
    }
    return "unknown";
  }
}

/**
 * How a run cancel landed: the daemon accepted it (`cancelled`); the run had
 * already ended, or there was no run id to name (`stale` — the turn ended on
 * its own terms and must not be labelled cancelled); or the request itself
 * failed (`unknown` — the run's state is not known to this client).
 */
export type HarnessRunCancelOutcome = "cancelled" | "stale" | "unknown";

/** The outcome of a per-child cancel: the daemon accepted it, or the child
 *  was already gone (finished, or never known to this run). */
export type HarnessChildCancelOutcome = "cancelled" | "not_found";

/**
 * Cancels ONE running delegated child — a subagent, a parallel branch, or a
 * team member — by its child session id, while the parent run keeps
 * streaming (`POST /v1/sessions/{id}/cancel-child`, mirroring `/approve`).
 * The daemon retracts any permission ask the child had surfaced; the
 * stream's `retract` frame then dismisses it client-side.
 *
 * NOT fire-and-forget, unlike `cancelHarnessRun`: the UI reports the outcome.
 * A 404 `child_not_found` (an unknown or already-finished child) or a bare
 * `not_found` reads as "already finished"; a 409 `no_active_run` and every
 * transport fault propagate as the typed HarnessApiError.
 */
export async function cancelHarnessChild(
  sessionId: string,
  childId: string,
): Promise<HarnessChildCancelOutcome> {
  const session = await harnessSession(sessionId);
  try {
    await harness(() => session.cancelChild(childId));
    return "cancelled";
  } catch (error) {
    if (
      error instanceof HarnessApiError &&
      (error.code === "child_not_found" || error.code === "not_found")
    ) {
      return "not_found";
    }
    throw error;
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
 * One page of the inventory walk as it lands: the cumulative page/row counts
 * so far plus the rows of THIS page, so a caller can merge and show them
 * before the walk finishes (or is cancelled).
 */
export interface SessionWalkProgress {
  /** Pages fetched so far, this one included. */
  pages: number;
  /** Rows fetched so far across every page, this one included. */
  rows: number;
  /** The rows of the page that just landed. */
  page: SessionSummary[];
}

/**
 * Walks the session inventory to completion, bounded so a pathological store
 * cannot loop the UI forever. Only a COMPLETE walk may be used to conclude a
 * session is gone — a partial page proves nothing about absent rows.
 * `onProgress` fires after each page lands; an aborted `signal` rejects the
 * walk mid-way (the pages already reported through `onProgress` stand).
 */
export async function fetchAllSessions(
  signal?: AbortSignal,
  maxPages = 25,
  onProgress?: (progress: SessionWalkProgress) => void,
): Promise<{ sessions: SessionSummary[]; complete: boolean }> {
  const sessions: SessionSummary[] = [];
  let cursor = "";
  for (let page = 0; page < maxPages; page += 1) {
    const result = await fetchSessionInventoryPage(cursor, 100, signal);
    sessions.push(...result.sessions);
    onProgress?.({
      pages: page + 1,
      rows: sessions.length,
      page: result.sessions,
    });
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
interface HarnessResolvedModel {
  providerId: string;
  modelId: string;
  contextWindow: number;
  /** The effective reasoning-effort tier ("" when the daemon echoes none). */
  reasoningEffort: string;
}

/**
 * The session's cumulative token spend as the daemon persists it (the
 * snapshot's `token_usage["main"].total` bucket, engine/session/usage.go) —
 * durable across reloads, unlike a client-side sum of run terminals.
 */
interface HarnessSessionTokenUsage {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  reasoningTokens: number;
}

/**
 * The server-owned placement's bounded DISPLAY metadata (ADR 0291): the
 * daemon's kind word (`local`, `git-worktree`, `no-fs`…), a label (a
 * worktree's directory name), the checked-out branch and the HEAD revision.
 * Never a path and never a selector — Studio only ever shows it.
 */
interface HarnessPlacement {
  kind: string;
  label: string;
  branch: string;
  revision: string;
}

/** The snapshot's `placement`, or null when the daemon omits it or names
 *  neither a label nor a kind (an older daemon, or no display metadata). */
function placementFromSnapshot(
  value: SessionSnapshot["placement"],
): HarnessPlacement | null {
  if (!value || !(value.label || value.kind)) return null;
  return {
    kind: value.kind,
    label: value.label,
    branch: value.branch,
    revision: value.revision ?? "",
  };
}

/** Session-snapshot detail Studio consumes beyond the mode. */
export interface HarnessSessionDetail {
  resolvedModel: HarnessResolvedModel | null;
  /** The placement's display metadata the chat header badges (label and
   *  branch); null on an older daemon or when the snapshot carries none. */
  placement: HarnessPlacement | null;
  /** The server capabilities echo when the daemon stamps one on the session
   *  (B1.4), in the SDK's camelCase projection; older daemons omit it — fall
   *  back to the compatibility document. */
  capabilities: Record<string, unknown>;
  /** Null when the daemon reports no main-bucket usage (older daemon, or a
   *  session that has not run yet) — the client keeps its own visit sum. */
  tokenUsage: HarnessSessionTokenUsage | null;
  /** The session's RESOLVED input modalities (`session_capabilities`, proto
   *  SessionSnapshot field 21): what the composer may send as image/audio
   *  parts. Null on an older daemon — fall back to the compatibility echo. */
  sessionCapabilities: { image: boolean; audio: boolean } | null;
  /** On an AI-debug session (ADR 0254): the stored session it diagnoses, off
   *  the snapshot's relationship — so the chat's DEBUG header does not depend
   *  on the inventory row alone. Absent on an ordinary session. */
  debugTargetSessionId?: string;
  /** On an AI-debug session: the reporting MCP servers bound at create
   *  (`--debug-mcp`); availability never authorizes publication. Absent when
   *  none are bound. */
  debugMcpServers?: string[];
  /** On an AI-debug session: the direct MCP tools those servers mounted
   *  (`mcp__<server>__<tool>`) — each call asks for approval one call at a
   *  time. Absent when none are mounted. */
  debugMcpTools?: string[];
}

/** The token-usage bucket the main agent's spend is recorded under. */
const MAIN_USAGE_BUCKET = "main";

export async function fetchHarnessSessionDetail(
  sessionId: string,
  signal?: AbortSignal,
): Promise<HarnessSessionDetail> {
  const snapshot = await fetchSnapshot(sessionId, signal);
  const resolved = snapshot.resolvedModel;
  const total = snapshot.tokenUsage?.[MAIN_USAGE_BUCKET]?.total;
  const debugTarget = snapshot.relationship?.debugTargetSessionId ?? "";
  const debugServers = (snapshot.debugMcpServers ?? []).filter(Boolean);
  const debugTools = (snapshot.debugMcpTools ?? []).filter(Boolean);
  return {
    // Debug-session facts ride only when set, so an ordinary session's detail
    // stays the three-field shape older callers and tests compare against.
    ...(debugTarget ? { debugTargetSessionId: debugTarget } : {}),
    ...(debugServers.length > 0 ? { debugMcpServers: debugServers } : {}),
    ...(debugTools.length > 0 ? { debugMcpTools: debugTools } : {}),
    resolvedModel: resolved
      ? {
          providerId: resolved.providerId,
          modelId: resolved.modelId,
          contextWindow: Number(resolved.contextWindow) || 0,
          reasoningEffort: resolved.reasoningEffort ?? "",
        }
      : null,
    placement: placementFromSnapshot(snapshot.placement),
    capabilities: snapshot.capabilities ? { ...snapshot.capabilities } : {},
    tokenUsage: total
      ? {
          inputTokens: Number(total.inputTokens) || 0,
          outputTokens: Number(total.outputTokens) || 0,
          cacheReadTokens: Number(total.cacheReadTokens) || 0,
          cacheWriteTokens: Number(total.cacheWriteTokens) || 0,
          reasoningTokens: Number(total.reasoningTokens) || 0,
        }
      : null,
    sessionCapabilities: snapshot.sessionCapabilities
      ? {
          image: snapshot.sessionCapabilities.image === true,
          audio: snapshot.sessionCapabilities.audio === true,
        }
      : null,
  };
}

// ── Session identity (the /session built-in) ────────────────────────────────

/**
 * What the `/session` details dialog shows: the exact daemon id, title,
 * lifecycle state, permission mode, the resolved provider/model, the
 * server-owned placement's DISPLAY metadata (never a path, ADR 0291), and the
 * creation time. Read fresh from the snapshot on open.
 */
export interface HarnessSessionIdentity {
  id: string;
  title: string;
  /** "operator" / "first-prompt" / "generated"; "" on an older daemon. */
  titleProvenance: string;
  state: string;
  /** The daemon's session kind (main/subagent/team_member/scheduled/debug…);
   *  "" when an older daemon omits it. */
  kind: string;
  mode: SessionPermissionMode;
  resolvedModel: HarnessResolvedModel | null;
  placement: HarnessPlacement | null;
  /** Unix seconds; 0 when the daemon reports none. */
  createdAtUnix: number;
  turns: number;
  toolCalls: number;
  /** Null when the daemon echoes no limits block (older daemon). */
  limits: {
    maxTurns: number;
    maxToolCalls: number;
    maxConsecutiveFailures: number;
  } | null;
  /** How this session relates to others (ADR 0254 and the delegation
   *  families): only the fields the daemon set; null when it set none. */
  relationship: HarnessSessionRelationship | null;
}

interface HarnessSessionRelationship {
  parentSessionId?: string;
  callId?: string;
  branchIndex?: number;
  scheduleName?: string;
  originSessionId?: string;
  teamId?: string;
  memberName?: string;
  debugTargetSessionId?: string;
}

function relationshipFromSnapshot(
  value: SessionSnapshot["relationship"],
): HarnessSessionRelationship | null {
  if (!value) return null;
  const out: HarnessSessionRelationship = {};
  if (value.parentSessionId) out.parentSessionId = value.parentSessionId;
  if (value.callId) out.callId = value.callId;
  if (value.branchIndex !== undefined && value.branchIndex > 0)
    out.branchIndex = value.branchIndex;
  if (value.scheduleName) out.scheduleName = value.scheduleName;
  if (value.originSessionId) out.originSessionId = value.originSessionId;
  if (value.teamId) out.teamId = value.teamId;
  if (value.memberName) out.memberName = value.memberName;
  if (value.debugTargetSessionId)
    out.debugTargetSessionId = value.debugTargetSessionId;
  return Object.keys(out).length > 0 ? out : null;
}

/**
 * The pure projection behind `fetchHarnessSessionIdentity`: everything the
 * details dialog shows, read off one GET-session snapshot. Tolerates an
 * older daemon that omits placement, relationship, limits, kind or title.
 */
export function sessionIdentityFromSnapshot(
  snapshot: SessionSnapshot,
  fallbackId = "",
): HarnessSessionIdentity {
  const resolved = snapshot.resolvedModel;
  const limits = snapshot.limits;
  return {
    id: snapshot.sessionId || fallbackId,
    title: snapshot.title?.value ?? "",
    titleProvenance: snapshot.title?.provenance ?? "",
    state: snapshot.state ?? "",
    kind: snapshot.kind ?? "",
    mode: sessionPermissionModeFromSdk(snapshot.mode),
    resolvedModel: resolved
      ? {
          providerId: resolved.providerId,
          modelId: resolved.modelId,
          contextWindow: Number(resolved.contextWindow) || 0,
          reasoningEffort: resolved.reasoningEffort ?? "",
        }
      : null,
    placement: placementFromSnapshot(snapshot.placement),
    createdAtUnix: Number(snapshot.createdAtUnix) || 0,
    turns: Number(snapshot.turns) || 0,
    toolCalls: Number(snapshot.toolCalls) || 0,
    limits: limits
      ? {
          maxTurns: Number(limits.maxTurns) || 0,
          maxToolCalls: Number(limits.maxToolCalls) || 0,
          maxConsecutiveFailures: Number(limits.maxConsecutiveFailures) || 0,
        }
      : null,
    relationship: relationshipFromSnapshot(snapshot.relationship),
  };
}

export async function fetchHarnessSessionIdentity(
  sessionId: string,
  signal?: AbortSignal,
): Promise<HarnessSessionIdentity> {
  const snapshot = await fetchSnapshot(sessionId, signal);
  return sessionIdentityFromSnapshot(snapshot, sessionId);
}

// ── Clear (the /clear built-in) ─────────────────────────────────────────────

/**
 * The ClearSession successor RPC (`POST /v1/sessions/{id}/clear`, ADR 0291):
 * a DISTINCT empty-history session inheriting the source's placement, model,
 * mode and limits; the source is left intact. Returns the successor's id
 * (its handle adopted into the cache so the UI can drive it at once). A
 * running/awaiting source is refused by the daemon (typed HarnessApiError).
 */
export async function clearHarnessSession(
  sessionId: string,
  options?: {
    /** An opaque worktree selector from ListWorktrees (ADR 0291): the
     *  successor is rooted at that worktree instead of inheriting the
     *  source's placement. ""/absent omits the field (same placement). */
    worktreeSelector?: string;
  },
  signal?: AbortSignal,
): Promise<string> {
  const session = await harnessSession(sessionId, signal);
  const successor = await harness(() =>
    session.clear(
      options?.worktreeSelector
        ? { worktreeSelector: options.worktreeSelector }
        : {},
      { signal },
    ),
  );
  if (!successor.id) throw new Error("harness returned no session id");
  return adoptSession(successor).id;
}
