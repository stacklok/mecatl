import type { BrowserContext, CDPSession, Page, Request } from "@playwright/test";

import { expect, test } from "./fixtures.js";

async function openFixture(page: Page, origin: string): Promise<void> {
  await page.goto(origin, { waitUntil: "domcontentloaded" });
}

function headerTokens(value: string | undefined): Set<string> {
  return new Set(
    (value ?? "")
      .split(/[,\n]/u)
      .map((token) => token.trim().toLowerCase())
      .filter(Boolean),
  );
}

interface PreflightObservation {
  readonly origin: string;
  readonly requestedHeaders: Set<string>;
  readonly requestedMethod: string;
  readonly requestId: string;
  responseHeaders?: Record<string, string>;
  status?: number;
}

interface CDPRequestEvent {
  request: {
    headers: Record<string, string | number>;
    method: string;
    url: string;
  };
  requestId: string;
}

interface CDPResponseEvent {
  requestId: string;
  response: {
    headers: Record<string, string | number>;
    status: number;
    url: string;
  };
}

interface CDPResponseExtraInfoEvent {
  headers: Record<string, string | number>;
  requestId: string;
  statusCode: number;
}

function normalizedHeaders(headers: Record<string, string | number>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(headers).map(([name, value]) => [name.toLowerCase(), String(value)]),
  );
}

async function observePreflights(
  context: BrowserContext,
  page: Page,
  baseUrl: string,
): Promise<{ close(): Promise<void>; observations: PreflightObservation[] }> {
  const session: CDPSession = await context.newCDPSession(page);
  const observations: PreflightObservation[] = [];
  const responses = new Map<
    string,
    { headers: Record<string, string>; raw: boolean; status: number }
  >();
  const applyResponse = (requestId: string): void => {
    const observation = observations.find((candidate) => candidate.requestId === requestId);
    const response = responses.get(requestId);
    if (observation === undefined || response === undefined) return;
    observation.responseHeaders = response.headers;
    observation.status = response.status;
  };
  session.on("Network.requestWillBeSent", (event: CDPRequestEvent) => {
    if (event.request.method !== "OPTIONS" || new URL(event.request.url).origin !== baseUrl) return;
    const headers = normalizedHeaders(event.request.headers);
    observations.push({
      origin: headers.origin ?? "",
      requestedHeaders: headerTokens(headers["access-control-request-headers"]),
      requestedMethod: headers["access-control-request-method"]?.toUpperCase() ?? "",
      requestId: event.requestId,
    });
    applyResponse(event.requestId);
  });
  session.on("Network.responseReceived", (event: CDPResponseEvent) => {
    const prior = responses.get(event.requestId);
    if (prior?.raw === true) return;
    responses.set(event.requestId, {
      headers: normalizedHeaders(event.response.headers),
      raw: false,
      status: event.response.status,
    });
    applyResponse(event.requestId);
  });
  session.on("Network.responseReceivedExtraInfo", (event: CDPResponseExtraInfoEvent) => {
    responses.set(event.requestId, {
      headers: normalizedHeaders(event.headers),
      raw: true,
      status: event.statusCode,
    });
    applyResponse(event.requestId);
  });
  await session.send("Network.enable");
  return {
    close: () => session.detach(),
    observations,
  };
}

function findPreflight(
  observations: PreflightObservation[],
  origin: string,
  method: string,
): PreflightObservation | undefined {
  for (const observation of observations) {
    if (observation.origin === origin && observation.requestedMethod === method) return observation;
  }
  return undefined;
}

function requestJSON(request: Request): Record<string, unknown> {
  const body = request.postData();
  if (body === null) throw new Error(`request ${request.url()} did not carry a JSON body`);
  return JSON.parse(body) as Record<string, unknown>;
}

