import { type Client, type Run, ServerError, type Session } from "@stacklok-oss/mecatl-sdk";
import { connect, type NodeConnectOptions } from "@stacklok-oss/mecatl-sdk/node";

export interface PromptOutcome {
  text: string;
  stopReason: string;
  sessionId: string;
}

/** Invoked with each incremental text chunk as the run streams, in order. */
export type DeltaHandler = (delta: string) => void | Promise<void>;

/**
 * Detects the "session has a live external authorization" failed-precondition
 * error (#1283): a first-use ToolHive connector needs an interactive OAuth
 * consent this bot can never complete, which otherwise wedges the thread's
 * cached session forever. There's no dedicated stable error code for this
 * (it shares the generic `failed_precondition` bucket — see
 * `sdk/typescript/src/errors.ts`), so detection has to match the free-text
 * message the server actually emits
 * (`internal/adapter/server/mcp_authorization.go`). Exported so
 * `agentSessions.ts` can use the SAME check to pick the right reply — one
 * predicate, not two that could silently drift apart.
 */
export function isStuckExternalAuthorization(error: unknown): boolean {
  return (
    error instanceof ServerError &&
    error.code === "failed_precondition" &&
    error.message.includes("has a live external authorization")
  );
}

/**
 * Bridges Slack threads to mecatl sessions: one session per thread key,
 * created lazily and reused for every later prompt in that thread. Every
 * permission ask is auto-approved (#882: "every demo run executes under an
 * auto-approved posture so it never blocks on a human"). Caller-level
 * authorization and a per-user rate limit live in agentSessions.ts, above
 * this bridge (panel-review, #883) — this class only knows about threads,
 * not who's behind them.
 *
 * Session placement is server-owned: this client never sends a workspace path.
 * Configure the daemon's default with `mecated --workspace`; every new Slack
 * thread receives a session on that server-selected placement.
 *

 * TODO(#883 follow-up, panel-review): no per-run token/spend budget.
 * The SDK's `RunOptions` (M1) doesn't yet expose a per-call token limit —
 * mecatl core supports tighten-only budgets (`Deps.MaxRunTokens`), but the
 * TypeScript SDK hasn't surfaced it as of this writing. Revisit once it
 * does; wire it through `handlePrompt` rather than guessing at a raw
 * protocol field.
 *
 * TODO(#883 follow-up, panel-review): `#sessions` is process-local and
 * lost on restart — a bot restart forgets which mecatl session belongs to
 * which thread, so an in-progress thread starts a fresh session even if
 * the real one is still alive/resumable server-side. Persisting this map
 * (e.g. to a local file or the workspace) is a real design decision
 * (format, migration, staleness) deliberately deferred rather than bolted
 * on here — in-memory only until there's a real datastore to persist it in.
 */
export class MecatlBridge {
  readonly #client: Client;
  readonly #sessions = new Map<string, Session>();
  readonly #queues = new Map<string, Promise<unknown>>();
  readonly #runs = new Map<string, Run>();

  constructor(target: NodeConnectOptions) {
    this.#client = connect(target);
  }

  /**
   * Runs one prompt for a thread, queued behind any prompt already in flight
   * for it. `onDelta`, if given, is invoked in order with each incremental
   * text chunk the run streams before the final result.
   */
  async handlePrompt(
    threadKey: string,
    text: string,
    onDelta?: DeltaHandler,
  ): Promise<PromptOutcome> {
    const previous = this.#queues.get(threadKey) ?? Promise.resolve();
    const next = previous.then(() => this.#runPrompt(threadKey, text, onDelta));
    // Swallow so an awaited failure doesn't become an unhandled rejection on the queue chain.
    this.#queues.set(
      threadKey,
      next.catch(() => undefined),
    );
    return next;
  }

  /** Cancels the thread's in-flight run, if any. A no-op if nothing is running. */
  async cancel(threadKey: string): Promise<void> {
    const run = this.#runs.get(threadKey);
    if (run === undefined) return;
    await run.cancel();
  }

  /** Drops the cached session for a thread so the next prompt starts fresh. */
  evictSession(threadKey: string): void {
    this.#sessions.delete(threadKey);
  }

  async close(): Promise<void> {
    await this.#client.close();
  }

  async #runPrompt(
    threadKey: string,
    text: string,
    onDelta: DeltaHandler | undefined,
  ): Promise<PromptOutcome> {
    const session = await this.#sessionFor(threadKey);
    try {
      // session.run() itself — not just the run's event stream — must be inside
      // this try (#1289 review, samuv): the server can reject a session with a
      // live external authorization at RUN ADMISSION, before the SDK's run()
      // ever resolves (it waits for the first run-ID-bearing event), so that
      // rejection previously escaped this catch entirely and the stuck session
      // was never evicted.
      const run = await session.run(text, { onPermissionAsk: () => "allow_once" });
      this.#runs.set(threadKey, run);
      for await (const event of run) {
        if (event.kind === "message.delta") {
          if (event.text.length > 0) await onDelta?.(event.text);
          continue;
        }
        if (event.kind === "result") {
          return {
            sessionId: run.sessionId,
            stopReason: event.payload.stop,
            text: event.payload.text,
          };
        }
      }
      // RunImpl's own contract (see run.ts's #next()) guarantees every run
      // ends in a "result" event or throws — this is defensive, not reachable.
      throw new Error(`run ${run.id} ended without a terminal result`);
    } catch (error) {
      // Evict HERE — synchronously, before this function's own promise settles —
      // not in agentSessions.ts's caller-side catch (#1287 review, samuv). Slack
      // can deliver a second message for the same thread while this one is still
      // running; that second call is already chained onto #queues behind this
      // one via `next.catch(() => undefined)`, and that guard is attached to
      // `next` BEFORE the caller's own `await handlePrompt(...)` ever gets to
      // react to the rejection. So a caller-side evict always loses the race:
      // the next queued #runPrompt can start, reusing the stuck session, before
      // the caller's catch block runs. Evicting inside the promise that the
      // queue itself awaits closes that window structurally.
      if (isStuckExternalAuthorization(error)) this.evictSession(threadKey);
      throw error;
    } finally {
      this.#runs.delete(threadKey);
    }
  }

  async #sessionFor(threadKey: string): Promise<Session> {
    const existing = this.#sessions.get(threadKey);
    if (existing !== undefined) return existing;
    const session = await this.#client.sessions.create({});
    this.#sessions.set(threadKey, session);
    return session;
  }
}
