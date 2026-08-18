import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { after, before, test } from "node:test";

import { requestIsAllowed, validateGatewayURL } from "../lib/controller-security.mjs";
import {
  decodeScheduleRows,
  decodeSessionInventory,
  decodeSessionTranscript,
  parseMecatlEvent,
} from "../lib/protocol.ts";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const nextBin = resolve(root, "node_modules/.bin/next");
const upstreamRequests = [];
let upstream;
let studio;
let studioBaseURL;

async function listen(server) {
  await new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  return server.address().port;
}

async function freePort() {
  const probe = http.createServer();
  const port = await listen(probe);
  await new Promise((resolveClose) => probe.close(resolveClose));
  return port;
}

async function waitForServer(url, process) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (process.exitCode !== null) throw new Error(`Next exited during test startup (${process.exitCode})`);
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch { /* still starting */ }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error("Next did not become ready for tests");
}

before(async () => {
  upstream = http.createServer((request, response) => {
    upstreamRequests.push({ url: request.url, authorization: request.headers.authorization });
    response.setHeader("Content-Type", "application/json");
    response.end(JSON.stringify({ models: [{ id: "test-model", provider_id: "test" }] }));
  });
  const upstreamPort = await listen(upstream);
  const studioPort = await freePort();
  studioBaseURL = `http://127.0.0.1:${studioPort}`;
  studio = spawn(nextBin, ["start", "-p", String(studioPort)], {
    cwd: root,
    env: {
      ...process.env,
      MECATL_BASE_URL: `http://127.0.0.1:${upstreamPort}`,
      MECATL_AUTH_TOKEN: "test-secret",
      MECATL_WORKSPACE: "/workspace/from-deployment",
      MECATL_STUDIO_PUBLIC_ORIGIN: studioBaseURL,
    },
    stdio: ["ignore", "ignore", "inherit"],
  });
  await waitForServer(studioBaseURL, studio);
});

after(async () => {
  studio?.kill("SIGTERM");
  await new Promise((resolveClose) => upstream.close(resolveClose));
});

test("server-renders Mecatl Studio", async () => {
  const response = await fetch(studioBaseURL);
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);
  const html = await response.text();
  assert.match(html, /<title>Mecatl Studio<\/title>/i);
  assert.match(html, /A focused local workspace for building with the mecatl agent harness/);
});

test("external mode injects daemon auth server-side and disables local controls", async () => {
  const models = await fetch(`${studioBaseURL}/api/mecatl/v1/models`);
  assert.equal(models.status, 200);
  assert.deepEqual(await models.json(), { models: [{ id: "test-model", provider_id: "test" }] });
  assert.deepEqual(upstreamRequests.at(-1), {
    url: "/v1/models",
    authorization: "Bearer test-secret",
  });

  const status = await fetch(`${studioBaseURL}/api/mecatl-control/status`).then((response) => response.json());
  assert.equal(status.mode, "external");
  assert.equal(status.workspace, "/workspace/from-deployment");

  const mutation = await fetch(`${studioBaseURL}/api/mecatl-control/model-router`, { method: "POST" });
  assert.equal(mutation.status, 409);
  assert.match((await mutation.json()).error, /external mecated deployment/);

  const csrf = await fetch(`${studioBaseURL}/api/mecatl/v1/sessions`, {
    method: "POST",
    headers: { origin: "https://evil.example", "content-type": "text/plain" },
    body: "{}",
  });
  assert.equal(csrf.status, 403);
});

test("controller policy rejects CSRF and DNS-rebinding requests", () => {
  const policy = {
    allowedOrigins: new Set(["http://localhost:3000"]),
    mcpProxyPrefix: "/mcp-proxy/unguessable/",
  };
  const url = new URL("http://127.0.0.1:8788/mcp");
  assert.equal(requestIsAllowed({ method: "POST", headers: { host: "127.0.0.1:8788" } }, url, policy), false);
  assert.equal(requestIsAllowed({ method: "POST", headers: { host: "127.0.0.1:8788", origin: "https://evil.example" } }, url, policy), false);
  assert.equal(requestIsAllowed({ method: "POST", headers: { host: "attacker.example", "x-mecatl-studio-request": "1" } }, url, policy), false);
  assert.equal(requestIsAllowed({ method: "POST", headers: { host: "127.0.0.1:8788", origin: "http://localhost:3000", "x-mecatl-studio-request": "1" } }, url, policy), true);
});

