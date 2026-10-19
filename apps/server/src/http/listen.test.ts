// SPDX-License-Identifier: Apache-2.0

import type { AddressInfo } from "node:net";
import { connect } from "node:net";
import type { ServerType } from "@hono/node-server";
import { afterEach, describe, expect, it } from "vitest";
import { startServer } from "./listen.js";

let server: ServerType | undefined;

afterEach(async () => {
  await new Promise<void>((resolve) => {
    if (!server) return resolve();
    server.close(() => resolve());
  });
  server = undefined;
});

async function listen(): Promise<number> {
  return new Promise((resolve) => {
    server = startServer(
      { fetch: () => new Response("app", { status: 200 }), hostname: "127.0.0.1", port: 0 },
      (info: AddressInfo) => resolve(info.port),
    );
  });
}

/** Send raw bytes and collect everything the server writes until it closes. */
async function exchange(port: number, request: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const socket = connect(port, "127.0.0.1");
    const chunks: Buffer[] = [];
    socket.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
    socket.on("error", reject);
    socket.on("close", () => resolve(Buffer.concat(chunks).toString("utf8")));
    socket.end(request);
  });
}

function split(response: string) {
  const end = response.indexOf("\r\n\r\n");
  const head = response.slice(0, end).split("\r\n");
  return { body: response.slice(end + 4), head, status: head[0] };
}

describe("startServer", () => {
  it("serves ordinary requests through the app", async () => {
    const port = await listen();
    const { body, status } = split(
      await exchange(
        port,
        "GET /workspace/chat HTTP/1.1\r\nHost: studio\r\nConnection: close\r\n\r\n",
      ),
    );
    expect(status).toBe("HTTP/1.1 200 OK");
    expect(body).toContain("app");
  });

  it("explains an oversized share link instead of a blank 431", async () => {
    const port = await listen();
    // A share target GET carrying about 24 KB of encoded text, past Node's 16 KiB header limit.
    const prompt = encodeURIComponent("<script>alert(1)</script> ".repeat(600));
    const response = await exchange(
      port,
      `GET /workspace/chat?prompt=${prompt} HTTP/1.1\r\nHost: studio\r\n\r\n`,
    );
    const { body, head, status } = split(response);
    expect(status).toBe("HTTP/1.1 431 Request Header Fields Too Large");
    expect(head).toContain("Content-Type: text/html; charset=utf-8");
    expect(head).toContain("Connection: close");
    expect(head).toContain("Cache-Control: no-store");
    expect(head).toContain("X-Content-Type-Options: nosniff");
    expect(head).toContain("Content-Security-Policy: default-src 'none'; frame-ancestors 'none'");
    expect(head).toContain(`Content-Length: ${Buffer.byteLength(body)}`);
    expect(body).toContain("too long");
    expect(body).toContain('href="/workspace/chat"');
    // Nothing from the request is echoed back.
    expect(body).not.toContain("script");
    expect(body).not.toContain("alert");
  });

  it("keeps Node's plain 400 for other malformed requests", async () => {
    const port = await listen();
    const response = await exchange(port, "NOT A REQUEST\r\n\r\n");
    expect(response).toBe("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n");
  });
});
