import { createHash } from "node:crypto";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  createHttpTransport,
  type EventContentBlock,
  ProtocolError,
  SESSION_ID_HEADER_NAME,
  type Session,
  UnsupportedFeatureError,
} from "../src/index.js";

const sessionId = "pdf-download";
const artifactId = "a".repeat(48);
const pdf = new TextEncoder().encode(`%PDF-1.7\n${"private-content\n".repeat(25_000)}%%EOF`);
const digest = createHash("sha256").update(pdf).digest("hex");

describe("SDK PDF download", () => {
  it("projects typed tool-result metadata and streams ordered gRPC bytes", async () => {
    const observed: Array<{ sessionId: string; artifactId: string }> = [];
    const headers: Headers[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId }),
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: { artifacts: true },
          features: ["server_info"],
        }),
        converse: async function* () {
          yield {
            event: {
              runId: "run-pdf",
              type: "tool.result",
              toolResult: {
                callId: "call-pdf",
                blocks: [
                  {
                    artifactId,
                    kind: 7,
                    mimeType: "application/pdf",
                    name: "report.pdf",
                    sha256: digest,
                    size: BigInt(pdf.byteLength),
                  },
                ],
              },
            },
          };
          yield { event: { runId: "run-pdf", type: "result", result: { stop: "end_turn" } } };
        },
        downloadArtifact: async function* (request, context) {
          observed.push(request);
          headers.push(new Headers(context.requestHeader));
          yield { chunk: pdf.subarray(0, 256 * 1024) };
          yield { chunk: pdf.subarray(256 * 1024) };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    let block: EventContentBlock | undefined;
    for await (const event of await session.run("make a PDF")) {
      if (event.kind === "tool.result") block = event.payload.blocks[0];
    }
    expect(block).toMatchObject({
      artifactId,
      kind: 7,
      mimeType: "application/pdf",
      name: "report.pdf",
      sha256: digest,
      size: BigInt(pdf.byteLength),
    });
    expectTypeOf<Session["downloadArtifact"]>().toEqualTypeOf<
      (
        artifactId: string,
        requestOptions?: import("../src/index.js").RequestOptions,
      ) => AsyncIterable<Uint8Array>
    >();
    const chunks: Uint8Array[] = [];
    for await (const chunk of session.downloadArtifact(artifactId, {
      headers: { "x-caller": "kept" },
    }))
      chunks.push(chunk);
    expect(chunks).toHaveLength(2);
    expect(Buffer.concat(chunks)).toEqual(Buffer.from(pdf));
    expect(createHash("sha256").update(Buffer.concat(chunks)).digest("hex")).toBe(digest);
    expect(observed).toHaveLength(1);
    expect(observed[0]).toMatchObject({ artifactId, sessionId });
    expect(headers[0]?.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
    expect(headers[0]?.get("x-caller")).toBe("kept");
    await client.close();
  });

  it("cancels a gRPC download before returning its transport iterator", async () => {
    let abortedAtReturn = false;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId }),
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: { artifacts: true },
          features: ["server_info"],
        }),
        downloadArtifact: async function* () {
          yield { chunk: pdf.subarray(0, 10) };
          yield { chunk: pdf.subarray(10, 20) };
        },
      });
    });
    const baseStream = transport.stream.bind(transport);
    transport.stream = async (...args) => {
      const response = await baseStream(...args);
      if (args[0].name !== "DownloadArtifact") return response;
      const original = response.message[Symbol.asyncIterator]();
      return {
        ...response,
        message: {
          [Symbol.asyncIterator]() {
            return {
              next: () => original.next(),
              async return() {
                abortedAtReturn = args[1]?.aborted === true;
                return (await original.return?.()) ?? { done: true, value: undefined };
              },
            };
          },
        },
      };
    };
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const iterator = session.downloadArtifact(artifactId)[Symbol.asyncIterator]();
    expect((await iterator.next()).value).toEqual(pdf.subarray(0, 10));
    await iterator.return?.();
    expect(abortedAtReturn).toBe(true);
    await client.close();
  });

  it("streams HTTP through configured fetch and releases the body on early return", async () => {
    const requests: Array<{ url: URL; init: RequestInit }> = [];
    let cancelled = false;
    let pulls = 0;
    const pieces = [
      pdf.subarray(0, 100_000),
      pdf.subarray(100_000, 200_000),
      pdf.subarray(200_000),
    ];
    const fetch: typeof globalThis.fetch = async (input, init) => {
      const url = new URL(String(input));
      if (url.pathname === "/v1/compatibility")
        return Response.json({
          api_major: 1,
          capabilities: { artifacts: true },
          features: ["server_info"],
        });
      if (url.pathname === "/v1/sessions") return Response.json({ session_id: sessionId });
      requests.push({ url, init: init ?? {} });
      const body = new ReadableStream<Uint8Array>(
        {
          pull(controller) {
            const piece = pieces[pulls];
            if (piece === undefined) {
              controller.close();
              return;
            }
            pulls++;
            controller.enqueue(piece);
          },
          cancel() {
            cancelled = true;
          },
        },
        { highWaterMark: 0 },
      );
      return new Response(body, { headers: { "content-type": "application/pdf" } });
    };
    const client = connect({
      transport: createHttpTransport({
        baseUrl: "https://mecatl.test",
        fetch,
        credentials: "include",
        headers: { "x-configured": "yes" },
      }),
    });
    const session = await client.sessions.create({});
    const iterator = session
      .downloadArtifact(artifactId, { headers: { "x-caller": "kept" } })
      [Symbol.asyncIterator]();
    expect(requests).toHaveLength(0);
    const first = await iterator.next();
    expect(first.value).toEqual(pieces[0]);
    expect(pulls).toBeLessThan(pieces.length);
    expect(requests[0]?.url.pathname).toBe(`/v1/sessions/${sessionId}/artifacts/${artifactId}`);
    expect(requests[0]?.init.credentials).toBe("include");
    expect(new Headers(requests[0]?.init.headers).get("x-configured")).toBe("yes");
    expect(new Headers(requests[0]?.init.headers).get("x-caller")).toBe("kept");
    await iterator.return?.();
    expect(cancelled).toBe(true);

    pulls = 0;
    cancelled = false;
    const all: Uint8Array[] = [];
    for await (const chunk of session.downloadArtifact(artifactId)) all.push(chunk);
    expect(Buffer.concat(all)).toEqual(Buffer.from(pdf));
    expect(createHash("sha256").update(Buffer.concat(all)).digest("hex")).toBe(digest);
    await client.close();
  });

  it("rejects a false PDF content type and closes the HTTP response body", async () => {
    let cancelled = false;
    const fetch: typeof globalThis.fetch = async (input) => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/compatibility") {
        return Response.json({
          api_major: 1,
          capabilities: { artifacts: true },
          features: ["server_info"],
        });
      }
      if (path === "/v1/sessions") return Response.json({ session_id: sessionId });
      return new Response(
        new ReadableStream<Uint8Array>({
          cancel() {
            cancelled = true;
          },
        }),
        { headers: { "content-type": "application/pdf-malformed" } },
      );
    };
    const client = connect({ baseUrl: "https://mecatl.test", fetch });
    const session = await client.sessions.create({});
    await expect(
      session.downloadArtifact(artifactId)[Symbol.asyncIterator]().next(),
    ).rejects.toBeInstanceOf(ProtocolError);
    expect(cancelled).toBe(true);
    await client.close();
  });

  it("fails with a typed unsupported feature before requesting an old server", async () => {
    let calls = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        downloadArtifact: async function* () {
          calls++;
          yield { chunk: pdf };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const next = session.downloadArtifact(artifactId)[Symbol.asyncIterator]().next();
    await expect(next).rejects.toBeInstanceOf(UnsupportedFeatureError);
    expect(calls).toBe(0);
    await client.close();
  });
});