test("gateway egress requires HTTPS or an operator-enabled loopback exception", () => {
  assert.equal(validateGatewayURL("https://gateway.example/mcp").protocol, "https:");
  assert.throws(() => validateGatewayURL("http://169.254.169.254/latest/meta-data"), /must use HTTPS/);
  assert.throws(() => validateGatewayURL("http://127.0.0.1:9000/mcp"), /must use HTTPS/);
  assert.equal(validateGatewayURL("http://127.0.0.1:9000/mcp", { allowLoopbackHTTP: true }).hostname, "127.0.0.1");
  assert.throws(() => validateGatewayURL("https://user:secret@gateway.example/mcp"), /must not contain credentials/);
});

test("wire decoders preserve terminal failures, schedules, and unknown event kinds", () => {
  const failure = parseMecatlEvent(JSON.stringify({ type: "result", result: { stop: "error", error: "provider unavailable" } }));
  assert.equal(failure.result?.stop, "error");
  assert.equal(failure.result?.error, "provider unavailable");

  const route = parseMecatlEvent(JSON.stringify({ type: "provider.route", text: "openrouter → anthropic" }));
  assert.equal(route.type, "provider.route");
  assert.equal(route.text, "openrouter → anthropic");
  assert.throws(() => parseMecatlEvent(JSON.stringify({ result: {} })), /event\.type is required/);

  const rows = decodeScheduleRows({ schedules: [{
    spec: { name: "weekly", prompt: "Review", trigger: { cron: "0 9 * * 1" }, mode: 2, mutating: false },
    state: { enabled: true, fire_count: 3, next_fire_at: { seconds: "60", nanos: 500_000_000 }, last_fire_session_id: "pending" },
  }] });
  assert.equal(rows[0].nextFireAt, 60_500);
  assert.equal(rows[0].fireStage, "claimed");
  assert.equal(rows[0].mode, 2);
});

// --- the stored-chat inventory ---------------------------------------------
//
// These envelopes are the daemon's real wire shape: encoding/json over the
// generated protobuf structs, so field names are snake_case and every zero
// value is OMITTED. A decoder that reads `capabilities.rename` as "missing means
// unknown" rather than "missing means false" would offer the operator actions
// the daemon is going to refuse.

test("session inventory decodes a stored chat row", () => {
  const [row] = decodeSessionInventory({
    sessions: [{
      session_id: "sess-1",
      title: "Explain the agent loop",
      state: "completed",
      workspace: "/repo",
      model_id: "claude-opus-5",
      turns: 4,
      modified_at_unix: 1_760_000_060,
      created_at_unix: 1_760_000_000,
      capabilities: {
        rename: true,
        delete: true,
        view_transcript: true,
        reasons: {},
      },
    }],
  }).sessions;
  assert.equal(row.sessionId, "sess-1");
  assert.equal(row.title, "Explain the agent loop");
  assert.equal(row.state, "completed");
  assert.equal(row.modelId, "claude-opus-5");
  assert.equal(row.turns, 4);
  // Unix SECONDS on the wire, millis in the client.
  assert.equal(row.modifiedAt, 1_760_000_060_000);
  assert.equal(row.createdAt, 1_760_000_000_000);
  assert.equal(row.isChat, true);
  assert.equal(row.canRename, true);
  assert.equal(row.canDelete, true);
  assert.equal(row.canViewTranscript, true);
});

test("an omitted capability is a denial, not an unknown", () => {
  // A chat the daemon will not delete because its store cannot prune: `delete`
  // and `rename` are absent from the JSON entirely because they are false.
  const [row] = decodeSessionInventory({
    sessions: [{
      session_id: "sess-2",
      capabilities: { reasons: { delete: "storage_unsupported" } },
    }],
  }).sessions;
  assert.equal(row.canRename, false);
  assert.equal(row.canDelete, false);
  assert.equal(row.deleteReason, "storage_unsupported");
  // Absent title/state/model must not become the string "undefined".
  assert.equal(row.title, "");
  assert.equal(row.state, "");
  assert.equal(row.turns, 0);
  assert.equal(row.modifiedAt, 0);
});

