import { once } from "node:events";
import net from "node:net";

import { create, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it } from "vitest";

import {
  HarnessService,
  type RecheckMcpAuthorizationResponse,
  RecheckMcpAuthorizationResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { createNodeTransport } from "../src/node-transport.js";

// A raw HTTP/2 peer pins the exact frame order grpc-go uses when a handler
// returns while the client request side is still open: final DATA, trailers
// with END_STREAM, then RST_STREAM(NO_ERROR). A Node HTTP/2 server would
// reorder or coalesce its own cleanup. See nodejs/node#66693.
const PREFACE = Buffer.from("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n");
const DATA = 0;
const HEADERS = 1;
const RST_STREAM = 3;
const SETTINGS = 4;
const PING = 6;
const WINDOW_UPDATE = 8;
const SETTINGS_INITIAL_WINDOW_SIZE = 4;
const DEFAULT_WINDOW = 65_535;
const MAX_FRAME = 16_384;
const END_STREAM = 1;
const ACK = 1;
const END_HEADERS = 4;
const NO_ERROR = 0;

function frame(
  type: number,
  flags: number,
  streamId: number,
  payload: Buffer = Buffer.alloc(0),
): Buffer {
  const header = Buffer.alloc(9);
  header.writeUIntBE(payload.length, 0, 3);
  header[3] = type;
  header[4] = flags;
  header.writeUInt32BE(streamId, 5);
  return Buffer.concat([header, payload]);
}

// HPACK literal header field without indexing, new name; short ASCII only.
function literal(name: string, value: string): Buffer {
  return Buffer.concat([
    Buffer.from([0x00, name.length]),
    Buffer.from(name),
    Buffer.from([value.length]),
    Buffer.from(value),
  ]);
}

// Static-table :status 200 plus the gRPC content type.
const responseHeaders = Buffer.concat([
  Buffer.from([0x88]),
  literal("content-type", "application/grpc+proto"),
]);

function envelope(runId: string): Buffer {
  const body = toBinary(
    RecheckMcpAuthorizationResponseSchema,
    create(RecheckMcpAuthorizationResponseSchema, { event: { runId } }),
  );
  const prefix = Buffer.alloc(5);
  prefix.writeUInt32BE(body.length, 1);
  return Buffer.concat([prefix, body]);
}

class RawPeer {
  readonly #server = net.createServer();
  #socket: net.Socket | undefined;
  #nextPing = 0;
  readonly #pings = new Map<number, () => void>();
  readonly streamId: Promise<number>;
  #resolveStream: (id: number) => void = () => undefined;
  // Send-side flow control, so a large body moves only as the client reads.
  #connectionWindow = DEFAULT_WINDOW;
  #initialStreamWindow = DEFAULT_WINDOW;
  readonly #streamWindows = new Map<number, number>();
  #body: { id: number; bytes: Buffer; offset: number; tail: Buffer[] } | undefined;

  constructor() {
    this.streamId = new Promise((resolve) => {
      this.#resolveStream = resolve;
    });
    this.#server.on("connection", (socket) => this.#accept(socket));
  }

  async listen(): Promise<string> {
    this.#server.listen(0, "127.0.0.1");
    await once(this.#server, "listening");
    const address = this.#server.address() as net.AddressInfo;
    return `http://127.0.0.1:${address.port}`;
  }

  write(...frames: Buffer[]): void {
    for (const sent of frames) {
      if (sent[3] !== DATA) continue;
      const id = sent.readUInt32BE(5);
      const length = sent.readUIntBE(0, 3);
      this.#connectionWindow -= length;
      this.#streamWindows.set(id, this.#streamWindow(id) - length);
    }
    this.#socket?.write(Buffer.concat(frames));
  }

  // Sends body bytes as the flow-control windows allow, then the tail frames.
  sendBody(id: number, bytes: Buffer, tail: Buffer[]): void {
    this.#body = { bytes, id, offset: 0, tail };
    this.#pump();
  }

  get unsentBodyBytes(): number {
    return this.#body === undefined ? 0 : this.#body.bytes.length - this.#body.offset;
  }

  get bodySent(): boolean {
    return this.#body !== undefined && this.#body.tail.length === 0;
  }

  #streamWindow(id: number): number {
    return this.#streamWindows.get(id) ?? this.#initialStreamWindow;
  }

  #pump(): void {
    const body = this.#body;
    if (body === undefined) return;
    while (body.offset < body.bytes.length) {
      const size = Math.min(
        MAX_FRAME,
        body.bytes.length - body.offset,
        this.#connectionWindow,
        this.#streamWindow(body.id),
      );
      if (size <= 0) return;
      this.write(frame(DATA, 0, body.id, body.bytes.subarray(body.offset, body.offset + size)));
      body.offset += size;
    }
    if (body.tail.length > 0) this.write(...body.tail.splice(0));
  }

  // The client processes frames in order, so its PING ACK proves that every
  // earlier frame, including a reset, reached the client stream.
  async barrier(): Promise<void> {
    const id = ++this.#nextPing;
    const payload = Buffer.alloc(8);
    payload.writeUInt32BE(id, 4);
    const acked = new Promise<void>((resolve) => this.#pings.set(id, resolve));
    this.write(frame(PING, 0, 0, payload));
    await acked;
  }

  async close(): Promise<void> {
    this.#socket?.destroy();
    await new Promise((resolve) => this.#server.close(resolve));
  }

  #accept(socket: net.Socket): void {
    this.#socket = socket;
    socket.on("error", () => undefined);
    socket.write(frame(SETTINGS, 0, 0));
    let input = Buffer.alloc(0);
    let receivedPreface = false;
    let answered = false;
    socket.on("data", (chunk: Buffer) => {
      input = Buffer.concat([input, chunk]);
      if (!receivedPreface) {
        if (input.length < PREFACE.length) return;
        input = input.subarray(PREFACE.length);
        receivedPreface = true;
      }
      while (input.length >= 9) {
        const length = input.readUIntBE(0, 3);
        if (input.length < length + 9) return;
        const type = input[3];
        const flags = input[4] ?? 0;
        const id = input.readUInt32BE(5) & 0x7fffffff;
        const payload = input.subarray(9, length + 9);
        input = input.subarray(length + 9);
        if (type === SETTINGS && (flags & ACK) === 0) {
          for (let offset = 0; offset + 6 <= payload.length; offset += 6) {
            if (payload.readUInt16BE(offset) !== SETTINGS_INITIAL_WINDOW_SIZE) continue;
            const size = payload.readUInt32BE(offset + 2);
            for (const [streamId, window] of this.#streamWindows) {
              this.#streamWindows.set(streamId, window + size - this.#initialStreamWindow);
            }
            this.#initialStreamWindow = size;
          }
          this.write(frame(SETTINGS, ACK, 0));
        }
        if (type === WINDOW_UPDATE) {
          const increment = payload.readUInt32BE(0) & 0x7fffffff;
          if (id === 0) this.#connectionWindow += increment;
          else this.#streamWindows.set(id, this.#streamWindow(id) + increment);
          this.#pump();
        }
        if (type === PING && (flags & ACK) === 0) this.write(frame(PING, ACK, 0, payload));
        if (type === PING && (flags & ACK) !== 0) {
          const pingId = payload.readUInt32BE(4);
          this.#pings.get(pingId)?.();
          this.#pings.delete(pingId);
        }
        if (type === HEADERS && !answered) {
          answered = true;
          this.write(
            frame(HEADERS, END_HEADERS, id, responseHeaders),
            frame(DATA, 0, id, envelope("initial")),
          );
          this.#resolveStream(id);
        }
      }
    });
  }
}

interface OpenCall {
  readonly abort: AbortController;
  readonly peer: RawPeer;
  readonly streamId: number;
  readonly responses: AsyncIterator<RecheckMcpAuthorizationResponse>;
}

const cleanups: (() => PromiseLike<void>)[] = [];

afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});

