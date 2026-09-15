import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import { dirname, resolve } from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";

import {
  requestIsAllowed,
  validateGatewayURL,
} from "../src/lib/controller-security.mjs";

// Hermetic server-tier suite: a real `next start` of the production build in
// EXTERNAL mode, against a fake in-process daemon that records every request.
// This is the only layer that proves the proxy tier's behavior (bearer
// injection, CSRF 403, external-mode 409, verbatim body forwarding, offline
// 503) end to end. Wire decoding is the TypeScript SDK's (`@stacklok-oss/mecatl-sdk`)
// responsibility and is tested there. Requires `npm run build` first (npm test does that).

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const nextBin = resolve(root, "node_modules/.bin/next");
const upstreamRequests = [];
let upstream;
let studio;
let studioBaseURL;
let offlineStudio;
let offlineBaseURL;

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

async function waitForServer(url, child) {
  for (let attempt = 0; attempt < 150; attempt += 1) {
    if (child.exitCode !== null)
      throw new Error(`Next exited during test startup (${child.exitCode})`);
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {
      /* still starting */
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error("Next did not become ready for tests");
}

function startStudio(port, baseURL, mecatlBaseURL) {
  return spawn(nextBin, ["start", "-p", String(port)], {
    cwd: root,
    env: {
      ...process.env,
      MECATL_BASE_URL: mecatlBaseURL,
      MECATL_AUTH_TOKEN: "test-secret",
      MECATL_WORKSPACE: "/workspace/from-deployment",
      MECATL_STUDIO_PUBLIC_ORIGIN: baseURL,
    },
    stdio: ["ignore", "ignore", "inherit"],
  });
}

before(async () => {
  upstream = http.createServer((request, response) => {
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => {
      upstreamRequests.push({
        url: request.url,
        authorization: request.headers.authorization,
        body: Buffer.concat(chunks).toString() || null,
      });
      response.setHeader("Content-Type", "application/json");
      if (request.url === "/v1/sessions") {
        response.end(JSON.stringify({ session_id: "session-from-upstream" }));
        return;
      }
      response.end(
        JSON.stringify({ models: [{ id: "test-model", provider_id: "test" }] }),
      );
    });
  });
  const upstreamPort = await listen(upstream);

  const studioPort = await freePort();
  studioBaseURL = `http://127.0.0.1:${studioPort}`;
  studio = startStudio(
    studioPort,
    studioBaseURL,
    `http://127.0.0.1:${upstreamPort}`,
  );

  // A second instance whose daemon does not exist: the offline deployment.
  const offlinePort = await freePort();
  const deadPort = await freePort();
  offlineBaseURL = `http://127.0.0.1:${offlinePort}`;
  offlineStudio = startStudio(
    offlinePort,
    offlineBaseURL,
    `http://127.0.0.1:${deadPort}`,
  );

  await Promise.all([
    waitForServer(studioBaseURL, studio),
    waitForServer(offlineBaseURL, offlineStudio),
  ]);
});

after(async () => {
  studio?.kill("SIGTERM");
  offlineStudio?.kill("SIGTERM");
  await new Promise((resolveClose) => upstream.close(resolveClose));
});

test("server-renders Mecatl Studio", async () => {
  const response = await fetch(`${studioBaseURL}/workspace/chat`);
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);
  const html = await response.text();
  assert.match(html, /Mecatl Studio/);
});

test("external mode injects daemon auth server-side and disables local controls", async () => {
  const models = await fetch(`${studioBaseURL}/api/mecatl/v1/models`);
  assert.equal(models.status, 200);
  assert.deepEqual(await models.json(), {
    models: [{ id: "test-model", provider_id: "test" }],
  });
  assert.deepEqual(upstreamRequests.at(-1), {
    url: "/v1/models",
    authorization: "Bearer test-secret",
    body: null,
  });

  const status = await fetch(`${studioBaseURL}/api/mecatl-control/status`).then(
    (response) => response.json(),
  );
  assert.equal(status.mode, "external");
  assert.equal(status.workspace, "/workspace/from-deployment");

  const mutation = await fetch(
    `${studioBaseURL}/api/mecatl-control/model-router`,
    { method: "POST" },
  );
  assert.equal(mutation.status, 409);
  assert.match((await mutation.json()).error, /external mecated deployment/);

  // Skill AND provider management are controller-owned: external mode owns
  // nothing locally, so every write — and even the inventory reads — answers
  // 409. The provider rows pin that no external deployment's auth.yaml can
  // be probed, removed, or even enumerated through Studio.
  for (const [path, method] of [
    ["skills", "POST"],
    ["skills/pr-feedback/disable", "POST"],
    ["skills/pr-feedback/enable", "POST"],
    ["skills/pr-feedback/body", "PUT"],
    ["skills/pr-feedback", "DELETE"],
    ["skills/disabled", "GET"],
    ["providers", "GET"],
    ["providers/known", "GET"],
    ["providers/openrouter/test", "POST"],
    ["providers/openrouter", "DELETE"],
    ["restart", "POST"],
  ]) {
    const refused = await fetch(`${studioBaseURL}/api/mecatl-control/${path}`, {
      method,
    });
    assert.equal(refused.status, 409, `${method} ${path}`);
  }

  const csrf = await fetch(`${studioBaseURL}/api/mecatl/v1/sessions`, {
    method: "POST",
    headers: { origin: "https://evil.example", "content-type": "text/plain" },
    body: "{}",
  });
  assert.equal(csrf.status, 403);
});

