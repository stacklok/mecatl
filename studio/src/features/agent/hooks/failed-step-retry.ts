import type { AgentMessage, RetryDisposition, StreamProgress } from "../types";

/**
 * The failed-step retry decisions (ADR 0239), extracted pure from the chat
 * hook so each one is testable on its own. The TUI's rules, carried over:
 *
 * - ONE automatic, prompt-free retry per user prompt, and only when the
 *   daemon typed the failure `retryable` AND the stream died `precommit`
 *   (nothing visible was produced, so nothing is duplicated). A permanent
 *   failure is never retried; a retry that fails again is never re-retried.
 * - The server stays authoritative for eligibility: a manual Retry asks the
 *   daemon first for every disposition except `permanent` — including the
 *   UNKNOWN one a reload leaves behind — and falls back to re-sending the
 *   prompt only on the daemon's 409 `failed_step_retry_ineligible`.
 * - A failed session revisited later still offers Retry (durable), even
 *   though the transcript carries no error text.
 */

/** Transcript notice on the failed bubble when the hook retries by itself. */
export const AUTO_RETRY_NOTICE = "Retrying the failed step automatically…";

/** The strip text for a chat whose inventory state reads `failed` on open. */
export const REHYDRATED_FAILURE_ERROR = "The last turn of this chat failed.";

/** The failed bubble's detail for that rehydrated failure (the transcript
 *  carries no error text, so the card says only what is known). */
export const REHYDRATED_FAILURE_DETAIL =
  "This turn failed in an earlier visit.";

/** A retry run that ended `precommit`: the model was never called. */
export const RETRY_PRECOMMIT_EXPLANATION =
  "The retry stopped before the model was called — nothing was re-run.";

/** The daemon refused the retry (409) and no prompt is held to re-send. */
export const RETRY_INELIGIBLE_NO_PROMPT =
  "The daemon has no failed step to retry, and the original prompt is no longer held here. Send it again from the composer.";

/** The daemon typed the failure permanent (either wire field). */
export function isPermanentFailure(event: {
  permanent: boolean;
  retryDisposition?: RetryDisposition;
}): boolean {
  return event.permanent || event.retryDisposition === "permanent";
}

/**
 * The TUI's `FailedStepRetryEligible && !failedStepRetryTried`: retryable +
 * precommit, not yet spent, never permanent. An absent or `unknown` field
 * (an older daemon) never auto-retries — the manual affordance remains.
 */
export function shouldAutoRetry(input: {
  disposition?: RetryDisposition;
  streamProgress?: StreamProgress;
  alreadyRetried: boolean;
  permanent: boolean;
}): boolean {
  if (input.alreadyRetried || input.permanent) return false;
  return (
    input.disposition === "retryable" && input.streamProgress === "precommit"
  );
}

export type RetryRoute = "endpoint" | "resend";

/**
 * Where a manual Retry goes. `endpoint` = `POST .../retry` (the daemon
 * decides eligibility); `resend` = re-send the held prompt. Only a PERMANENT
 * disposition skips the daemon (the identical request is rejected), and so
 * does a turn that did not fail at all (`failed === false`: a cancel leaves
 * no failed step daemon-side, so asking would only earn a 409).
 */
export function retryRoute(
  disposition: RetryDisposition | undefined,
  failed = true,
): RetryRoute {
  if (!failed) return "resend";
  return disposition === "permanent" ? "resend" : "endpoint";
}

/** The strip text when `POST .../retry` itself failed (not a 409). */
export function retryStartFailure(message: string): string {
  return `Retry could not start: ${message}`;
}

/**
 * The strip text after a RETRY run ended in error: a `precommit` stop means
 * the model was never called, which is worth saying — otherwise the daemon's
 * own error text stands.
 */
export function retryFailureExplanation(input: {
  streamProgress?: StreamProgress;
  error: string;
}): string {
  if (input.streamProgress === "precommit") return RETRY_PRECOMMIT_EXPLANATION;
  return input.error || "The run failed without a specific error.";
}

/** A user message that was the turn's own prompt: not a scheduled task's
 *  delivery note, not a mid-run steer. */
function isGenuinePrompt(message: AgentMessage): boolean {
  return message.role === "user" && !message.delivery && !message.steered;
}

export interface FailedRehydrate {
  /** The transcript with the failed turn marked (or a failed bubble added). */
  messages: AgentMessage[];
  /** The id of the bubble that carries the marking. */
  markedId: string;
  /** True when no assistant turn existed and one was appended to carry it. */
  appended: boolean;
  /** The prompt of that failed turn, for the resend fallback (null = none). */
  lastPrompt: string | null;
}

