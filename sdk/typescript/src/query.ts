import type { Client, CreateSessionOptions, Session } from "./client.js";
import { clientDiagnostics } from "./client.js";
import { type DiagnosticRecord, PlanApprovalRequiredError } from "./errors.js";
import type { Event, PermissionAskEventPayload } from "./events.js";
import type { PromptInput } from "./media.js";
import { PLAN_APPROVED_PROCEED_TEXT, type PlanApprovalResponder } from "./plan.js";
import type { PermissionAskResponder, Run } from "./run.js";
import { type SpawnOptions, spawn } from "./spawn.js";

/** Options for one spawn-create-run-cleanup query. @public */
export interface QueryOptions {
  /** Use an existing client instead of spawning a local daemon. The client remains caller-owned. */
  client?: Client;
  /** Automatically answer permission asks. With no responder, query denies each ask safely. */
  onPermissionAsk?: PermissionAskResponder;
  /** Required in plan mode and invoked only for PresentPlan approval asks. */
  onPlanApproval?: PlanApprovalResponder;
  /** Keep the created session after the query. SDK-spawned daemons use an in-memory store. */
  retainSession?: boolean;
  /** Fields applied when query creates its session. */
  session?: CreateSessionOptions;
  /** Abort this query and clean up every resource it created. */
  signal?: AbortSignal;
  /** Daemon options used only when query creates its own client. */
  spawn?: SpawnOptions;
}

/** One query-owned event stream. Its session id remains useful when retention uses a supplied client. @public */
export interface Query extends AsyncIterable<Event> {
  /** The id of the session created for this query. */
  readonly sessionId: string;
}

interface QueryInternalOptions {
  spawn?: (options?: SpawnOptions) => Promise<Client>;
}

function throwIfAborted(signal: AbortSignal | undefined): void {
  if (!signal?.aborted) return;
  throw signal.reason ?? new DOMException("The query was aborted", "AbortError");
}

function emitAskDenied(client: Client, ask: PermissionAskEventPayload): void {
  const sink = clientDiagnostics(client);
  if (sink === undefined) return;
  const record: DiagnosticRecord = Object.freeze({
    code: "query_permission_ask_denied",
    fields: Object.freeze({ askId: ask.askId, tool: ask.tool }),
    level: "info",
    message: `query() denied permission ask ${ask.askId} for ${ask.tool} because no responder was provided`,
  });
  try {
    sink(record);
  } catch {
    // Diagnostics observers never alter query permission policy.
  }
}

function defaultAskResponder(client: Client): PermissionAskResponder {
  return (ask) => {
    emitAskDenied(client, ask);
    return "deny";
  };
}

async function unwindSetup(
  client: Client | undefined,
  session: Session | undefined,
): Promise<void> {
  try {
    await session?.delete();
  } catch {
    // Preserve the typed setup failure; an SDK-owned client close still tears down its daemon.
  }
  try {
    await client?.close();
  } catch {
    // Client disposal is specified as non-throwing, but setup failure remains primary if that changes.
  }
}

class QueryImpl implements Query, AsyncIterator<Event> {
  readonly sessionId: string;

