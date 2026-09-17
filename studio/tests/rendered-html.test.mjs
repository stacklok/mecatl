import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, readFileSync } from "node:fs";
import http from "node:http";
import https from "node:https";
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
// Remote-login parity instances (the `mecatui login` / `connect` knobs as
// env): one in RFC 9728 discovery mode against a fake issuer, one anonymous
// — over TLS with a private CA when openssl can mint a certificate here.
let discoveryStudio;
let discoveryBaseURL;
let anonymousStudio;
let anonymousBaseURL;
let issuer;
let issuerBaseURL;
let upstreamBaseURL;
let tlsUpstream;
let tlsAvailable = false;
const tlsUpstreamRequests = [];

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

function startStudio(port, baseURL, mecatlBaseURL, extraEnv = {}) {
  return spawn(nextBin, ["start", "-p", String(port)], {
    cwd: root,
    env: {
      ...process.env,
      MECATL_BASE_URL: mecatlBaseURL,
      MECATL_AUTH_TOKEN: "test-secret",
      MECATL_WORKSPACE: "/workspace/from-deployment",
      MECATL_STUDIO_PUBLIC_ORIGIN: baseURL,
      // Hermetic: the developer's shell must not leak an operator palette
      // directory into either instance; each test below sets its own.
      STUDIO_PALETTE_DIR: "",
      ...extraEnv,
    },
    stdio: ["ignore", "ignore", "inherit"],
  });
}

/** A throwaway self-signed localhost certificate for the TLS fake upstream
 * (openssl is present on the CI runners and developer machines; null when
 * it is not, and the TLS assertions skip). */
function makeSelfSignedCert() {
  const dir = resolve(root, ".scratch/hermetic-tls");
  const keyPath = resolve(dir, "key.pem");
  const certPath = resolve(dir, "cert.pem");
  try {
    mkdirSync(dir, { recursive: true });
    execFileSync(
      "openssl",
      [
        "req",
        "-x509",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-keyout",
        keyPath,
        "-out",
        certPath,
        "-days",
        "2",
        "-subj",
        "/CN=localhost",
        "-addext",
        "subjectAltName=DNS:localhost,IP:127.0.0.1",
      ],
      { stdio: "ignore" },
    );
    return {
      key: readFileSync(keyPath),
      cert: readFileSync(certPath),
      certPath,
    };
  } catch {
    return null;
  }
}