/**
 * Marks a rehydrated transcript as the failed session it belongs to: the
 * trailing assistant turn renders failed (a run that died before recording
 * any assistant text gets a failed bubble appended after its prompt), and the
 * prompt IMMEDIATELY before that turn is extracted for the resend fallback —
 * never an older, unrelated one. Null when there is nothing to mark.
 */
export function failedRehydrateState(
  messages: readonly AgentMessage[],
): FailedRehydrate | null {
  if (messages.length === 0) return null;
  const last = messages[messages.length - 1];
  if (last.role === "tool") return null;
  const marked: AgentMessage[] = [...messages];
  let markedId: string;
  let appended = false;
  let promptIndex: number;
  if (last.role === "assistant") {
    markedId = last.id;
    marked[marked.length - 1] = {
      ...last,
      failed: true,
      failureDetail: last.failureDetail ?? REHYDRATED_FAILURE_DETAIL,
      toolCalls: last.toolCalls?.map((call) =>
        call.status === "running"
          ? { ...call, status: "failed" as const }
          : call,
      ),
    };
    promptIndex = marked.length - 2;
  } else {
    if (!isGenuinePrompt(last)) return null;
    markedId = `${last.id}-failed`;
    appended = true;
    marked.push({
      id: markedId,
      role: "assistant",
      content: "",
      timestamp: last.timestamp,
      failed: true,
      failureDetail: REHYDRATED_FAILURE_DETAIL,
    });
    promptIndex = marked.length - 2;
  }
  // The turn's prompt sits right before it; a steer that landed mid-run
  // belongs to the same turn and is skipped, anything else ends the search.
  let lastPrompt: string | null = null;
  for (let index = promptIndex; index >= 0; index -= 1) {
    const candidate = marked[index];
    if (candidate.role === "user" && candidate.steered) continue;
    if (isGenuinePrompt(candidate)) lastPrompt = candidate.content;
    break;
  }
  return { messages: marked, markedId, appended, lastPrompt };
}

/** Reverses `failedRehydrateState` once the session has left `failed`
 *  without this tab driving a run (another client moved it on). */
export function unmarkFailedRehydrate(
  messages: readonly AgentMessage[],
  mark: Pick<FailedRehydrate, "markedId" | "appended">,
): AgentMessage[] {
  if (mark.appended) {
    return messages.filter((message) => message.id !== mark.markedId);
  }
  return messages.map((message) =>
    message.id === mark.markedId &&
    message.failureDetail === REHYDRATED_FAILURE_DETAIL
      ? { ...message, failed: undefined, failureDetail: undefined }
      : message,
  );
}

/**
 * The prompt this tab last sent, scoped to the chat and the send it belongs
 * to (the TUI's `promptRecovery{sessionID, streamGen}`): a prompt from another
 * chat or an earlier run must never replay. `sessionId` is null for a draft
 * whose daemon session is minted on that very send.
 */
export interface ScopedPrompt {
  sessionId: string | null;
  serial: number;
  text: string;
  files?: File[];
}

/** True when the held prompt belongs to the chat currently bound. */
export function promptBelongsTo(
  prompt: ScopedPrompt | null,
  sessionId: string | null,
): prompt is ScopedPrompt {
  if (!prompt) return false;
  return prompt.sessionId === null || prompt.sessionId === sessionId;
}

/**
 * The draft handed back to the composer after a transport fault, for
 * edit-before-resend. Text-only, like the TUI's `recoverPrompt`: attachments
 * have a one-shot lifecycle and are never replayed implicitly into the
 * composer — the Retry button's resend path still carries them.
 */
export function recoverableDraft(prompt: {
  text: string;
  files?: File[];
}): { text: string } | null {
  if (!prompt.text.trim()) return null;
  if (prompt.files && prompt.files.length > 0) return null;
  return { text: prompt.text };
}

/**
 * Drops the failed exchange from the tail of the transcript: every trailing
 * failed or still-empty assistant bubble, then the user bubble that carried
 * `prompt` — so a resend, or an edit-and-resend from the composer, replaces
 * the exchange instead of stacking a duplicate above it. An unrelated tail
 * (a clean answer, a different prompt) is left exactly as it was.
 */
export function trimFailedExchange(
  messages: AgentMessage[],
  prompt: string,
): AgentMessage[] {
  const trimmed = [...messages];
  while (trimmed.length) {
    const last = trimmed[trimmed.length - 1];
    if (last.role === "assistant" && (last.failed || !last.content)) {
      trimmed.pop();
      continue;
    }
    if (last.role === "user" && last.content === prompt) {
      trimmed.pop();
    }
    break;
  }
  return trimmed;
}
