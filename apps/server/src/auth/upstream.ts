// SPDX-License-Identifier: Apache-2.0

import { AsyncLocalStorage } from "node:async_hooks";
import {
  MecatlError,
  AuthenticationError as SdkAuthenticationError,
} from "@stacklok-oss/mecatl-sdk";
import type { Context } from "hono";
import type { AppEnv } from "../http/env.js";
import type { ChatService } from "../mecatl/chat.js";
import type { MecatlRuntime } from "../mecatl/runtime.js";
import type { AuthenticationCredential, AuthenticationService } from "./service.js";

/** Sessions remembered as opened by this process; the oldest are forgotten first. */
const maximumOpenedSessions = 10_000;

interface RequestState {
  readonly context: Context<AppEnv>;
  credential: AuthenticationCredential;
  /** Set once this request replaced the credential the session gate bound. */
  refreshed: boolean;
}

/**
 * The browser's credential as one request presents it to Mecatl. Every
 * upstream call made through a guarded service gets one refresh and one retry
 * when Mecatl rejects the bearer; a stream gets it only before its first item,
 * which routes pull before committing a response. When the deployment signs
 * users in at AS-1 (`freshTokenPerSessionOpen`), requests that open a session
 * first refresh, so each carries an access token no earlier request used.
 * Every refresh is stored in the response's cookies, so it must happen before
 * the response is committed.
 */
export class UpstreamCredentials {
  readonly #authentication: AuthenticationService;
  readonly #opened = new Set<string>();
  readonly #requests = new AsyncLocalStorage<RequestState>();
  readonly #runtime: MecatlRuntime;

  constructor(authentication: AuthenticationService, runtime: MecatlRuntime) {
    this.#authentication = authentication;
    this.#runtime = runtime;
  }

