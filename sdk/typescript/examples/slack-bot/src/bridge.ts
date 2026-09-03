import type { Client, Session } from "@stacklok/mecatl-sdk";
import { connect, type NodeConnectOptions } from "@stacklok/mecatl-sdk/node";

export interface PromptOutcome {
  text: string;
  stopReason: string;
  sessionId: string;
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
 * on here.
 */
export class MecatlBridge {
  readonly #client: Client;
  readonly #sessions = new Map<string, Session>();
  readonly #queues = new Map<string, Promise<unknown>>();

  constructor(target: NodeConnectOptions) {
    this.#client = connect(target);
  }

  /** Runs one prompt for a thread, queued behind any prompt already in flight for it. */
  async handlePrompt(threadKey: string, text: string): Promise<PromptOutcome> {
    const previous = this.#queues.get(threadKey) ?? Promise.resolve();
    const next = previous.then(() => this.#runPrompt(threadKey, text));
    // Swallow so an awaited failure doesn't become an unhandled rejection on the queue chain.
    this.#queues.set(
      threadKey,
      next.catch(() => undefined),
    );
    return next;
  }

  async close(): Promise<void> {
    await this.#client.close();
  }

  async #runPrompt(threadKey: string, text: string): Promise<PromptOutcome> {
    const session = await this.#sessionFor(threadKey);
    const run = await session.run(text, { onPermissionAsk: () => "allow_once" });
    const result = await run.result();
    return { sessionId: result.sessionId, stopReason: result.stopReason, text: result.text };
  }

  async #sessionFor(threadKey: string): Promise<Session> {
    const existing = this.#sessions.get(threadKey);
    if (existing !== undefined) return existing;
    const session = await this.#client.sessions.create({});
    this.#sessions.set(threadKey, session);
    return session;
  }
}