test("Chromium completes the browser SDK control flow", async ({ browserHarness, page }) => {
  await openFixture(page, browserHarness.origin);

  const observed = await page.evaluate(
    async ({ baseUrl }) => {
      const packageName: string = "@stacklok/mecatl-sdk";
      const sdk = (await import(packageName)) as typeof import("../../src/index.js");
      const client = sdk.connect({ baseUrl, credentials: "include" });
      let result:
        | {
            entrypoint: { blobHelper: boolean; nodeSpawn: boolean };
            nodeGlobals: { buffer: boolean; process: boolean };
            runId: string;
            sessionId: string;
            status: string;
            stopReason: string;
            terminalKind: string;
            text: string;
          }
        | undefined;
      try {
        const session = await client.sessions.create({});
        const run = await session.run("complete the Chromium control flow");
        const terminal = await run.result();
        result = {
          entrypoint: {
            blobHelper: "imagePartFromBlob" in sdk,
            nodeSpawn: "spawn" in sdk,
          },
          nodeGlobals: {
            buffer: "Buffer" in globalThis,
            process: "process" in globalThis,
          },
          runId: terminal.runId,
          sessionId: terminal.sessionId,
          status: client.status.getSnapshot(),
          stopReason: terminal.stopReason,
          terminalKind: terminal.rawEvent.kind,
          text: terminal.text,
        };
        await session.delete();
      } finally {
        await client.close();
      }
      return { ...result, closed: true };
    },
    { baseUrl: browserHarness.baseUrl },
  );

  expect(observed).toMatchObject({
    closed: true,
    entrypoint: { blobHelper: true, nodeSpawn: false },
    nodeGlobals: { buffer: false, process: false },
    status: "online",
    stopReason: "end_turn",
    terminalKind: "result",
    text: "Chromium browser control flow completed",
  });
  expect(observed.runId).not.toBe("");
  expect(observed.sessionId).not.toBe("");
});

test("the browser transport passes exact-origin CORS preflight", async ({
  browserHarness,
  context,
  page,
}) => {
  await openFixture(page, browserHarness.origin);
  const preflights = await observePreflights(context, page, browserHarness.baseUrl);
  const allowed = await page.evaluate(
    async ({ baseUrl }) => {
      const packageName: string = "@stacklok/mecatl-sdk";
      const { connect } = (await import(packageName)) as typeof import("../../src/index.js");
      const client = connect({
        baseUrl,
        credentials: "include",
        headers: { "x-browser-proof": "chromium" },
      });
      try {
        const session = await client.sessions.create({});
        await session.delete();
        return client.status.getSnapshot();
      } finally {
        await client.close();
      }
    },
    { baseUrl: browserHarness.baseUrl },
  );
  expect(allowed).toBe("online");

  const allowedPreflight = findPreflight(preflights.observations, browserHarness.origin, "POST");
  expect(allowedPreflight, "session creation did not issue a browser preflight").toBeDefined();
  if (allowedPreflight === undefined) return;
  const allowedHeaders = allowedPreflight.responseHeaders ?? {};
  expect(allowedPreflight.status).toBe(204);
  expect(allowedPreflight.requestedHeaders).toEqual(new Set(["content-type", "x-browser-proof"]));
  expect(allowedHeaders["access-control-allow-origin"]).toBe(browserHarness.origin);
  expect(allowedHeaders["access-control-allow-credentials"]).toBe("true");
  expect(headerTokens(allowedHeaders["access-control-allow-methods"])).toEqual(
    new Set(["delete", "get", "options", "post", "put"]),
  );
  expect(headerTokens(allowedHeaders["access-control-allow-headers"])).toEqual(
    new Set(["content-type", "x-browser-proof"]),
  );
  expect(headerTokens(allowedHeaders.vary)).toEqual(
    new Set(["access-control-request-headers", "origin"]),
  );

  const beforeSibling = preflights.observations.length;
  await openFixture(page, browserHarness.siblingOrigin);
  const refused = await page.evaluate(
    async ({ baseUrl }) => {
      const packageName: string = "@stacklok/mecatl-sdk";
      const sdk = (await import(packageName)) as typeof import("../../src/index.js");
      const client = sdk.connect({
        baseUrl,
        credentials: "include",
        headers: { "x-browser-proof": "chromium" },
      });
      try {
        await client.sessions.create({});
        return { code: "", name: "unexpected success" };
      } catch (error) {
        return error instanceof sdk.MecatlError
          ? { code: error.code, name: error.constructor.name }
          : { code: "unknown", name: error instanceof Error ? error.name : String(error) };
      } finally {
        await client.close();
      }
    },
    { baseUrl: browserHarness.baseUrl },
  );
  expect(refused).toEqual({ code: "transport", name: "TransportError" });

  const refusedPreflight = findPreflight(
    preflights.observations.slice(beforeSibling),
    browserHarness.siblingOrigin,
    "GET",
  );
  expect(refusedPreflight, "sibling origin did not reach the refused preflight").toBeDefined();
  if (refusedPreflight === undefined) return;
  const refusedHeaders = refusedPreflight.responseHeaders ?? {};
  expect(refusedPreflight.status).toBe(204);
  expect(refusedHeaders["access-control-allow-origin"]).toBeUndefined();
  expect(refusedHeaders["access-control-allow-credentials"]).toBeUndefined();
  expect(headerTokens(refusedHeaders.vary)).toContain("origin");
  await preflights.close();
});