// Opens a bidi call whose request side stays open, like the SDK's
// authorization and Converse inputs, and consumes the initial response.
async function openCall(): Promise<OpenCall> {
  const peer = new RawPeer();
  const baseUrl = await peer.listen();
  cleanups.push(() => peer.close());
  const transport = createNodeTransport({ baseUrl }) as ReturnType<typeof createNodeTransport> &
    AsyncDisposable;
  cleanups.push(() => transport[Symbol.asyncDispose]());
  let release: () => void = () => undefined;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  cleanups.push(async () => release());
  async function* input() {
    yield { authorizationId: "authorization", sessionId: "session" };
    await released;
  }
  const abort = new AbortController();
  const response = await transport.stream(
    HarnessService.method.recheckMcpAuthorization,
    abort.signal,
    undefined,
    undefined,
    input(),
    undefined,
  );
  const responses = response.message[Symbol.asyncIterator]();
  await expect(responses.next()).resolves.toMatchObject({
    done: false,
    value: { event: { runId: "initial" } },
  });
  return { abort, peer, responses, streamId: await peer.streamId };
}

async function drain(responses: OpenCall["responses"]): Promise<string[]> {
  const seen: string[] = [];
  for (;;) {
    const next = await responses.next();
    if (next.done) return seen;
    seen.push(next.value.event?.runId ?? "");
  }
}

