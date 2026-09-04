import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { Transport } from "@connectrpc/connect";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  audioPart,
  audioPartFromBlob,
  connect,
  createHttpTransport,
  imagePart,
  imagePartFromBlob,
  MAX_MEDIA_PART_BYTES,
  type PromptPart,
  PromptValidationError,
  ServerError,
  textPart,
} from "../src/index.js";
import { audioPartFromPath, imagePartFromPath } from "../src/node.js";

function countedTransport(transport: Transport): {
  calls: { stream: number; unary: number };
  transport: Transport;
} {
  const calls = { stream: 0, unary: 0 };
  return {
    calls,
    transport: new Proxy(transport, {
      get(target, property, receiver) {
        const value = Reflect.get(target, property, receiver);
        if ((property === "stream" || property === "unary") && typeof value === "function") {
          return (...args: unknown[]) => {
            calls[property] += 1;
            return Reflect.apply(value, target, args);
          };
        }
        return value;
      },
    }),
  };
}

function terminal(runId: string) {
  return {
    event: {
      result: { stop: "end_turn", text: "done" },
      runId,
      type: "result",
    },
  };
}

// Import syntax only: prose in a generated doc comment can contain the word
// "from" followed by a quoted phrase, which a source-wide regex reads as an
// import and reports as a bogus violation.
function relativeSpecifiers(source: string): string[] {
  return importSpecifiers(source).filter((specifier) => specifier.startsWith("."));
}

function bareSpecifiers(source: string): string[] {
  return importSpecifiers(source).filter((specifier) => !specifier.startsWith("."));
}

