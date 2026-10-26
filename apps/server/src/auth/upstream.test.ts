// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { MecatlError } from "@stacklok-oss/mecatl-sdk";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createApp } from "../app.js";
import type { ChatService } from "../mecatl/chat.js";
import { type MecatlRuntime, type RuntimeConfig, startMecatlRuntime } from "../mecatl/runtime.js";
import { type FakeIssuerOptions, fakeIssuer, resourceUrl } from "../testing/fake-issuer.js";
import { cookieHeader, csrfHeaders, fakeClient } from "../testing/fakes.js";
import { type AuthenticationService, createAuthenticationService } from "./service.js";

const external: RuntimeConfig = { baseUrl: resourceUrl, resourceUrl, source: "external" };
const secret = "0123456789abcdef0123456789abcdef";
const publicUrl = new URL("https://studio.example.com");
const origin = publicUrl.origin;

const runtimes: MecatlRuntime[] = [];
afterEach(async () => {
  await Promise.all(runtimes.splice(0).map((runtime) => runtime.close()));
});

function rejected(): MecatlError {
  return new MecatlError("token rejected", {
    code: "unauthenticated",
    status: 16,
    transport: "grpc",
  });
}

/**
 * A signed-in Studio over the real runtime credential context: each fake chat
 * operation records the bearer the SDK would send at the moment it runs.
 */
async function signedIn(issuerOptions: FakeIssuerOptions, chat: Partial<ChatService> = {}) {
  const issuer = fakeIssuer({ rotateRefreshTokens: true, ...issuerOptions });
  let provider: (() => HeadersInit) | undefined;
  const runtime = await startMecatlRuntime(external, {
    createClient: async (_config, credentialProvider) => {
      provider = credentialProvider;
      return fakeClient();
    },
  });
  runtimes.push(runtime);
  const bearer = () => new Headers(provider?.()).get("authorization") ?? "none";
  const seen: Array<[operation: string, bearer: string]> = [];
  const record = (operation: string) => seen.push([operation, bearer()]);
  const authentication = (await createAuthenticationService(external, {
    fetch: issuer.fetch,
    publicUrl,
    sessionSecret: secret,
  })) as AuthenticationService;
  const fakeChat = {
    clearSession: async (sessionId: string) => {
      record(`clear:${sessionId}`);
      return { id: `cleared-${sessionId}` };
    },
    createSession: async () => {
      record("create");
      return { id: "created" };
    },
    forkSession: async (sessionId: string) => {
      record(`fork:${sessionId}`);
      return { id: `forked-${sessionId}` };
    },
    listSessions: async () => {
      record("list");
      return { complete: true, items: [] };
    },
    async *retry(sessionId: string) {
      record(`retry:${sessionId}`);
      yield { runId: "run-retry", sessionId, type: "run.started" } satisfies RunStreamEvent;
    },
    async *run(sessionId: string) {
      record(`run:${sessionId}`);
      yield { runId: "run-1", sessionId, type: "run.started" } satisfies RunStreamEvent;
    },
    ...chat,
  } as unknown as ChatService;
  const app = createApp({ authentication, chat: fakeChat, runtime, security: { publicUrl } });

  const login = await app.request(`${origin}/api/v1/auth/login`);
  const state = new URL(login.headers.get("location") ?? "").searchParams.get("state");
  const callback = await app.request(`${origin}/api/v1/auth/callback?code=code-1&state=${state}`, {
    headers: { Cookie: cookieHeader(login.headers.getSetCookie()) },
  });
  expect(callback.status).toBe(302);
  const session = cookieHeader(callback.headers.getSetCookie());

  const send = (path: string, body: unknown = {}, cookies = session) =>
    app.request(`${origin}/api/v1/sessions${path}`, {
      body: JSON.stringify(body),
      headers: csrfHeaders("t", {
        "Content-Type": "application/json",
        Cookie: `${cookies}; studio_csrf=t`,
      }),
      method: "POST",
    });
  const list = (cookies = session) =>
    app.request(`${origin}/api/v1/sessions`, { headers: { Cookie: cookies } });
  return { issuer, list, record, seen, send };
}

const fork = { model: { id: "model", providerId: "provider" }, reasoningEffort: "default" };
const prompt = { prompt: "Hello" };