test("Chromium runs a multimodal prompt and permission callback", async ({
  browserHarness,
  page,
}) => {
  const promptRequests: Request[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname.endsWith("/prompt")) {
      promptRequests.push(request);
    }
  });
  await openFixture(page, browserHarness.origin);

  const observed = await page.evaluate(
    async ({ baseUrl }) => {
      const packageName: string = "@stacklok/mecatl-sdk";
      const sdk = (await import(packageName)) as typeof import("../../src/index.js");
      const browserFetch = globalThis.fetch.bind(globalThis);
      const imageCapableMockFetch: typeof globalThis.fetch = async (input, init) => {
        const response = await browserFetch(input, init);
        const inputUrl =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        if (
          new URL(inputUrl).pathname === "/v1/sessions" &&
          init?.method === "POST" &&
          response.ok
        ) {
          // The offline mock-script provider intentionally advertises no media capability.
          // This test-only echo override opens the SDK gate while the real daemon still
          // receives and validates the actual multimodal HTTP request below.
          const body = (await response.json()) as {
            session_capabilities?: { audio?: boolean; image?: boolean };
          };
          body.session_capabilities = { ...body.session_capabilities, image: true };
          const headers = new Headers(response.headers);
          headers.delete("content-length");
          return new Response(JSON.stringify(body), {
            headers,
            status: response.status,
            statusText: response.statusText,
          });
        }
        return response;
      };
      const client = sdk.connect({
        baseUrl,
        credentials: "include",
        fetch: imageCapableMockFetch,
      });
      const asks: Array<{ askId: string; tool: string }> = [];
      try {
        const session = await client.sessions.create({});
        const image = await sdk.imagePartFromBlob(
          new Blob([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], {
            type: "image/png",
          }),
        );
        const run = await session.run([sdk.textPart("inspect this in-memory image"), image], {
          onPermissionAsk: (ask) => {
            asks.push({ askId: ask.askId, tool: ask.tool });
            return "allow_once";
          },
        });
        const terminal = await run.result();
        await session.delete();
        return {
          asks,
          runId: terminal.runId,
          stopReason: terminal.stopReason,
          terminalKind: terminal.rawEvent.kind,
          text: terminal.text,
        };
      } finally {
        await client.close();
      }
    },
    { baseUrl: browserHarness.baseUrl },
  );

  expect(observed).toMatchObject({
    asks: [{ tool: "Write" }],
    stopReason: "end_turn",
    terminalKind: "result",
    text: "Chromium multimodal permission flow completed",
  });
  expect(observed.asks[0]?.askId).toContain("browser-multimodal-write");
  expect(promptRequests).toHaveLength(1);
  const prompt = requestJSON(promptRequests[0] as Request);
  expect(prompt.text).toBe("inspect this in-memory image");
  expect(prompt.parts).toEqual([
    {
      data: "iVBORw0KGgo=",
      kind: "image",
      mime_type: "image/png",
      url: "",
    },
  ]);
});

