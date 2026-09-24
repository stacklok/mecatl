import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it, vi } from "vitest";

import { HarnessService, SteerOutcome } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  createHttpTransport,
  PromptValidationError,
  pdfPart,
  type Run,
  SESSION_ID_HEADER_NAME,
  textPart,
  UnsupportedFeatureError,
} from "../src/index.js";

const sessionId = "pdf-session";
const artifactId = "pdf-artifact";
const sha256 = "a".repeat(64);

function terminal(runId: string) {
  return { event: { result: { stop: "end_turn", text: "done" }, runId, type: "result" } };
}

describe("SDK PDF input", () => {
  it("streams bounded gRPC upload frames and sends only a reference in prompt and steer", async () => {
    const uploaded: Array<{ payload: { case: string | undefined; value?: unknown } }> = [];
    const uploadHeaders: Headers[] = [];
    let prompt: unknown;
    let steer: unknown;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId, sessionCapabilities: { pdf: true } }),
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: { pdfArtifacts: true },
          features: ["prompt_free_controls"],
        }),
        uploadPdf: async (requests, context) => {
          uploadHeaders.push(new Headers(context.requestHeader));
          for await (const request of requests) uploaded.push(request);
          return { artifactId, name: "report.pdf", sha256, size: 300_003n };
        },
        converse: async function* (requests) {
          prompt = (await requests[Symbol.asyncIterator]().next()).value;
          yield terminal("run-pdf");
        },
        steerRun: (request) => {
          steer = request;
          return { messageId: request.messageId, outcome: SteerOutcome.ACCEPTED, runId: "run-pdf" };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const first = new Uint8Array(300_001).fill(7);
    const last = new Uint8Array([8, 9]);
    async function* source(): AsyncIterable<Uint8Array> {
      yield first;
      yield new Uint8Array();
      yield last;
    }
    const pdf = await session.uploadPdf(
      source(),
      { name: "report.pdf" },
      {
        headers: { "x-caller": "kept" },
        timeoutMs: 4_321,
      },
    );
    expect(pdf).toEqual({ kind: "pdf", artifactId });
    expect(uploadHeaders[0]?.get("x-caller")).toBe("kept");
    expect(uploadHeaders[0]?.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
    expect(uploaded[0]).toMatchObject({
      payload: {
        case: "metadata",
        value: { mimeType: "application/pdf", name: "report.pdf", sessionId },
      },
    });
    const chunks = uploaded.slice(1).map((request) => {
      expect(request.payload.case).toBe("chunk");
      return request.payload.value as Uint8Array;
    });
    expect(chunks.length).toBeGreaterThan(1);
    expect(chunks.every((chunk) => chunk.byteLength > 0 && chunk.byteLength <= 256 * 1024)).toBe(
      true,
    );
    expect(Buffer.concat(chunks)).toEqual(Buffer.concat([first, last]));

    await (await session.run([textPart("inspect"), pdf])).result();
    await session.controls("run-pdf").steer([pdf], { messageId: "steer-pdf" });
    for (const part of [
      (prompt as { kind: { value: { parts: unknown[] } } }).kind.value.parts[0],
      (steer as { parts: unknown[] }).parts[0],
    ]) {
      expect(part).toMatchObject({
        artifactId,
        data: new Uint8Array(),
        kind: 3,
        mimeType: "application/pdf",
        url: "",
      });
      expect(part).not.toHaveProperty("name", "report.pdf");
    }
    expectTypeOf<Run["steer"]>().toEqualTypeOf<(text: string) => Promise<void>>();
    await client.close();
  });

  it("rejects unsafe names, forged references, and absent PDF capability before upload", async () => {
    let uploadCalls = 0;
    let steerCalls = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId, sessionCapabilities: { pdf: false } }),
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: { pdfArtifacts: true },
          features: ["prompt_free_controls"],
        }),
        uploadPdf: async () => {
          uploadCalls += 1;
          return { artifactId };
        },
        steerRun: () => {
          steerCalls += 1;
          return { outcome: SteerOutcome.ACCEPTED, runId: "run-pdf" };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const source = new Blob(["%PDF-1.7\n%%EOF"], { type: "application/pdf" });

    expect(() => pdfPart("")).toThrowError(PromptValidationError);
    expect(() => pdfPart(" \n ")).toThrowError(PromptValidationError);
    await expect(session.uploadPdf(source, { name: "../report.pdf" })).rejects.toBeInstanceOf(
      PromptValidationError,
    );
    await expect(session.uploadPdf(source, { name: "report.pdf" })).rejects.toMatchObject({
      reason: "capability",
    });
    await expect(session.run([pdfPart(artifactId)])).rejects.toMatchObject({
      reason: "capability",
    });
    await expect(session.controls("run-pdf").steer([pdfPart(artifactId)])).rejects.toMatchObject({
      reason: "capability",
    });
    expect(uploadCalls).toBe(0);
    expect(steerCalls).toBe(0);
    await client.close();

    const oldTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId, sessionCapabilities: { pdf: true } }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
      });
    });
    const oldClient = connect({ transport: oldTransport });
    const oldSession = await oldClient.sessions.create({});
    await expect(oldSession.uploadPdf(source, { name: "report.pdf" })).rejects.toBeInstanceOf(
      UnsupportedFeatureError,
    );
    await expect(oldSession.run([pdfPart(artifactId)])).rejects.toBeInstanceOf(
      UnsupportedFeatureError,
    );
    await oldClient.close();
  });

  it("writes only the PDF artifact reference in HTTP prompt and steer JSON", async () => {
    const bodies = new Map<string, unknown>();
    const fetch: typeof globalThis.fetch = async (input, init) => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/compatibility") {
        return Response.json({
          api_major: 1,
          capabilities: { pdf_artifacts: true },
          features: ["prompt_free_controls"],
        });
      }
      if (path === "/v1/sessions") {
        return Response.json({ session_id: sessionId, session_capabilities: { pdf: true } });
      }
      bodies.set(path, JSON.parse(String(init?.body)));
      if (path.endsWith("/prompt")) {
        return new Response(
          `data: ${JSON.stringify({ result: { stop: "end_turn", text: "done" }, run_id: "run-pdf", type: "result" })}\n\n`,
          { headers: { "content-type": "text/event-stream" } },
        );
      }
      if (path.endsWith("/controls/steer")) {
        return Response.json({ message_id: "", outcome: "accepted", run_id: "run-pdf" });
      }
      throw new Error(`unexpected path ${path}`);
    };
    const client = connect({ baseUrl: "https://mecatl.test", fetch });
    const session = await client.sessions.create({});
    const pdf = pdfPart(artifactId);
    await (await session.run([pdf])).result();
    await session.controls("run-pdf").steer([pdf]);
    const part = { artifact_id: artifactId, kind: "pdf", mime_type: "application/pdf" };
    expect(bodies.get(`/v1/sessions/${sessionId}/prompt`)).toEqual({ parts: [part], text: "" });
    expect(bodies.get(`/v1/sessions/${sessionId}/controls/steer`)).toEqual({
      expected_run_id: "run-pdf",
      message_id: "",
      parts: [part],
    });
    await client.close();
  });

  it("streams a Blob through configured HTTP fetch with credentials and no arrayBuffer call", async () => {
    const calls: Array<{ init: RequestInit; url: URL }> = [];
    const chunks: Uint8Array[] = [];
    const fetch: typeof globalThis.fetch = async (input, init) => {
      const url = new URL(String(input));
      if (url.pathname === "/v1/compatibility") {
        return Response.json({
          api_major: 1,
          capabilities: { pdf_artifacts: true },
          features: ["server_info"],
        });
      }
      if (url.pathname === "/v1/sessions") {
        return Response.json({ session_id: sessionId, session_capabilities: { pdf: true } });
      }
      calls.push({ init: init ?? {}, url });
      if (init?.body === undefined) throw new Error("PDF upload has no request body");
      const reader = (init.body as ReadableStream<Uint8Array>).getReader();
      try {
        for (;;) {
          const next = await reader.read();
          if (next.done) break;
          chunks.push(next.value);
        }
      } finally {
        reader.releaseLock();
      }
      return Response.json({ artifact_id: artifactId, name: "A & B.pdf", size: 300_003, sha256 });
    };
    const client = connect({
      transport: createHttpTransport({
        baseUrl: "https://mecatl.test",
        credentials: "include",
        credentialProvider: () => ({ authorization: "Bearer sample" }),
        fetch,
        headers: { "x-static": "configured" },
      }),
    });
    const session = await client.sessions.create({});
    const blob = new Blob([new Uint8Array(300_003)], { type: "application/pdf" });
    vi.spyOn(blob, "arrayBuffer").mockImplementation(() => {
      throw new Error("Blob.arrayBuffer() must not be called");
    });
    const pdf = await session.uploadPdf(
      blob,
      { name: "A & B.pdf" },
      {
        headers: { "x-caller": "kept" },
      },
    );
    expect(pdf).toEqual({ kind: "pdf", artifactId });
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url.pathname).toBe(`/v1/sessions/${sessionId}/pdfs`);
    expect(calls[0]?.url.searchParams.get("name")).toBe("A & B.pdf");
    expect(calls[0]?.init.credentials).toBe("include");
    expect(new Headers(calls[0]?.init.headers).get("content-type")).toBe("application/pdf");
    expect(new Headers(calls[0]?.init.headers).get("authorization")).toBe("Bearer sample");
    expect(new Headers(calls[0]?.init.headers).get("x-static")).toBe("configured");
    expect(new Headers(calls[0]?.init.headers).get("x-caller")).toBe("kept");
    expect(new Headers(calls[0]?.init.headers).get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
    expect(chunks.length).toBeGreaterThan(1);
    expect(chunks.every((chunk) => chunk.byteLength > 0 && chunk.byteLength <= 256 * 1024)).toBe(
      true,
    );
    expect(chunks.reduce((total, chunk) => total + chunk.byteLength, 0)).toBe(300_003);
    await client.close();
  });

  it("rejects malformed and oversized async sources without sending invalid chunks", async () => {
    const sentChunks: Uint8Array[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId, sessionCapabilities: { pdf: true } }),
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: { pdfArtifacts: true },
          features: ["server_info"],
        }),
        uploadPdf: async (requests) => {
          for await (const request of requests) {
            if (request.payload.case === "chunk") sentChunks.push(request.payload.value);
          }
          return { artifactId, name: "report.pdf", sha256, size: 1n };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    async function* malformed(): AsyncIterable<Uint8Array> {
      yield "not bytes" as unknown as Uint8Array;
    }
    await expect(session.uploadPdf(malformed(), { name: "report.pdf" })).rejects.toBeInstanceOf(
      PromptValidationError,
    );
    expect(sentChunks).toHaveLength(0);

    const piece = new Uint8Array(256 * 1024);
    async function* oversized(): AsyncIterable<Uint8Array> {
      for (let index = 0; index < 80; index += 1) yield piece;
      yield new Uint8Array([1]);
    }
    await expect(session.uploadPdf(oversized(), { name: "report.pdf" })).rejects.toMatchObject({
      reason: "size",
    });
    expect(sentChunks).toHaveLength(80);
    expect(sentChunks.every((chunk) => chunk.byteLength === piece.byteLength)).toBe(true);
    await client.close();
  });

  it("aborts a waiting gRPC upload and closes its caller-owned source", async () => {
    let sawChunk: () => void = () => undefined;
    const firstChunk = new Promise<void>((resolve) => {
      sawChunk = resolve;
    });
    let releaseSource: () => void = () => undefined;
    const sourceBlocked = new Promise<void>((resolve) => {
      releaseSource = resolve;
    });
    let sourceClosed = false;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId, sessionCapabilities: { pdf: true } }),
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: { pdfArtifacts: true },
          features: ["server_info"],
        }),
        uploadPdf: async (requests) => {
          for await (const request of requests) {
            if (request.payload.case === "chunk") sawChunk();
          }
          return { artifactId, name: "report.pdf", sha256, size: 1n };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const controller = new AbortController();
    async function* source(): AsyncIterable<Uint8Array> {
      try {
        yield new Uint8Array([1]);
        await sourceBlocked;
        yield new Uint8Array([2]);
      } finally {
        sourceClosed = true;
      }
    }
    const upload = session.uploadPdf(
      source(),
      { name: "report.pdf" },
      { signal: controller.signal },
    );
    await firstChunk;
    controller.abort(new Error("cancelled"));
    await expect(
      Promise.race([
        upload,
        new Promise<never>((_resolve, reject) => {
          setTimeout(() => reject(new Error("upload ignored cancellation")), 1_000);
        }),
      ]),
    ).rejects.not.toThrow("upload ignored cancellation");
    releaseSource();
    await vi.waitFor(() => expect(sourceClosed).toBe(true));
    await client.close();
  });

  it("honors HTTP request cancellation without pulling the rest of an async source", async () => {
    let pulls = 0;
    let returns = 0;
    let started: () => void = () => undefined;
    const firstChunk = new Promise<void>((resolve) => {
      started = resolve;
    });
    const controller = new AbortController();
    const fetch: typeof globalThis.fetch = async (input, init) => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/compatibility") {
        return Response.json({
          api_major: 1,
          capabilities: { pdf_artifacts: true },
          features: ["server_info"],
        });
      }
      if (path === "/v1/sessions") {
        return Response.json({ session_id: sessionId, session_capabilities: { pdf: true } });
      }
      if (init?.body === undefined) throw new Error("PDF upload has no request body");
      const reader = (init.body as ReadableStream<Uint8Array>).getReader();
      const first = await reader.read();
      expect(first.done).toBe(false);
      started();
      await new Promise<void>((_resolve, reject) => {
        if (init?.signal?.aborted) {
          reject(new Error("aborted fetch"));
          return;
        }
        init?.signal?.addEventListener("abort", () => reject(new Error("aborted fetch")), {
          once: true,
        });
      });
      throw new Error("unreachable");
    };
    const client = connect({ baseUrl: "https://mecatl.test", fetch });
    const session = await client.sessions.create({});
    const source: AsyncIterable<Uint8Array> = {
      [Symbol.asyncIterator]() {
        return {
          async next() {
            pulls += 1;
            return { done: false, value: new Uint8Array([1, 2, 3]) };
          },
          async return() {
            returns += 1;
            return { done: true, value: undefined };
          },
        };
      },
    };
    const upload = session.uploadPdf(source, { name: "report.pdf" }, { signal: controller.signal });
    await firstChunk;
    expect(pulls).toBe(1);
    controller.abort(new Error("cancelled"));
    await expect(upload).rejects.toBeDefined();
    expect(pulls).toBe(1);
    expect(returns).toBeGreaterThan(0);
    await client.close();
  });
});