const rst = (code: number) => {
  const payload = Buffer.alloc(4);
  payload.writeUInt32BE(code);
  return payload;
};

describe("Node transport response completion", () => {
  it("ends cleanly when a complete response is followed by a NO_ERROR reset", async () => {
    const { peer, responses, streamId } = await openCall();
    peer.write(
      frame(DATA, 0, streamId, envelope("terminal")),
      frame(HEADERS, END_HEADERS | END_STREAM, streamId, literal("grpc-status", "0")),
      frame(RST_STREAM, 0, streamId, rst(NO_ERROR)),
    );
    await peer.barrier();
    await expect(drain(responses)).resolves.toEqual(["terminal"]);
  });

  it("ends cleanly when the server completes without a reset", async () => {
    const { peer, responses, streamId } = await openCall();
    peer.write(
      frame(DATA, 0, streamId, envelope("terminal")),
      frame(HEADERS, END_HEADERS | END_STREAM, streamId, literal("grpc-status", "0")),
    );
    await peer.barrier();
    await expect(drain(responses)).resolves.toEqual(["terminal"]);
  });

  it("reports the server status when a failed response is followed by a NO_ERROR reset", async () => {
    const { peer, responses, streamId } = await openCall();
    peer.write(
      frame(DATA, 0, streamId, envelope("terminal")),
      frame(HEADERS, END_HEADERS | END_STREAM, streamId, literal("grpc-status", "7")),
      frame(RST_STREAM, 0, streamId, rst(NO_ERROR)),
    );
    await peer.barrier();
    await expect(responses.next()).resolves.toMatchObject({ done: false });
    const failure = await responses.next().catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(ConnectError);
    expect((failure as ConnectError).code).toBe(Code.PermissionDenied);
  });

  it("bounds buffering and delivers a large response before a NO_ERROR reset", async () => {
    const messages = Array.from({ length: 128 }, (_, index) => `m${index}:${"x".repeat(16_000)}`);
    const body = Buffer.concat(messages.map(envelope));
    const { peer, responses, streamId } = await openCall();
    peer.sendBody(streamId, body, [
      frame(HEADERS, END_HEADERS | END_STREAM, streamId, literal("grpc-status", "0")),
      frame(RST_STREAM, 0, streamId, rst(NO_ERROR)),
    ]);
    // Without reads, flow control stalls the peer long before the end.
    await peer.barrier();
    await peer.barrier();
    expect(peer.unsentBodyBytes).toBeGreaterThan(body.length / 2);
    // Read until the rest fits the window the client still grants, then stop:
    // the trailers and reset arrive while bytes are buffered below the reader.
    const seen: string[] = [];
    while (peer.unsentBodyBytes > 48_000) {
      const next = await responses.next();
      if (next.done) break;
      seen.push(next.value.event?.runId ?? "");
    }
    await peer.barrier();
    await peer.barrier();
    expect(peer.bodySent).toBe(true);
    seen.push(...(await drain(responses)));
    expect(seen.map((runId) => runId.slice(0, runId.indexOf(":")))).toEqual(
      messages.map((runId) => runId.slice(0, runId.indexOf(":"))),
    );
    expect(seen.every((runId, index) => runId === messages[index])).toBe(true);
  });

  it("settles a pending read when the caller aborts", async () => {
    const { abort, responses } = await openCall();
    const pending = responses.next();
    abort.abort();
    const failure = await pending.catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(ConnectError);
    expect((failure as ConnectError).code).toBe(Code.Canceled);
  });

  it("settles a read after the caller aborts a reset response with unread bytes", async () => {
    const { abort, peer, responses, streamId } = await openCall();
    peer.write(
      frame(DATA, 0, streamId, envelope("terminal")),
      frame(HEADERS, END_HEADERS | END_STREAM, streamId, literal("grpc-status", "0")),
      frame(RST_STREAM, 0, streamId, rst(NO_ERROR)),
    );
    await peer.barrier();
    abort.abort();
    const failure = await responses.next().catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(ConnectError);
    expect((failure as ConnectError).code).toBe(Code.Canceled);
  });

  it("still fails when the server resets before completing the response", async () => {
    const { peer, responses, streamId } = await openCall();
    peer.write(
      frame(DATA, 0, streamId, envelope("terminal")),
      frame(RST_STREAM, 0, streamId, rst(NO_ERROR)),
    );
    await peer.barrier();
    await expect(drain(responses)).rejects.toBeInstanceOf(ConnectError);
  });
});
