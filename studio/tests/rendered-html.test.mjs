import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { after, before, test } from "node:test";

import { requestIsAllowed, validateGatewayURL } from "../lib/controller-security.mjs";
import {
  decodeScheduleFire,
  decodeScheduleFires,
  decodeScheduleRows,
  encodeScheduleSpec,
  parseMecatlEvent,
  scheduleDraftFromRow,
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

// The wire schedule an edit has to survive: every spec field set, including the
// ones the panel's form has no control for.
const storedSchedule = () => ({
  spec: {
    name: "nightly",
    prompt: "Sweep the flaky tests",
    trigger: { cron: "0 2 * * *" },
    timezone: "Europe/London",
    workspace: "/repo",
    mode: 3,
    mutating: true,
    max_fires: 12,
    limits: { max_turns: 40, max_tool_calls: 200, max_consecutive_failures: 3 },
    singleton: true,
    misfire: 2,
    carry_context: true,
    fire_timeout: { seconds: 900 },
    selector: { provider_id: "openrouter", model_id: "anthropic/claude-sonnet-5" },
    parts: [{ kind: 1, mime_type: "image/png", data: "aGk=" }],
    created_at: { seconds: "1700000000" },
    owner: { subject: "user-7", name: "Ada", grant_type: "user" },
  },
  state: { enabled: true, fire_count: 12, next_fire_at: { seconds: "1800000000" } },
});

test("editing a schedule preserves the spec fields the form cannot show", () => {
  const [row] = decodeScheduleRows({ schedules: [storedSchedule()] });
  assert.equal(row.timezone, "Europe/London");
  assert.equal(row.maxFires, 12);
  assert.equal(row.limits.maxTurns, 40);
  assert.equal(row.owner, "Ada");

  // PUT replaces the whole spec, so a prompt-only edit that dropped these would
  // silently delete an operator's provider selector, deadline, and retry policy.
  const body = encodeScheduleSpec({ ...scheduleDraftFromRow(row), prompt: "Sweep and file issues" }, row.carried);
  assert.equal(body.prompt, "Sweep and file issues");
  assert.deepEqual(body.selector, { provider_id: "openrouter", model_id: "anthropic/claude-sonnet-5" });
  assert.equal(body.misfire, 2);
  assert.equal(body.carry_context, true);
  assert.equal(body.singleton, true);
  assert.deepEqual(body.parts, [{ kind: 1, mime_type: "image/png", data: "aGk=" }]);
  assert.deepEqual(body.limits, { max_turns: 40, max_tool_calls: 200, max_consecutive_failures: 3 });
  // Request bodies are decoded with protojson: a Duration is "900s", never the
  // {seconds} object the response carried.
  assert.equal(body.fire_timeout, "900s");
  // Server-owned fields are never echoed back.
  assert.equal(body.created_at, undefined);
  assert.equal(body.owner, undefined);

  // A create has nothing to preserve, so the carried half stays absent rather
  // than being sent as zero values.
  const created = encodeScheduleSpec(scheduleDraftFromRow(row));
  assert.equal(created.selector, undefined);
  assert.equal(created.fire_timeout, undefined);
  assert.equal(created.parts, undefined);
});

test("a schedule request body sends only the fields its trigger allows", () => {
  const cron = encodeScheduleSpec({
    name: "weekly", prompt: "Review", trigger: { kind: "cron", cron: "0 9 * * 1", timezone: "UTC" },
    profile: "", workspace: "/repo", mode: 2, mutating: false, maxFires: 5,
    limits: { maxTurns: 0, maxToolCalls: 0, maxConsecutiveFailures: 0 }, oneShotRetry: true, oneShotMaxRetries: 3,
  });
  assert.deepEqual(cron.trigger, { cron: "0 9 * * 1" });
  assert.equal(cron.max_fires, 5);
  // one_shot_retry is one-shot-only — the daemon rejects a cron carrying it, so
  // a form that has it toggled must not put it on the wire.
  assert.equal(cron.one_shot_retry, undefined);

  const at = Date.UTC(2027, 0, 2, 3, 4, 5);
  const oneShot = encodeScheduleSpec({
    name: "once", prompt: "Ship it", trigger: { kind: "one-shot", at },
    profile: "no-fs", workspace: "", mode: 2, mutating: false, maxFires: 5,
    limits: { maxTurns: 1, maxToolCalls: 2, maxConsecutiveFailures: 0 }, oneShotRetry: true, oneShotMaxRetries: 2,
  });
  // A Timestamp must be RFC 3339 on the way in, whatever shape it read back as.
  assert.deepEqual(oneShot.trigger, { one_shot: "2027-01-02T03:04:05.000Z" });
  assert.equal(oneShot.one_shot_retry, true);
  assert.equal(oneShot.one_shot_max_retries, 2);
  assert.equal(oneShot.max_fires, undefined);
  assert.equal(oneShot.timezone, undefined);
});

test("the fire log decodes outcomes, orders newest first, and marks in-flight fires", () => {
  const fires = decodeScheduleFires({ fires: [
    { id: "sched--nightly-1", schedule_name: "nightly", session_id: "sched--nightly-1", fired_at: { seconds: "1000" }, stop: "end_turn" },
    { id: "sched--nightly-3", schedule_name: "nightly", session_id: "sched--nightly-3", fired_at: { seconds: "3000" }, started_at: { seconds: "3001" } },
    { id: "sched--nightly-2", schedule_name: "nightly", session_id: "sched--nightly-2", fired_at: { seconds: "2000" }, stop: "error", err: "provider unavailable" },
    { schedule_name: "nightly", stop: "end_turn" },
  ] });
  assert.deepEqual(fires.map((fire) => fire.id), ["sched--nightly-3", "sched--nightly-2", "sched--nightly-1"]);
  // No stop yet is progress, not success: an unfinished fire must never read as ok.
  assert.equal(fires[0].inFlight, true);
  assert.equal(fires[1].inFlight, false);
  assert.equal(fires[1].err, "provider unavailable");
  assert.equal(fires[2].stop, "end_turn");

  // protojson timestamps reach the same decoder as stdlib ones.
  const single = decodeScheduleFire({ fire: { id: "sched--nightly-4", fired_at: "2026-08-18T07:30:00Z", stop: "budget" } });
  assert.equal(single.firedAt, Date.parse("2026-08-18T07:30:00Z"));
  assert.equal(single.inFlight, false);
  assert.equal(decodeScheduleFire({}), null);
});