  readonly #client: Client;
  readonly #closeClient: boolean;
  readonly #onAbort: () => void;
  readonly #retainSession: boolean;
  readonly #runOptions: {
    readonly onPermissionAsk: PermissionAskResponder;
    readonly onPlanApproval?: PlanApprovalResponder;
  };
  readonly #session: Session;
  readonly #signal: AbortSignal | undefined;
  #phase: "initial" | "continuation-pending" | "continuation" | "done";
  #run: Run;
  #source: AsyncIterator<Event>;
  #activeNext: Promise<IteratorResult<Event>> | undefined;
  #cleanupPromise: Promise<void> | undefined;
  #runTerminal = false;

  constructor(
    client: Client,
    session: Session,
    run: Run,
    closeClient: boolean,
    retainSession: boolean,
    signal: AbortSignal | undefined,
    runOptions: {
      readonly onPermissionAsk: PermissionAskResponder;
      readonly onPlanApproval?: PlanApprovalResponder;
    },
    planMode: boolean,
  ) {
    this.#client = client;
    this.#closeClient = closeClient;
    this.#retainSession = retainSession;
    this.#run = run;
    this.#session = session;
    this.#signal = signal;
    this.#source = run[Symbol.asyncIterator]();
    this.#runOptions = runOptions;
    this.#phase = planMode ? "initial" : "continuation";
    this.sessionId = session.id;
    this.#onAbort = () => {
      void this.#finish(true).catch(() => undefined);
    };
    signal?.addEventListener("abort", this.#onAbort, { once: true });
  }

  [Symbol.asyncIterator](): AsyncIterator<Event> {
    return this;
  }

  async next(): Promise<IteratorResult<Event>> {
    if (this.#cleanupPromise !== undefined) {
      await this.#cleanupPromise;
      return { done: true, value: undefined };
    }

    if (this.#phase === "continuation-pending") {
      try {
        throwIfAborted(this.#signal);
        this.#run = await this.#session.run(PLAN_APPROVED_PROCEED_TEXT, this.#runOptions);
        this.#source = this.#run[Symbol.asyncIterator]();
        this.#runTerminal = false;
        this.#phase = "continuation";
      } catch (error) {
        try {
          await this.#finish(true);
        } catch {
          // Preserve the continuation-start failure after attempting full cleanup.
        }
        throw error;
      }
    }

    const pending = this.#source.next();
    this.#activeNext = pending;
    let next: IteratorResult<Event>;
    try {
      next = await pending;
    } catch (error) {
      try {
        await this.#finish(true);
      } catch {
        // Preserve the stream failure after attempting full cleanup.
      }
      throw error;
    } finally {
      if (this.#activeNext === pending) this.#activeNext = undefined;
    }

    if (next.done) {
      await this.#finish(false);
      return next;
    }
    if (next.value.kind === "result") {
      this.#runTerminal = true;
      if (this.#phase === "initial" && next.value.payload.stop === "plan_approved") {
        this.#phase = "continuation-pending";
      } else {
        this.#phase = "done";
        await this.#finish(false);
      }
    }
    return next;
  }

  async return(): Promise<IteratorResult<Event>> {
    await this.#finish(true);
    return { done: true, value: undefined };
  }

  #finish(abandoned: boolean): Promise<void> {
    this.#cleanupPromise ??= this.#finishOnce(abandoned);
    return this.#cleanupPromise;
  }

  async #finishOnce(abandoned: boolean): Promise<void> {
    this.#signal?.removeEventListener("abort", this.#onAbort);
    let failure: unknown;

    if (abandoned && !this.#runTerminal) {
      try {
        await this.#run.cancel();
      } catch (error) {
        failure = error;
      }

      try {
        const active = this.#activeNext;
        if (active !== undefined) this.#observeDrain(await active);
        while (!this.#runTerminal) {
          const next = await this.#source.next();
          this.#observeDrain(next);
          if (next.done) break;
        }
      } catch (error) {
        failure ??= error;
      }
    }

    if (!this.#retainSession) {
      try {
        await this.#session.delete();
      } catch (error) {
        failure ??= error;
      }
    }
    if (this.#closeClient) {
      try {
        await this.#client.close();
      } catch (error) {
        failure ??= error;
      }
    }
    if (failure !== undefined) throw failure;
  }

  #observeDrain(next: IteratorResult<Event>): void {
    if (!next.done && next.value.kind === "result") this.#runTerminal = true;
  }
}

/** Internal construction seam used by the unit suite; not exported from the package entry point. */
export async function queryInternal(
  prompt: PromptInput,
  options: QueryOptions = {},
  internal: QueryInternalOptions = {},
): Promise<Query> {
  const planMode = options.session?.mode === 2;
  if (planMode && options.onPlanApproval === undefined) throw new PlanApprovalRequiredError();
  throwIfAborted(options.signal);

  let client = options.client;
  let session: Session | undefined;
  const closeClient = client === undefined;
  try {
    client ??= await (internal.spawn ?? spawn)(options.spawn);
    throwIfAborted(options.signal);
    session = await client.sessions.create(options.session ?? {});
    throwIfAborted(options.signal);
    const runOptions = {
      onPermissionAsk: options.onPermissionAsk ?? defaultAskResponder(client),
      ...(options.onPlanApproval === undefined ? {} : { onPlanApproval: options.onPlanApproval }),
    };
    const run = await session.run(prompt, runOptions);
    throwIfAborted(options.signal);
    return new QueryImpl(
      client,
      session,
      run,
      closeClient,
      options.retainSession ?? false,
      options.signal,
      runOptions,
      planMode,
    );
  } catch (error) {
    await unwindSetup(closeClient ? client : undefined, session);
    throw error;
  }
}

/** Spawns if needed, creates one session, runs one prompt, and cleans up owned resources. @public */
export function query(prompt: PromptInput, options: QueryOptions = {}): Promise<Query> {
  return queryInternal(prompt, options);
}