function storedSession(response: Response): boolean {
  const cookies = response.headers.getSetCookie();
  return ["studio_access", "studio_refresh"].every((name) =>
    cookies.some((cookie) => cookie.startsWith(`${name}=`) && !cookie.includes("Max-Age=0")),
  );
}

function clearedSession(response: Response): boolean {
  const cookies = response.headers.getSetCookie();
  return ["studio_access", "studio_refresh"].every((name) =>
    cookies.some((cookie) => cookie.startsWith(`${name}=;`) && cookie.includes("Max-Age=0")),
  );
}

describe("upstream credentials", () => {
  it("on AS-1, every session open carries an access token no earlier request used", async () => {
    const studio = await signedIn({ identityBrokerSignIn: true });
    expect((await studio.list()).status).toBe(200);
    const [, loginBearer] = studio.seen[0] ?? [];

    for (const [path, body] of [
      ["", {}],
      ["/source/fork", fork],
      ["/source/clear", {}],
    ] as const) {
      const response = await studio.send(path, body);
      expect(response.status, path).toBe(201);
      expect(storedSession(response), path).toBe(true);
    }
    // The first run of a session another client (or an earlier process)
    // opened is fresh; later runs, and runs of sessions this process opened,
    // are not.
    for (const [path, body] of [
      ["/created/runs", prompt],
      ["/forked-source/retry", {}],
      ["/elsewhere/runs", prompt],
      ["/elsewhere/runs", prompt],
      ["/resumed/retry", {}],
      ["/resumed/runs", prompt],
    ] as const) {
      const response = await studio.send(path, body);
      expect(response.status, path).toBe(200);
      expect(await response.text(), path).toContain("run.started");
    }

    // Every request reused the login cookies, so one that did not refresh
    // presents the login token.
    expect(studio.seen.map(([operation, bearer]) => [operation, bearer !== loginBearer])).toEqual([
      ["list", false],
      ["create", true],
      ["fork:source", true],
      ["clear:source", true],
      ["run:created", false],
      ["retry:forked-source", false],
      ["run:elsewhere", true],
      ["run:elsewhere", false],
      ["retry:resumed", true],
      ["run:resumed", false],
    ]);
    const fresh = studio.seen.filter(([, bearer]) => bearer !== loginBearer);
    expect(new Set(fresh.map(([, bearer]) => bearer)).size).toBe(fresh.length);
    expect(studio.issuer.refreshCalls()).toBe(fresh.length);
  });

  it("without AS-1, opening a session never forces a refresh", async () => {
    const studio = await signedIn({});
    expect((await studio.send("", {})).status).toBe(201);
    expect((await studio.send("/source/fork", fork)).status).toBe(201);
    expect((await studio.send("/source/clear", {})).status).toBe(201);
    expect((await studio.send("/elsewhere/runs", prompt)).status).toBe(200);
    expect((await studio.send("/resumed/retry", {})).status).toBe(200);
    expect(studio.issuer.refreshCalls()).toBe(0);
    expect(new Set(studio.seen.map(([, bearer]) => bearer)).size).toBe(1);
  });

  it("a forced refresh skips the rotation grace window and follows the rotated refresh token", async () => {
    // Near expiry, so the first request refreshes the ordinary way and leaves
    // its credential in the grace window.
    const studio = await signedIn({ expiresIn: 10_000, identityBrokerSignIn: true });
    expect((await studio.list()).status).toBe(200);
    expect(studio.issuer.refreshCalls()).toBe(1);
    const [, cached] = studio.seen[0] ?? [];

    // Three opens from the stale login cookies, two of them concurrent: none
    // reuses the cached credential, none replays a spent refresh token, and
    // each stores what it was given.
    const responses = [
      await studio.send("", {}),
      ...(await Promise.all([studio.send("", {}), studio.send("/source/fork", fork)])),
    ];
    for (const response of responses) {
      expect(response.status).toBe(201);
      expect(storedSession(response)).toBe(true);
    }
    const opens = studio.seen.slice(1).map(([, bearer]) => bearer);
    expect(opens).not.toContain(cached);
    expect(new Set([cached, ...opens]).size).toBe(4);
    const presented = studio.issuer
      .tokenRequests()
      .filter((request) => request.grantType === "refresh_token")
      .map((request) => request.refreshToken);
    expect(new Set(presented).size).toBe(presented.length);
  });

  it("a rejected bearer is refreshed and the operation retried exactly once", async () => {
    const listSessions = vi.fn<ChatService["listSessions"]>();
    const studio = await signedIn({}, { listSessions });
    listSessions.mockImplementationOnce(async () => {
      studio.record("list");
      throw rejected();
    });
    listSessions.mockImplementationOnce(async () => {
      studio.record("list");
      return { complete: true, items: [] };
    });
    const recovered = await studio.list();
    expect(recovered.status).toBe(200);
    expect(listSessions).toHaveBeenCalledTimes(2);
    expect(studio.issuer.refreshCalls()).toBe(1);
    expect(storedSession(recovered)).toBe(true);
    const [first, second] = studio.seen.map(([, bearer]) => bearer);
    expect(second).not.toBe(first);

    listSessions.mockReset();
    listSessions.mockRejectedValue(rejected());
    const expired = await studio.list();
    expect(expired.status).toBe(401);
    await expect(expired.json()).resolves.toMatchObject({ code: "session_expired" });
    expect(listSessions).toHaveBeenCalledTimes(2);
    expect(studio.issuer.refreshCalls()).toBe(2);
    expect(clearedSession(expired)).toBe(true);
  });

  it("a refresh that fails ends the session without a retry", async () => {
    const listSessions = vi.fn<ChatService["listSessions"]>().mockRejectedValue(rejected());
    const studio = await signedIn({ refreshFails: true }, { listSessions });
    const expired = await studio.list();
    expect(expired.status).toBe(401);
    await expect(expired.json()).resolves.toMatchObject({ code: "session_expired" });
    expect(listSessions).toHaveBeenCalledTimes(1);
    expect(studio.issuer.refreshCalls()).toBe(1);
    expect(clearedSession(expired)).toBe(true);

    // On AS-1 the forced refresh before an open fails the same way, and the
    // open never runs.
    const createSession = vi.fn<ChatService["createSession"]>();
    const broker = await signedIn(
      { identityBrokerSignIn: true, refreshFails: true },
      {
        createSession,
      },
    );
    const refused = await broker.send("", {});
    expect(refused.status).toBe(401);
    await expect(refused.json()).resolves.toMatchObject({ code: "session_expired" });
    expect(createSession).not.toHaveBeenCalled();
    expect(clearedSession(refused)).toBe(true);
  });

  it("a run rejected before its first event is retried once, before the stream commits", async () => {
    let attempts = 0;
    const studio = await signedIn(
      {},
      {
        async *run(sessionId: string) {
          attempts += 1;
          if (attempts === 1) throw rejected();
          yield { runId: "run-1", sessionId, type: "run.started" };
        },
      },
    );
    const recovered = await studio.send("/session-1/runs", prompt);
    expect(recovered.status).toBe(200);
    expect(await recovered.text()).toContain('"runId":"run-1"');
    expect(attempts).toBe(2);
    expect(studio.issuer.refreshCalls()).toBe(1);
    // The refreshed tokens reach the browser even though the body streams.
    expect(storedSession(recovered)).toBe(true);
  });

  it("a run rejected twice ends the session; one rejected after its first event is reported in-band", async () => {
    let attempts = 0;
    const studio = await signedIn(
      {},
      {
        // biome-ignore lint/correctness/useYield: the stream fails before its first event
        async *run() {
          attempts += 1;
          throw rejected();
        },
        async *retry(sessionId: string) {
          attempts += 1;
          yield { runId: "run-retry", sessionId, type: "run.started" };
          throw rejected();
        },
      },
    );
    const expired = await studio.send("/session-1/runs", prompt);
    expect(expired.status).toBe(401);
    await expect(expired.json()).resolves.toMatchObject({ code: "session_expired" });
    expect(attempts).toBe(2);
    expect(studio.issuer.refreshCalls()).toBe(1);
    expect(clearedSession(expired)).toBe(true);

    attempts = 0;
    const streamed = await studio.send("/session-1/retry", {});
    expect(streamed.status).toBe(200);
    const body = await streamed.text();
    expect(body).toContain('"runId":"run-retry"');
    expect(body).toContain('"code":"unauthenticated"');
    expect(attempts).toBe(1);
    expect(studio.issuer.refreshCalls()).toBe(1);
  });
});