test("session creation is forwarded verbatim — placement is server-owned", async () => {
  const created = await fetch(`${studioBaseURL}/api/mecatl/v1/sessions`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ mode: "default" }),
  });
  assert.equal(created.status, 200);
  const recorded = upstreamRequests.at(-1);
  assert.equal(recorded.url, "/v1/sessions");
  assert.equal(recorded.authorization, "Bearer test-secret");
  // The daemon decodes this body with unknown fields DISALLOWED and assigns
  // the workspace itself (ADR 0291): the proxy must add nothing — no
  // `workspace`, no rewrite — only the bearer credential.
  assert.deepEqual(JSON.parse(recorded.body), { mode: "default" });

  // Slash-command discovery is keyed by session, never by a workspace path:
  // the query string passes through untouched.
  const commands = await fetch(
    `${studioBaseURL}/api/mecatl/v1/commands?session_id=sess-1`,
  );
  assert.equal(commands.status, 200);
  assert.equal(upstreamRequests.at(-1).url, "/v1/commands?session_id=sess-1");
});

test("an unreachable daemon is a friendly 503, never demo content", async () => {
  const models = await fetch(`${offlineBaseURL}/api/mecatl/v1/models`);
  assert.equal(models.status, 503);
  assert.match((await models.json()).error, /unavailable/i);

  // The page itself still serves — the offline state is the UI's to render —
  // and carries no fabricated inventory.
  const page = await fetch(`${offlineBaseURL}/workspace/chat`);
  assert.equal(page.status, 200);
});

test("controller policy rejects CSRF and DNS-rebinding requests", () => {
  const policy = {
    allowedOrigins: new Set(["http://localhost:3000"]),
    mcpProxyPrefix: "/mcp-proxy/unguessable/",
  };
  const url = new URL("http://127.0.0.1:8788/mcp");
  assert.equal(
    requestIsAllowed(
      { method: "POST", headers: { host: "127.0.0.1:8788" } },
      url,
      policy,
    ),
    false,
  );
  assert.equal(
    requestIsAllowed(
      {
        method: "POST",
        headers: { host: "127.0.0.1:8788", origin: "https://evil.example" },
      },
      url,
      policy,
    ),
    false,
  );
  assert.equal(
    requestIsAllowed(
      {
        method: "POST",
        headers: { host: "attacker.example", "x-mecatl-studio-request": "1" },
      },
      url,
      policy,
    ),
    false,
  );
  assert.equal(
    requestIsAllowed(
      {
        method: "POST",
        headers: {
          host: "127.0.0.1:8788",
          origin: "http://localhost:3000",
          "x-mecatl-studio-request": "1",
        },
      },
      url,
      policy,
    ),
    true,
  );
  // Skill and provider routes are NOT in the header-free read-only
  // allowlist: even the inventory GETs need the server-set studio header,
  // and a mutation without it is refused like any other controller write.
  // For /providers that gate is part of rule 3's perimeter — a page in
  // another loopback-origin app must not be able to enumerate auth.yaml's
  // provider names, key-test a stored credential, or delete a block.
  for (const [method, pathname] of [
    ["GET", "/skills/disabled"],
    ["POST", "/skills"],
    ["POST", "/skills/pr-feedback/disable"],
    ["DELETE", "/skills/pr-feedback"],
    ["GET", "/providers"],
    ["GET", "/providers/known"],
    ["POST", "/providers/openrouter/test"],
    ["DELETE", "/providers/openrouter"],
    ["POST", "/restart"],
  ]) {
    assert.equal(
      requestIsAllowed(
        {
          method,
          headers: { host: "127.0.0.1:8788", origin: "http://localhost:3000" },
        },
        new URL(`http://127.0.0.1:8788${pathname}`),
        policy,
      ),
      false,
      `${method} ${pathname}`,
    );
  }
});

test("gateway egress requires HTTPS or an operator-enabled loopback exception", () => {
  assert.equal(
    validateGatewayURL("https://gateway.example/mcp").protocol,
    "https:",
  );
  assert.throws(
    () => validateGatewayURL("http://169.254.169.254/latest/meta-data"),
    /must use HTTPS/,
  );
  assert.throws(
    () => validateGatewayURL("http://127.0.0.1:9000/mcp"),
    /must use HTTPS/,
  );
  assert.equal(
    validateGatewayURL("http://127.0.0.1:9000/mcp", { allowLoopbackHTTP: true })
      .hostname,
    "127.0.0.1",
  );
  assert.throws(
    () => validateGatewayURL("https://user:secret@gateway.example/mcp"),
    /must not contain credentials/,
  );
});
