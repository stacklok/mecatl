import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type Client,
  connect,
  createHttpTransport,
  PromptValidationError,
  pdfPart,
  SESSION_ID_HEADER_NAME,
  UnsupportedFeatureError,
} from "../src/index.js";

const pdf = new Blob(["%PDF-1.7\n%%EOF"], { type: "application/pdf" });
const artifactId = "successor-artifact";
const sha256 = "a".repeat(64);
const bytes = new Uint8Array([1, 2, 3]);

interface Fixture {
  readonly client: Client;
  readonly reads: Array<{ sessionId: string; affinity: string | null }>;
  readonly uploads: string[];
  readonly downloads: string[];
}

function fixture(kind: "grpc" | "http", artifacts: boolean): Fixture {
  const reads: Fixture["reads"] = [];
  const uploads: string[] = [];
  const downloads: string[] = [];
  if (kind === "grpc") {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "source", sessionCapabilities: { pdf: false } }),
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: { artifacts },
          features: ["server_info"],
        }),
        forkSession: () => ({ sessionId: "forked" }),
        clearSession: () => ({ sessionId: "cleared" }),
        getSession: (request, context) => {
          reads.push({
            sessionId: request.sessionId,
            affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
          });
          return {
            session: {
              sessionId: request.sessionId,
              sessionCapabilities: { pdf: request.sessionId === "forked" },
            },
          };
        },
        uploadArtifact: async (requests) => {
          for await (const request of requests) {
            if (request.payload.case === "metadata") uploads.push(request.payload.value.sessionId);
          }
          return { artifactId, name: "report.pdf", mimeType: "application/pdf", sha256, size: 14n };
        },
        downloadArtifact: async function* (request) {
          downloads.push(request.sessionId);
          yield { chunk: bytes };
        },
      });
    });
    return { client: connect({ transport, transportKind: "grpc" }), reads, uploads, downloads };
  }

  const transport = createHttpTransport({
    baseUrl: "https://mecatl.test",
    fetch: async (input, init) => {
      const path = new URL(String(input)).pathname;
      const affinity = new Headers(init?.headers).get(SESSION_ID_HEADER_NAME);
      if (path === "/v1/compatibility") {
        return Response.json({
          api_major: 1,
          capabilities: { artifacts: artifacts },
          features: ["server_info"],
        });
      }
      if (path === "/v1/sessions") {
        return Response.json(
          { session_id: "source", session_capabilities: { pdf: false } },
          { status: 201 },
        );
      }
      if (path.endsWith("/fork")) return Response.json({ session_id: "forked" }, { status: 201 });
      if (path.endsWith("/clear")) return Response.json({ session_id: "cleared" }, { status: 201 });
      if (path.endsWith("/artifacts")) {
        uploads.push(affinity ?? "");
        if (init?.body === undefined) throw new Error("PDF upload has no request body");
        await (init.body as ReadableStream<Uint8Array>).pipeTo(new WritableStream());
        return Response.json({
          artifact_id: artifactId,
          name: "report.pdf",
          mime_type: "application/pdf",
          sha256,
          size: 14,
        });
      }
      if (path.includes("/artifacts/")) {
        downloads.push(path.split("/")[3] ?? "");
        return new Response(bytes, { headers: { "content-type": "application/pdf" } });
      }
      if (path === "/v1/sessions/forked" || path === "/v1/sessions/cleared") {
        const sessionId = path.split("/").at(-1) ?? "";
        reads.push({ sessionId, affinity });
        return Response.json({
          session_id: sessionId,
          session_capabilities: { pdf: sessionId === "forked" },
        });
      }
      throw new Error(`unexpected request ${path}`);
    },
  });
  return { client: connect({ transport, transportKind: "http" }), reads, uploads, downloads };
}

describe.each(["grpc", "http"] as const)("%s PDF successor handles", (kind) => {
  it("uses each successor's model capability and the server artifact capability", async () => {
    const { client, reads, uploads, downloads } = fixture(kind, true);
    const source = await client.sessions.create({});
    const forked = await client.sessions.fork(source.id, { modelId: "pdf-model" });
    const cleared = await forked.clear();

    await expect(
      source.uploadArtifact(pdf, { name: "report.pdf", mimeType: "application/pdf" }),
    ).resolves.toMatchObject({ artifactId });
    await expect(
      forked.uploadArtifact(pdf, { name: "report.pdf", mimeType: "application/pdf" }),
    ).resolves.toEqual({
      artifactId,
      name: "report.pdf",
      mimeType: "application/pdf",
      size: 14n,
      sha256,
    });
    await expect(
      cleared.uploadArtifact(pdf, { name: "report.pdf", mimeType: "application/pdf" }),
    ).resolves.toMatchObject({ artifactId });
    await expect(cleared.run([pdfPart(artifactId)])).rejects.toBeInstanceOf(PromptValidationError);

    const chunks: Uint8Array[] = [];
    for await (const chunk of cleared.downloadArtifact(artifactId)) chunks.push(chunk);
    expect(chunks).toEqual([bytes]);

    expect((await forked.snapshot()).sessionCapabilities?.pdf).toBe(true);
    expect((await cleared.snapshot()).sessionCapabilities?.pdf).toBe(false);
    await expect(
      forked.uploadArtifact(pdf, { name: "report.pdf", mimeType: "application/pdf" }),
    ).resolves.toEqual({
      artifactId,
      name: "report.pdf",
      mimeType: "application/pdf",
      size: 14n,
      sha256,
    });
    chunks.length = 0;
    for await (const chunk of cleared.downloadArtifact(artifactId)) chunks.push(chunk);
    expect(chunks).toEqual([bytes]);

    expect(reads).toEqual([
      { sessionId: "forked", affinity: "forked" },
      { sessionId: "cleared", affinity: "cleared" },
      { sessionId: "forked", affinity: "forked" },
      { sessionId: "cleared", affinity: "cleared" },
    ]);
    expect(uploads).toEqual(["source", "forked", "cleared", "forked"]);
    expect(downloads).toEqual(["cleared", "cleared"]);
    await client.close();
  });

  it("keeps server artifact support authoritative on successor handles", async () => {
    const { client, uploads, downloads } = fixture(kind, false);
    const source = await client.sessions.create({});
    const forked = await client.sessions.fork(source.id, { modelId: "pdf-model" });
    const cleared = await forked.clear();

    await expect(
      forked.uploadArtifact(pdf, { name: "report.pdf", mimeType: "application/pdf" }),
    ).rejects.toBeInstanceOf(UnsupportedFeatureError);
    await expect(
      forked.downloadArtifact(artifactId)[Symbol.asyncIterator]().next(),
    ).rejects.toBeInstanceOf(UnsupportedFeatureError);
    await expect(
      cleared.downloadArtifact(artifactId)[Symbol.asyncIterator]().next(),
    ).rejects.toBeInstanceOf(UnsupportedFeatureError);
    expect(uploads).toEqual([]);
    expect(downloads).toEqual([]);
    await client.close();
  });
});
