// SPDX-License-Identifier: Apache-2.0

import type { Duplex } from "node:stream";
import { type ServerType, serve } from "@hono/node-server";

type ServeOptions = Parameters<typeof serve>[0];
type ListeningListener = Parameters<typeof serve>[1];

/**
 * A page for requests whose address and headers exceed Node's 16 KiB limit.
 * The usual cause is the share target: a browser puts shared text in the
 * `/workspace/chat?prompt=` address, so long text never reaches the app and
 * Node would answer with an empty 431. The page stays 431 (a redirect would
 * loop when oversized cookies are the cause), echoes nothing from the
 * request, and loads nothing.
 */
const tooLargePage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Too large for Studio</title>
</head>
<body>
<main>
<h1>This request is too large for Studio</h1>
<p>If you shared text to Studio, it was too long to arrive in a link. Copy the text, open a new chat, and paste it into the message box instead.</p>
<p><a href="/workspace/chat">Open a new chat</a></p>
</main>
</body>
</html>
`;

const tooLargeResponse = [
  "HTTP/1.1 431 Request Header Fields Too Large",
  "Content-Type: text/html; charset=utf-8",
  `Content-Length: ${Buffer.byteLength(tooLargePage)}`,
  "Cache-Control: no-store",
  "Content-Security-Policy: default-src 'none'; frame-ancestors 'none'",
  "X-Content-Type-Options: nosniff",
  "Referrer-Policy: no-referrer",
  "Connection: close",
  "",
  tooLargePage,
].join("\r\n");

/** Node's own responses for the other parser errors, which a listener replaces. */
const defaultResponses: Readonly<Record<string, string>> = {
  ERR_HTTP_REQUEST_TIMEOUT: "HTTP/1.1 408 Request Timeout\r\nConnection: close\r\n\r\n",
  HPE_CHUNK_EXTENSIONS_OVERFLOW: "HTTP/1.1 413 Payload Too Large\r\nConnection: close\r\n\r\n",
};
const badRequestResponse = "HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n";

export function handleClientError(error: NodeJS.ErrnoException, socket: Duplex): void {
  if (socket.writable && (socket as Duplex & { bytesWritten?: number }).bytesWritten === 0) {
    if (error.code === "HPE_HEADER_OVERFLOW") {
      // Close once the page is flushed, even if the client never hangs up.
      socket.end(tooLargeResponse, () => socket.destroy());
      return;
    }
    socket.write(defaultResponses[error.code ?? ""] ?? badRequestResponse);
  }
  socket.destroy(error);
}

/** Start Studio's HTTP server, with a readable page for oversized share links. */
export function startServer(options: ServeOptions, onListening?: ListeningListener): ServerType {
  const server = serve(options, onListening);
  server.on("clientError", handleClientError);
  return server;
}