test("only inspect_only_kind means a row is not a chat", () => {
  const { sessions } = decodeSessionInventory({
    sessions: [
      // A subagent: never an operator-facing chat.
      { session_id: "subagent-9", capabilities: { reasons: { public_chat: "inspect_only_kind" } } },
      // A chat that happens to be busy: still a chat, still listed.
      { session_id: "sess-3", state: "running", capabilities: { reasons: { public_chat: "active_elsewhere", rename: "active_elsewhere" } } },
      // A chat paused on an approval raised in another client.
      { session_id: "sess-4", state: "awaiting", capabilities: { reasons: { public_chat: "awaiting_approval" } } },
    ],
  });
  assert.deepEqual(sessions.map((row) => [row.sessionId, row.isChat]), [
    ["subagent-9", false],
    ["sess-3", true],
    ["sess-4", true],
  ]);
  assert.equal(sessions[1].renameReason, "active_elsewhere");
});

test("session inventory drops a row with no id and reports the cursor", () => {
  const page = decodeSessionInventory({
    sessions: [{ title: "no id" }, { session_id: "sess-5" }],
    next_cursor: "opaque-cursor",
  });
  // A row with no id can never be opened, renamed, or deleted; rendering it
  // would put a permanently inert chat in the list.
  assert.deepEqual(page.sessions.map((row) => row.sessionId), ["sess-5"]);
  assert.equal(page.nextCursor, "opaque-cursor");
});

test("a malformed inventory envelope decodes to an empty page", () => {
  assert.deepEqual(decodeSessionInventory(null), { sessions: [], nextCursor: "" });
  assert.deepEqual(decodeSessionInventory({ sessions: "nope" }), { sessions: [], nextCursor: "" });
});

// --- a stored chat's transcript ---------------------------------------------

test("session transcript decodes messages, tool calls and their results", () => {
  const transcript = decodeSessionTranscript({
    session_id: "sess-1",
    complete: true,
    messages: [
      { role: "user", text: "read the readme" },
      { role: "assistant", text: "Reading it.", tool_calls: [{ id: "call-1", name: "Read", args: '{"path":"README.md"}' }] },
      { role: "tool", tool_result: { call_id: "call-1", content: "# mecatl" } },
      { role: "assistant", text: "It is the harness readme." },
    ],
  });
  assert.equal(transcript.sessionId, "sess-1");
  assert.equal(transcript.complete, true);
  assert.deepEqual(transcript.messages.map((message) => message.role), ["user", "assistant", "tool", "assistant"]);
  assert.deepEqual(transcript.messages[1].toolCalls, [
    { id: "call-1", name: "Read", args: '{"path":"README.md"}' },
  ]);
  // is_error is omitted when false, and must decode as false rather than
  // undefined — a successful tool call would otherwise render as a failure.
  assert.deepEqual(transcript.messages[2].toolResult, {
    callId: "call-1",
    content: "# mecatl",
    isError: false,
  });
});

test("a failed tool result keeps its error flag", () => {
  const transcript = decodeSessionTranscript({
    messages: [{ role: "tool", tool_result: { call_id: "call-2", content: "no such file", is_error: true } }],
  });
  assert.equal(transcript.messages[0].toolResult.isError, true);
});

test("an incomplete transcript is reported as incomplete", () => {
  // `complete` is the daemon's attestation that the whole conversation loaded.
  // It is omitted when false, so its absence must not read as "all of it".
  assert.equal(decodeSessionTranscript({ session_id: "sess-6", messages: [] }).complete, false);
});

test("a malformed transcript envelope decodes to no messages", () => {
  assert.deepEqual(decodeSessionTranscript(null).messages, []);
  assert.deepEqual(decodeSessionTranscript({ messages: [null, 7] }).messages, []);
});