  /** The session gate's binding: `operation` runs with this request's credential. */
  bind<T>(
    context: Context<AppEnv>,
    credential: AuthenticationCredential,
    operation: () => Promise<T>,
  ): Promise<T> {
    return this.#requests.run({ context, credential, refreshed: false }, () =>
      this.#runtime.runWithCredential(credential.accessToken, operation),
    );
  }

  /**
   * Wraps each method of a feature service with the one-retry policy. Other
   * properties, live capability getters included, still read the service.
   */
  guard<S extends object>(service: S): S {
    const guarded: PropertyDescriptorMap = {};
    for (const [name, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(service))) {
      const member: unknown = descriptor.value;
      guarded[name] =
        typeof member === "function"
          ? {
              ...descriptor,
              value: (...args: unknown[]) => this.#invoke(() => member.apply(service, args)),
            }
          : {
              enumerable: descriptor.enumerable ?? true,
              get: () => Reflect.get(service, name),
            };
    }
    return Object.defineProperties({}, guarded) as S;
  }

  /** `guard`, plus fresh tokens for the chat operations that open a session. */
  guardChat(chat: ChatService): ChatService {
    const opening = async (operation: () => Promise<{ id: string }>) => {
      const opened = await this.#call(operation, true);
      this.#noteOpened(opened.id);
      return opened;
    };
    return {
      ...this.guard(chat),
      clearSession: (sessionId, request) => opening(() => chat.clearSession(sessionId, request)),
      createSession: (request) => opening(() => chat.createSession(request)),
      forkSession: (sessionId, request) => opening(() => chat.forkSession(sessionId, request)),
      // Run entry reopens a broker session this process did not open (one
      // created elsewhere, or before a restart), so its first run is fresh.
      retry: (sessionId, signal) =>
        this.#stream(
          () => chat.retry(sessionId, signal),
          () => this.#firstEntry(sessionId),
        ),
      run: (sessionId, request, signal) =>
        this.#stream(
          () => chat.run(sessionId, request, signal),
          () => this.#firstEntry(sessionId),
        ),
    };
  }

  #invoke(operation: () => unknown): unknown {
    const state = this.#requests.getStore();
    if (state === undefined) return operation();
    const result = this.#within(state, operation);
    if (isAsyncIterable(result)) {
      let first = true;
      // The stream is already open; a retry opens another.
      return this.#stream(() => {
        if (!first) return operation() as AsyncIterable<unknown>;
        first = false;
        return result;
      });
    }
    if (isPromiseLike(result)) {
      return this.#retrying(state, result, operation as () => PromiseLike<unknown>);
    }
    return result;
  }

  async #call<T>(operation: () => Promise<T>, fresh: boolean): Promise<T> {
    const state = this.#requests.getStore();
    if (state === undefined) return operation();
    if (fresh && this.#freshOpens(state)) await this.#refresh(state);
    return this.#retrying(state, this.#within(state, operation), operation);
  }

  async #retrying<T>(
    state: RequestState,
    pending: PromiseLike<T>,
    operation: () => PromiseLike<T>,
  ): Promise<T> {
    try {
      return await pending;
    } catch (error) {
      if (!this.#retryable(state, error)) throw error;
      await this.#refresh(state);
      return await this.#within(state, operation);
    }
  }

  /**
   * Opens `open` lazily. `fresh` decides at the first pull whether the open
   * needs a fresh token; a rejection before the first item refreshes and
   * reopens once, later ones propagate.
   */
  async *#stream<T>(
    open: () => AsyncIterable<T>,
    fresh: () => boolean = () => false,
  ): AsyncGenerator<T, void, undefined> {
    const state = this.#requests.getStore();
    if (state === undefined) {
      yield* open();
      return;
    }
    if (fresh() && this.#freshOpens(state)) await this.#refresh(state);
    let iterator = this.#within(state, () => open()[Symbol.asyncIterator]());
    let delivered = false;
    let retried = false;
    try {
      for (;;) {
        let next: IteratorResult<T>;
        try {
          next = await this.#within(state, () => iterator.next());
        } catch (error) {
          if (delivered || retried || !this.#retryable(state, error)) throw error;
          retried = true;
          await this.#refresh(state);
          iterator = this.#within(state, () => open()[Symbol.asyncIterator]());
          continue;
        }
        if (next.done === true) return;
        delivered = true;
        yield next.value;
      }
    } finally {
      await iterator.return?.();
    }
  }

  #firstEntry(sessionId: string): boolean {
    if (this.#opened.has(sessionId)) return false;
    this.#noteOpened(sessionId);
    return true;
  }

  #freshOpens(state: RequestState): boolean {
    return (
      this.#authentication.freshTokenPerSessionOpen && state.credential.refreshToken !== undefined
    );
  }

  #noteOpened(sessionId: string) {
    if (!this.#authentication.freshTokenPerSessionOpen) return;
    this.#opened.delete(sessionId);
    this.#opened.add(sessionId);
    if (this.#opened.size > maximumOpenedSessions) {
      const oldest = this.#opened.values().next().value;
      if (oldest !== undefined) this.#opened.delete(oldest);
    }
  }

  /** A failed refresh ends the session the way a rejected bearer does today. */
  async #refresh(state: RequestState): Promise<void> {
    try {
      state.credential = await this.#authentication.refresh(state.context, state.credential);
      state.refreshed = true;
    } catch {
      throw new SdkAuthenticationError("The Studio session could not be refreshed", {
        transport: "local",
      });
    }
  }

  #retryable(state: RequestState, error: unknown): boolean {
    return (
      error instanceof MecatlError &&
      error.code === "unauthenticated" &&
      state.credential.refreshToken !== undefined
    );
  }

  /**
   * Runs `operation` with the request's current bearer. Until a refresh, that
   * is the one the session gate bound. After one, `runWithCredential` rebinds
   * it; it runs its callback synchronously, so `operation`'s own result (a
   * promise or an iterator) is captured and returned as is.
   */
  #within<T>(state: RequestState, operation: () => T): T {
    if (!state.refreshed) return operation();
    let outcome: { value: T } | { error: unknown } | undefined;
    this.#runtime
      .runWithCredential(state.credential.accessToken, async () => {
        try {
          outcome = { value: operation() };
        } catch (error) {
          outcome = { error };
          return;
        }
        // The context lasts as long as the operation; its result is handled by the caller.
        if (isPromiseLike(outcome.value)) await outcome.value;
      })
      .then(undefined, () => undefined);
    if (outcome === undefined) throw new Error("runWithCredential must run its callback at once");
    if ("error" in outcome) throw outcome.error;
    return outcome.value;
  }
}

function isAsyncIterable(value: unknown): value is AsyncIterable<unknown> {
  return typeof value === "object" && value !== null && Symbol.asyncIterator in value;
}

function isPromiseLike(value: unknown): value is PromiseLike<unknown> {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { then?: unknown }).then === "function"
  );
}