before(async () => {
  const upstreamHandler = (request, response) => {
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => {
      upstreamRequests.push({
        url: request.url,
        authorization: request.headers.authorization,
        body: Buffer.concat(chunks).toString() || null,
      });
      response.setHeader("Content-Type", "application/json");
      // RFC 9728 protected-resource metadata: the profile a discovery-mode
      // Studio reads instead of MECATL_OIDC_ISSUER / CLIENT_ID (the
      // deployment names its issuer, client, audience and scopes).
      if (request.url === "/.well-known/oauth-protected-resource") {
        response.end(
          JSON.stringify({
            resource: upstreamBaseURL,
            authorization_servers: [issuerBaseURL],
            "com.stacklok.mecatl.client_id": "studio-discovered-client",
            "com.stacklok.mecatl.audience": "mecatl-daemon",
            scopes_supported: ["openid", "profile", "offline_access"],
          }),
        );
        return;
      }
      // The proxy forwards Content-Disposition (the controller's daemon-log
      // download names its file with it); the fake sets one everywhere so
      // the external-mode test can pin that it survives the hop.
      response.setHeader(
        "Content-Disposition",
        'inline; filename="from-upstream.json"',
      );
      // A daemon that rejects Studio's bearer (a static-token or audience
      // mismatch): an RFC 9457 401 with the stable code.
      if (request.url === "/v1/sessions/rejected-credential") {
        response.statusCode = 401;
        response.setHeader("WWW-Authenticate", 'Bearer realm="mecatl"');
        response.end(
          JSON.stringify({
            type: "urn:mecatl:error:unauthenticated",
            code: "unauthenticated",
            error: "missing or invalid bearer token",
            status: 401,
          }),
        );
        return;
      }
      if (request.url === "/v1/sessions") {
        response.end(JSON.stringify({ session_id: "session-from-upstream" }));
        return;
      }
      response.end(
        JSON.stringify({ models: [{ id: "test-model", provider_id: "test" }] }),
      );
    });
  };
  upstream = http.createServer(upstreamHandler);
  const upstreamPort = await listen(upstream);
  upstreamBaseURL = `http://127.0.0.1:${upstreamPort}`;

  // The fake identity provider a discovered profile points at: its OIDC
  // metadata must echo the issuer the protected resource named, exactly.
  issuer = http.createServer((request, response) => {
    response.setHeader("Content-Type", "application/json");
    if (request.url === "/.well-known/openid-configuration") {
      response.end(
        JSON.stringify({
          issuer: issuerBaseURL,
          authorization_endpoint: `${issuerBaseURL}/authorize`,
          token_endpoint: `${issuerBaseURL}/token`,
          authorization_response_iss_parameter_supported: true,
        }),
      );
      return;
    }
    response.statusCode = 404;
    response.end("{}");
  });
  const issuerPort = await listen(issuer);
  issuerBaseURL = `http://127.0.0.1:${issuerPort}`;

  // The anonymous instance dials the upstream over TLS with a private CA
  // when a certificate can be minted (proving MECATL_TLS_CA end to end
  // through Next's fetch patch), else over plain HTTP.
  const tls = makeSelfSignedCert();
  tlsAvailable = tls !== null;
  let anonymousUpstreamURL = upstreamBaseURL;
  const anonymousEnv = { MECATL_AUTH_ANONYMOUS: "1" };
  if (tls) {
    tlsUpstream = https.createServer(
      { key: tls.key, cert: tls.cert },
      (request, response) => {
        tlsUpstreamRequests.push({
          url: request.url,
          authorization: request.headers.authorization,
          encrypted: Boolean(request.socket.encrypted),
        });
        upstreamHandler(request, response);
      },
    );
    const tlsPort = await listen(tlsUpstream);
    anonymousUpstreamURL = `https://127.0.0.1:${tlsPort}`;
    anonymousEnv.MECATL_TLS_CA = tls.certPath;
  }

  const discoveryPort = await freePort();
  discoveryBaseURL = `http://127.0.0.1:${discoveryPort}`;
  discoveryStudio = startStudio(
    discoveryPort,
    discoveryBaseURL,
    upstreamBaseURL,
    {
      MECATL_OIDC_DISCOVERY: "1",
      // Loopback plain HTTP for both the deployment and the fake issuer.
      MECATL_OIDC_PRIVATE_ISSUER: "1",
    },
  );

  const anonymousPort = await freePort();
  anonymousBaseURL = `http://127.0.0.1:${anonymousPort}`;
  anonymousStudio = startStudio(
    anonymousPort,
    anonymousBaseURL,
    anonymousUpstreamURL,
    anonymousEnv,
  );

  const studioPort = await freePort();
  studioBaseURL = `http://127.0.0.1:${studioPort}`;
  studio = startStudio(
    studioPort,
    studioBaseURL,
    `http://127.0.0.1:${upstreamPort}`,
    // The operator palette directory (src/app/api/palettes): one valid
    // document and one that must be skipped.
    { STUDIO_PALETTE_DIR: resolve(root, "tests/fixtures/palettes") },
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
    waitForServer(discoveryBaseURL, discoveryStudio),
    waitForServer(anonymousBaseURL, anonymousStudio),
  ]);
});

after(async () => {
  studio?.kill("SIGTERM");
  offlineStudio?.kill("SIGTERM");
  discoveryStudio?.kill("SIGTERM");
  anonymousStudio?.kill("SIGTERM");
  await new Promise((resolveClose) => upstream.close(resolveClose));
  await new Promise((resolveClose) => issuer.close(resolveClose));
  if (tlsUpstream)
    await new Promise((resolveClose) => tlsUpstream.close(resolveClose));
});

test("server-renders Mecatl Studio", async () => {
  const response = await fetch(`${studioBaseURL}/workspace/chat`);
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);
  const html = await response.text();
  assert.match(html, /Mecatl Studio/);
  // The palette boot script (src/components/palette-boot-script.tsx) ships
  // inline in <head>: the stored-or-default palette lands on <html> before
  // first paint, so the marker must be in the server HTML, not only in a
  // client bundle.
  assert.match(html, /data-palette/);
  assert.match(html, /mecatl-studio\.palette/);
});