test("Chromium cancels steers when advertised and reattaches", async ({ browserHarness, page }) => {
  const controlRequests: Request[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (request.method() === "POST" && (path.endsWith("/cancel") || path.endsWith("/steer"))) {
      controlRequests.push(request);
    }
  });
  await openFixture(page, browserHarness.origin);

  const observed = await page.evaluate(
    async ({ baseUrl }) => {
      const packageName: string = "@stacklok/mecatl-sdk";
      const sdk = (await import(packageName)) as typeof import("../../src/index.js");
      const compatibilityResponse = await fetch(`${baseUrl}/v1/compatibility`, {
        credentials: "include",
      });
      const compatibility = (await compatibilityResponse.json()) as { features?: string[] };
      const steerAdvertised = compatibility.features?.includes("http_steer") === true;
      const client = sdk.connect({ baseUrl, credentials: "include" });
      try {
        const cancelSession = await client.sessions.create({});
        const cancelledRun = await cancelSession.run("cancel the owned Chromium run");
        await cancelledRun.cancel();
        const cancelled = await cancelledRun.result();
        await cancelSession.delete();

        const steerSession = await client.sessions.create({});
        const steeredRun = await steerSession.run("exercise strict browser steer");
        await steeredRun.steer("keep this instruction on the advertised run only");
        let steer:
          | { kind: "supported"; runId: string; stopReason: string }
          | { code: string; feature: string; kind: "unsupported"; runId: string };
        try {
          const result = await steeredRun.result();
          steer = {
            kind: "supported",
            runId: result.runId,
            stopReason: result.stopReason,
          };
        } catch (error) {
          if (!(error instanceof sdk.UnsupportedFeatureError)) throw error;
          steer = {
            code: error.code,
            feature: error.feature,
            kind: "unsupported",
            runId: steeredRun.id,
          };
        }
        if (!steerAdvertised) {
          await new Promise((resolveWait) => setTimeout(resolveWait, 900));
        }
        await steerSession.delete();

        const attachedSession = await client.sessions.create({});
        const persistedRun = await attachedSession.run("persist and attach this Chromium run");
        const ownedResult = persistedRun.result();
        const attached = await attachedSession.attach(persistedRun.id);
        const phases: string[] = [];
        let attachedTerminal: { runId: string; stopReason: string; text: string } | undefined;
        for await (const envelope of attached) {
          phases.push(envelope.phase);
          if (envelope.kind === "event" && envelope.event.kind === "result") {
            attachedTerminal = {
              runId: envelope.event.runId,
              stopReason: envelope.event.payload.stop,
              text: envelope.event.payload.text,
            };
          }
        }
        const owned = await ownedResult;
        await attachedSession.delete();

        return {
          attachedTerminal,
          cancelled: {
            runId: cancelled.runId,
            stopReason: cancelled.stopReason,
          },
          ownedAttached: {
            runId: owned.runId,
            stopReason: owned.stopReason,
          },
          phases,
          steer,
          steerAdvertised,
        };
      } finally {
        await client.close();
      }
    },
    { baseUrl: browserHarness.baseUrl },
  );

  expect(observed.cancelled.stopReason).toBe("cancelled");
  const cancelRequest = controlRequests.find((request) =>
    new URL(request.url()).pathname.endsWith("/cancel"),
  );
  expect(cancelRequest, "owned cancellation did not reach the HTTP control route").toBeDefined();
  if (cancelRequest === undefined) return;
  expect(requestJSON(cancelRequest).expected_run_id).toBe(observed.cancelled.runId);

  if (observed.steerAdvertised) {
    expect(observed.steer).toMatchObject({ kind: "supported", stopReason: "end_turn" });
    const steerRequest = controlRequests.find((request) =>
      new URL(request.url()).pathname.endsWith("/steer"),
    );
    expect(steerRequest, "advertised strict steer did not reach the HTTP route").toBeDefined();
    if (steerRequest !== undefined) {
      expect(requestJSON(steerRequest).expected_run_id).toBe(observed.steer.runId);
    }
  } else {
    expect(observed.steer).toMatchObject({
      code: "unsupported_feature",
      feature: "http_steer",
      kind: "unsupported",
    });
    expect(
      controlRequests.some((request) => new URL(request.url()).pathname.endsWith("/steer")),
    ).toBe(false);
  }

  expect(observed.attachedTerminal).toEqual({
    runId: observed.ownedAttached.runId,
    stopReason: "end_turn",
    text: "persisted Chromium run completed",
  });
  expect(observed.ownedAttached.stopReason).toBe("end_turn");
  expect(observed.phases).toContain("replay");
  expect(observed.phases).toContain("live");
});