function importSpecifiers(source: string): string[] {
  // A module specifier is only ever the operand of an import/export statement.
  // Scanning the whole file for `from "..."` also matches ordinary prose and
  // string literals — src/watch.ts really does contain the message
  // 'Opening activity from "now" requires an explicit run id'. So: drop
  // comments, then accumulate only statements that begin with import/export
  // and read the specifier out of the completed statement.
  const code = source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  const found: string[] = [];
  let statement = "";
  for (const line of code.split("\n")) {
    if (statement === "") {
      // Only the forms that can carry a module specifier: `import …` and the
      // re-export shapes `export * …` / `export { … } from`. `export interface`
      // and friends open a block whose body would otherwise be swallowed.
      if (!/^\s*(?:import\b|export\s+(?:type\s+)?[*{])/.test(line)) {
        for (const dynamic of line.matchAll(/\bimport\s*\(\s*["']([^"']+)["']/g)) {
          if (dynamic[1] !== undefined) found.push(dynamic[1]);
        }
        continue;
      }
      statement = line;
    } else {
      statement += ` ${line}`;
    }
    if (!/;\s*$/.test(line) && !/\bfrom\s*["'][^"']+["']\s*$/.test(line)) continue;
    const specifier =
      /\bfrom\s*["']([^"']+)["']/.exec(statement) ??
      /^\s*import\s*["']([^"']+)["']/.exec(statement);
    if (specifier?.[1] !== undefined) found.push(specifier[1]);
    statement = "";
  }
  return found;
}

async function isomorphicSources(entry: string): Promise<Map<string, string>> {
  const seen = new Map<string, string>();
  const visit = async (file: string): Promise<void> => {
    if (seen.has(file)) return;
    const source = await readFile(file, "utf8");
    seen.set(file, source);
    // Static and dynamic forms both pull a module into the `.` bundle, so the
    // walk must see `import("./x.js")` as well as `from "./x.js"`.
    for (const specifier of relativeSpecifiers(source)) {
      const child = resolve(dirname(file), specifier.replace(/\.js$/, ".ts"));
      await visit(child);
    }
  };
  await visit(entry);
  return seen;
}

describe("multimodal prompt helpers", () => {
  it("part sources are XOR-validated locally", async () => {
    let receivedPrompt: unknown;
    const base = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({
          sessionCapabilities: { audio: true, image: true },
          sessionId: "session-xor",
        }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          receivedPrompt = (await requests[Symbol.asyncIterator]().next()).value;
          yield terminal("run-xor");
        },
      });
    });
    const counted = countedTransport(base);
    const client = connect({ transport: counted.transport });
    const session = await client.sessions.create({});
    await Promise.resolve();
    counted.calls.stream = 0;
    counted.calls.unary = 0;

    expect(() =>
      imagePart({
        bytes: new Uint8Array([1]),
        mimeType: "image/png",
        url: "https://media.example/image.png",
      }),
    ).toThrowError(PromptValidationError);
    expect(() => audioPart({ mimeType: "audio/wav" })).toThrowError(PromptValidationError);
    expect(() =>
      imagePart({ mimeType: "image/png", url: "http://media.example/image.png" }),
    ).toThrowError(PromptValidationError);

    const forged = {
      bytes: new Uint8Array([1]),
      kind: "image",
      mimeType: "image/png",
      url: "https://media.example/image.png",
    } as PromptPart;
    await expect(session.run([forged])).rejects.toMatchObject({ reason: "source_xor" });
    expect(counted.calls).toEqual({ stream: 0, unary: 0 });

    expect(textPart("hello")).toEqual({ kind: "text", text: "hello" });
    expect(imagePart({ bytes: new Uint8Array([1]), mimeType: "image/png" })).toMatchObject({
      kind: "image",
    });
    expect(
      audioPart({ mimeType: "audio/wav", url: "https://media.example/audio.wav" }),
    ).toMatchObject({ kind: "audio" });
    const remoteImage = imagePart({
      mimeType: "image/png",
      url: "https://media.example/image.png",
    });
    await (await session.run([remoteImage])).result();
    expect(receivedPrompt).toMatchObject({
      kind: {
        value: {
          parts: [
            {
              data: new Uint8Array(),
              kind: 1,
              mimeType: "image/png",
              url: "https://media.example/image.png",
            },
          ],
        },
      },
    });
    await client.close();
  });

  it("runtime helpers stay in their subpath", async () => {
    const imageBlob = new Blob([new Uint8Array([1, 2])], { type: "image/png" });
    const audioFile = new File([new Uint8Array([3, 4])], "sample.wav", {
      type: "audio/wav",
    });
    await expect(imagePartFromBlob(imageBlob)).resolves.toMatchObject({ kind: "image" });
    await expect(audioPartFromBlob(audioFile)).resolves.toMatchObject({ kind: "audio" });

    const license = new URL("../LICENSE", import.meta.url);
    await expect(imagePartFromPath(license, "image/png")).resolves.toMatchObject({
      kind: "image",
    });
    await expect(audioPartFromPath(license, "audio/wav")).resolves.toMatchObject({
      kind: "audio",
    });

    const root = await import("../src/index.js");
    expect("imagePartFromPath" in root).toBe(false);
    expect("audioPartFromPath" in root).toBe(false);

    const sources = await isomorphicSources(
      fileURLToPath(new URL("../src/index.ts", import.meta.url)),
    );
    // An allowlist, not a denylist: a denylist silently admits every specifier
    // nobody thought to name (node:crypto and node:net are already used one
    // module away in the ./node graph).
    const allowed = new Set(["@bufbuild/protobuf", "@connectrpc/connect"]);
    for (const [file, source] of sources) {
      for (const specifier of bareSpecifiers(source)) {
        const root = specifier.startsWith("@")
          ? specifier.split("/").slice(0, 2).join("/")
          : (specifier.split("/")[0] ?? specifier);
        expect(allowed.has(root), `${file} imports ${specifier}, outside the . allowlist`).toBe(
          true,
        );
      }
    }
  });

  it("local validation is a courtesy, the server is authoritative", async () => {
    expect(() => imagePart({ bytes: new Uint8Array([1]), mimeType: "audio/wav" })).toThrowError(
      expect.objectContaining({ reason: "mime_type" }),
    );
    expect(() =>
      imagePart({ bytes: new Uint8Array(MAX_MEDIA_PART_BYTES + 1), mimeType: "image/png" }),
    ).toThrowError(expect.objectContaining({ reason: "size" }));

    let grpcPrompt: unknown;
    let grpcStreams = 0;
    const grpcTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({
          sessionCapabilities: { audio: false, image: true },
          sessionId: "session-grpc-media",
        }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          grpcStreams += 1;
          grpcPrompt = (await requests[Symbol.asyncIterator]().next()).value;
          yield terminal("run-grpc-media");
        },
      });
    });
    const grpcClient = connect({ transport: grpcTransport });
    const grpcSession = await grpcClient.sessions.create({});
    const audio = audioPart({ bytes: new Uint8Array([9]), mimeType: "audio/wav" });
    await expect(grpcSession.run([audio])).rejects.toMatchObject({ reason: "capability" });
    expect(grpcStreams).toBe(0);

    const image = imagePart({ bytes: new Uint8Array([1, 2, 3]), mimeType: "image/png" });
    await (await grpcSession.run([textPart("inspect"), image])).result();
    expect(grpcPrompt).toMatchObject({
      kind: {
        case: "prompt",
        value: {
          parts: [{ data: new Uint8Array([1, 2, 3]), kind: 1, mimeType: "image/png" }],
          text: "inspect",
        },
      },
    });
    await grpcClient.close();

    let httpPrompt: unknown;
    const httpFetch: typeof globalThis.fetch = async (input, init) => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/compatibility") return Response.json({ api_major: 1 });
      if (path === "/v1/sessions") {
        return Response.json({
          session_capabilities: { audio: false, image: true },
          session_id: "session-http-media",
        });
      }
      if (path === "/v1/sessions/session-http-media/prompt") {
        httpPrompt = JSON.parse(String(init?.body));
        return Response.json(
          {
            code: "invalid_argument",
            detail: "provider rejected a locally valid image",
            status: 400,
            title: "Invalid prompt",
          },
          { headers: { "content-type": "application/problem+json" }, status: 400 },
        );
      }
      throw new ConnectError("unexpected route", Code.NotFound);
    };
    const httpClient = connect({
      transport: createHttpTransport({ baseUrl: "https://mecatl.test", fetch: httpFetch }),
    });
    const httpSession = await httpClient.sessions.create({});
    const rejection = await httpSession
      .run([textPart("inspect"), image])
      .catch((error: unknown) => error);
    expect(rejection).toBeInstanceOf(ServerError);
    expect(rejection).not.toBeInstanceOf(PromptValidationError);
    expect(rejection).toMatchObject({ code: "invalid_argument", transport: "http" });
    expect(httpPrompt).toEqual({
      parts: [{ data: "AQID", kind: "image", mime_type: "image/png", url: "" }],
      text: "inspect",
    });
    await httpClient.close();
  });
});