test("the About card server-renders Studio's own version", async () => {
  const response = await fetch(`${studioBaseURL}/workspace/settings/help`);
  assert.equal(response.status, 200);
  const html = await response.text();
  // The version is inlined at `next build` (src/lib/studio-version.ts), so
  // the server-rendered card already names it — before any daemon call, and
  // whatever the daemon answers. The assertion is that a real, non-empty
  // semver is there, never a blank or "unknown".
  const match = /data-testid="about-studio-version"[^>]*>([^<]+)</.exec(html);
  assert.ok(match, "the About card's Studio version row is server-rendered");
  const version = match[1].trim();
  assert.match(version, /^\d+\.\d+\.\d+/);
});

test("the about API reports Studio's configuration surface by name only; the About page names no variable", async () => {
  // GET /api/studio/about (src/app/api/studio/about/route.ts) answers which
  // reference names THIS deployment sets, as booleans — the token and the
  // workspace are set here, so the assertion that their VALUES are absent
  // from the body is the one that matters.
  const response = await fetch(`${studioBaseURL}/api/studio/about`);
  assert.equal(response.status, 200);
  assert.match(response.headers.get("cache-control") ?? "", /no-store/);
  const text = await response.text();
  const about = JSON.parse(text);
  assert.equal(about.mode, "external");
  assert.equal(about.configured.MECATL_BASE_URL, true);
  assert.equal(about.configured.MECATL_AUTH_TOKEN, true);
  assert.equal(about.configured.MECATL_WORKSPACE, true);
  assert.equal(about.configured.MECATL_OIDC_ISSUER, false);
  assert.doesNotMatch(text, /test-secret/);
  assert.doesNotMatch(text, /from-deployment/);
  // The versions are inlined at `next build`: real, non-empty, never blank.
  assert.match(about.version, /^\d+\.\d+\.\d+/);
  assert.match(about.build, /^[A-Za-z0-9._/+-]+$/);
  // Same-origin only, like the proxies: a foreign browser Origin is refused.
  const refused = await fetch(`${studioBaseURL}/api/studio/about`, {
    headers: { Origin: "http://evil.example" },
  });
  assert.equal(refused.status, 403);
  // The page itself server-renders the About cards and the docs link before
  // any request runs — and, written for office users, names no environment
  // variable and no value.
  const page = await fetch(`${studioBaseURL}/workspace/settings/help`);
  assert.equal(page.status, 200);
  const html = await page.text();
  assert.match(html, /About Studio/);
  assert.match(html, /data-testid="about-studio-version"/);
  assert.match(html, /https:\/\/mecatl\.dev\/docs\//);
  assert.doesNotMatch(html, /MECATL_BASE_URL/);
  assert.doesNotMatch(html, /test-secret/);
});

test("external mode injects daemon auth server-side and disables local controls", async () => {
  const models = await fetch(`${studioBaseURL}/api/mecatl/v1/models`);
  assert.equal(models.status, 200);
  assert.equal(
    models.headers.get("content-disposition"),
    'inline; filename="from-upstream.json"',
  );
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
  // The deployment spawned its own mecated: Studio neither knows nor sets
  // its posture/trust/shell flags, so the saved-permissions mirror is null
  // (the EFFECTIVE posture still reads off the daemon's capabilities).
  assert.equal(status.permissions, null);
  // Nor its project-trust decision: there is no controller registry to
  // read in external mode, so the workspace trust banner never renders.
  assert.equal(status.trust, null);
  // Same for the session store: its location / in-memory mode is the
  // deployment's own spawn flag, so the mirror is null and the Storage card
  // renders the managed note instead of a form.
  assert.equal(status.storage, null);
  // And its retention flags: the EFFECTIVE policy still reads off the
  // daemon's own storage health, so the Retention card shows the table
  // and the managed note instead of a form.
  assert.equal(status.retention, null);
  // And for the daemon defaults (default/subagent model, effort, caching,
  // base URLs, ToolHive, aliases/slots, credentials path): spawn flags of
  // the MANAGED daemon only, so the mirror is null and the card renders the
  // managed note instead of a form.
  assert.equal(status.daemonDefaults, null);

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
    // The custom-definition write and the key-only (`providers logout`)
    // removal edit the MANAGED daemon's settings.yaml / auth.yaml.
    ["providers/custom", "POST"],
    ["providers/openrouter?scope=credential", "DELETE"],
    ["restart", "POST"],
    // Starting the ToolHive proxy spawns a process on the MANAGED
    // controller's machine; the external deployment owns its own gateway.
    ["toolhive/start", "POST"],
    // Posture / trust / shell-less mode are spawn flags of the MANAGED
    // daemon: the external deployment owns its own, read included.
    ["permissions", "GET"],
    ["permissions", "POST"],
    // The session-store location / in-memory switch is a spawn flag of the
    // MANAGED daemon too.
    ["storage", "POST"],
    // As is the workspace root the MANAGED daemon is spawned against
    // (--workspace): the external deployment chose its own, and Studio
    // only ever shows the MECATL_WORKSPACE label for it.
    ["workspace", "POST"],
    // As are its retention limits / sweep cadence / main-deletion
    // acknowledgement.
    ["retention", "POST"],
    // The daemon log is the MANAGED controller's file of its child's
    // stderr; an external deployment writes its diagnostics wherever it
    // configured them, and Studio has no access to a remote daemon's log.
    ["logs", "GET"],
    ["logs/download", "GET"],
    // The daemon defaults (--default-model, --subagent-model, effort,
    // caching, base URLs, ToolHive, aliases/slots, --api-key-file) are
    // spawn flags of the MANAGED daemon: external owns them, read included.
    ["daemon-defaults", "GET"],
    ["daemon-defaults", "PUT"],
    // The diagnostics options (--log-level, the admin/metrics listener,
    // --perf-mcp, --goroutine-warn-threshold, --product-metrics opt-out,
    // controller-side quiet) are spawn flags of the MANAGED daemon too:
    // external owns them, read included.
    ["diagnostics-options", "GET"],
    ["diagnostics-options", "POST"],
    // The runtime admin surface (the loopback --metrics-addr listener the
    // MANAGED controller chose, its /metrics and /debug/vars relays) is the
    // managed daemon's: an external deployment configures its own
    // --metrics-addr / --perf-mcp, and Studio relays nothing for it.
    ["perf", "GET"],
    ["perf/metrics", "GET"],
    ["perf/vars", "GET"],
    // The runtime settings (learning mode/sensitivity, the steer opt-out,
    // the soul flags) are spawn flags + a CLI-tier file of the MANAGED
    // daemon, and the soul baseline approval is one of its spawns: external
    // owns them all, read included.
    ["runtime-settings", "GET"],
    ["runtime-settings", "PUT"],
    ["soul/approve", "POST"],
    // The daemon options (the two memory stores, --skills-dir, slash
    // commands, MCP discovery) are spawn flags of the MANAGED daemon:
    // external owns them, read included.
    ["daemon-options", "GET"],
    ["daemon-options", "PUT"],
    // The two project-trust grants (the workspace banner's "Trust project"
    // / "Trust for this session") write the MANAGED controller's own trust
    // registry and restart its daemon: external owns its trust decision.
    ["permissions/trust", "POST"],
    ["permissions/trust-once", "POST"],
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

test("operator palettes from STUDIO_PALETTE_DIR are served validated, broken files skipped", async () => {
  // The route reads the directory per request (force-dynamic + no-store), so
  // a changed STUDIO_PALETTE_DIR is picked up without a rebuild. midnight.json
  // is well-formed; broken.json carries a url() value and an unknown token
  // and must never reach the browser — the server logs it, nothing more.
  const response = await fetch(`${studioBaseURL}/api/palettes`);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  const body = await response.json();
  assert.deepEqual(
    body.palettes.map((palette) => palette.name),
    ["midnight"],
  );
  assert.equal(body.palettes[0].label, "Midnight");
  assert.equal(body.palettes[0].palette.brand, "#4f7cff");
  assert.equal(body.palettes[0].dark.brand, "#7c9cff");
  assert.ok(!JSON.stringify(body).includes("url("));

  // Same-origin only, like the proxies: an off-origin browser request is
  // refused rather than enumerating the deployment's palettes.
  const csrf = await fetch(`${studioBaseURL}/api/palettes`, {
    headers: { origin: "https://evil.example" },
  });
  assert.equal(csrf.status, 403);
});

test("no STUDIO_PALETTE_DIR means an empty operator palette list", async () => {
  const response = await fetch(`${offlineBaseURL}/api/palettes`);
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { palettes: [] });
});

test("a daemon 401 is relayed verbatim — status, code, words and challenge — with the bearer still injected", async () => {
  const rejected = await fetch(
    `${studioBaseURL}/api/mecatl/v1/sessions/rejected-credential`,
  );
  // The proxy adds auth ONLY: the daemon's refusal crosses untouched, so the
  // client can classify it (Credential rejected, never "unreachable").
  assert.equal(rejected.status, 401);
  assert.equal(
    rejected.headers.get("www-authenticate"),
    'Bearer realm="mecatl"',
  );
  const body = await rejected.json();
  assert.equal(body.code, "unauthenticated");
  assert.match(body.error, /bearer token/);
  assert.deepEqual(upstreamRequests.at(-1), {
    url: "/v1/sessions/rejected-credential",
    authorization: "Bearer test-secret",
    body: null,
  });
});

test("MECATL_AUTH_ANONYMOUS=1 sends the daemon no credential, even with a static token set", async () => {
  const models = await fetch(`${anonymousBaseURL}/api/mecatl/v1/models`);
  assert.equal(models.status, 200);
  const recorded = tlsAvailable
    ? tlsUpstreamRequests.at(-1)
    : upstreamRequests.at(-1);
  assert.equal(recorded.url, "/v1/models");
  // The `--anonymous` analogue: MECATL_AUTH_TOKEN is set on this instance
  // too, and still no Authorization header reaches the daemon.
  assert.equal(recorded.authorization, undefined);

  const status = await fetch(`${anonymousBaseURL}/api/auth/oidc/status`).then(
    (response) => response.json(),
  );
  assert.equal(status.authMode, "anonymous");
  assert.equal(status.state, "not-configured");
});

test("MECATL_TLS_CA trusts a private CA for the daemon connection (through Next's fetch patch)", async (t) => {
  if (!tlsAvailable) {
    t.skip("openssl is not available to mint a test certificate");
    return;
  }
  const models = await fetch(`${anonymousBaseURL}/api/mecatl/v1/models`);
  assert.equal(models.status, 200);
  assert.deepEqual(await models.json(), {
    models: [{ id: "test-model", provider_id: "test" }],
  });
  const recorded = tlsUpstreamRequests.at(-1);
  assert.equal(recorded.url, "/v1/models");
  // The request really crossed TLS: Node's default trust store would have
  // refused the self-signed upstream (a 503 from the proxy), so the 200
  // above IS the `dispatcher` surviving Next's fetch patch.
  assert.equal(recorded.encrypted, true);
  const status = await fetch(`${anonymousBaseURL}/api/auth/oidc/status`).then(
    (response) => response.json(),
  );
  assert.deepEqual(status.transport, {
    tlsCa: true,
    insecure: false,
    privateIssuer: false,
  });
});

test("RFC 9728 discovery is reviewed and confirmed before any sign-in starts", async () => {
  const statusURL = `${discoveryBaseURL}/api/auth/oidc/status`;
  const status = await fetch(statusURL).then((response) => response.json());
  // The deployment's own metadata named the profile — and it is not yet a
  // sign-in configuration: default-deny until a human confirms it.
  assert.equal(status.state, "discovered");
  assert.equal(status.configured, false);
  assert.equal(status.source, "discovery");
  assert.equal(status.issuer, issuerBaseURL);
  assert.equal(status.clientId, "studio-discovered-client");
  assert.equal(status.audience, "mecatl-daemon");
  assert.deepEqual(status.scopes, ["openid", "profile", "offline_access"]);
  assert.match(status.profileHash, /^[0-9a-f]{64}$/);
  assert.equal(status.authMode, "oidc");
  assert.equal(status.transport.privateIssuer, true);
  // The browser never learns the daemon's address (rule 3): the status
  // carries the issuer, never MECATL_BASE_URL.
  assert.equal(JSON.stringify(status).includes(upstreamBaseURL), false);

  // Unconfirmed: the login route refuses (both the popup and link shapes).
  const refused = await fetch(`${discoveryBaseURL}/api/auth/oidc/start`, {
    redirect: "manual",
  });
  assert.equal(refused.status, 400);
  assert.match((await refused.json()).error, /Continue with browser login/);
  const refusedLink = await fetch(
    `${discoveryBaseURL}/api/auth/oidc/start?mode=link`,
  );
  assert.equal(refusedLink.status, 400);

  // The proxy already treats the deployment as OIDC-protected: signed out
  // means an actionable 401, and the static token is NOT a fallback.
  const proxied = await fetch(`${discoveryBaseURL}/api/mecatl/v1/models`);
  assert.equal(proxied.status, 401);
  assert.equal((await proxied.json()).code, "oidc_login_required");

  // A hash that is not the reviewed profile is refused.
  const wrong = await fetch(
    `${discoveryBaseURL}/api/auth/oidc/confirm-discovery`,
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ profileHash: "0".repeat(64) }),
    },
  );
  assert.equal(wrong.status, 409);
  assert.equal(
    (await fetch(statusURL).then((response) => response.json())).state,
    "discovered",
  );

  // The reviewed hash confirms; the profile is now a sign-in configuration.
  const confirmed = await fetch(
    `${discoveryBaseURL}/api/auth/oidc/confirm-discovery`,
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ profileHash: status.profileHash }),
    },
  );
  assert.equal(confirmed.status, 200);
  const after = await fetch(statusURL).then((response) => response.json());
  assert.equal(after.state, "signed-out");
  assert.equal(after.configured, true);
  assert.equal(after.clientId, "studio-discovered-client");

  // The no-browser flow: a JSON authorize URL built from the DISCOVERED
  // profile against the fake issuer's advertised endpoint.
  const link = await fetch(`${discoveryBaseURL}/api/auth/oidc/start?mode=link`);
  assert.equal(link.status, 200);
  const body = await link.json();
  const authorize = new URL(body.authorizationUrl);
  assert.equal(
    authorize.origin + authorize.pathname,
    `${issuerBaseURL}/authorize`,
  );
  assert.equal(
    authorize.searchParams.get("client_id"),
    "studio-discovered-client",
  );
  assert.equal(authorize.searchParams.get("audience"), "mecatl-daemon");
  assert.equal(
    authorize.searchParams.get("scope"),
    "openid profile offline_access",
  );
  assert.equal(authorize.searchParams.get("code_challenge_method"), "S256");
  assert.equal(
    authorize.searchParams.get("redirect_uri"),
    `${discoveryBaseURL}/api/auth/oidc/callback`,
  );
  assert.ok(new Date(body.expiresAt).getTime() > Date.now());
  // The popup shape still 302s to the same issuer.
  const redirect = await fetch(`${discoveryBaseURL}/api/auth/oidc/start`, {
    redirect: "manual",
  });
  assert.equal(redirect.status, 302);
  assert.ok(
    redirect.headers.get("location").startsWith(`${issuerBaseURL}/authorize?`),
  );
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
    // Writing a provider definition into settings.yaml or cutting a key
    // line from auth.yaml are file edits another loopback-origin page must
    // never be able to make.
    ["POST", "/providers/custom"],
    ["DELETE", "/providers/openrouter?scope=credential"],
    ["POST", "/restart"],
    // Starting the ToolHive proxy spawns a process; another loopback-origin
    // page must never be able to do that.
    ["POST", "/toolhive/start"],
    // The permissions document (posture / trust / shell-less) is likewise
    // header-gated on BOTH verbs: another loopback-origin page must not read
    // the daemon's trust flags, let alone raise its posture.
    ["GET", "/permissions"],
    ["POST", "/permissions"],
    // The session-store write relocates (or drops) the daemon's persistence;
    // another loopback-origin page must not be able to do that.
    ["POST", "/storage"],
    // The workspace-root write points mecated at ANY directory on the
    // operator's machine and restarts it there; another loopback-origin
    // page must never be able to do that.
    ["POST", "/workspace"],
    // The retention write can switch on automatic deletion of the user's
    // own chats; another loopback-origin page must never reach it.
    ["POST", "/retention"],
    // The runtime admin surface: /metrics can embed prompt text and file
    // paths, and /perf names the loopback listener's port — another
    // loopback-origin page must not read either.
    ["GET", "/perf"],
    ["GET", "/perf/metrics"],
    ["GET", "/perf/vars"],
    // The daemon log carries model-influenced text (prompt fragments,
    // provider error bodies): another loopback-origin page must not read
    // or download it.
    ["GET", "/logs"],
    ["GET", "/logs/download"],
    // The runtime settings name files on this machine (the soul path and
    // the picker's candidates) and the PUT/approve restart the daemon:
    // another loopback-origin page must not read or write them.
    ["GET", "/runtime-settings"],
    ["PUT", "/runtime-settings"],
    ["POST", "/soul/approve"],
    // The daemon options name the skills/memory/user-model directories on
    // this machine and the PUT restarts the daemon (and creates
    // directories): another loopback-origin page must not read or write
    // them.
    ["GET", "/daemon-options"],
    ["PUT", "/daemon-options"],
    // The project-trust grants raise what a checked-in allow rule may
    // auto-approve and restart the daemon: another loopback-origin page must
    // never be able to trust the workspace on the user's behalf.
    ["POST", "/permissions/trust"],
    ["POST", "/permissions/trust-once"],
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
