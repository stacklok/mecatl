import { type ChildProcess, spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { dirname, extname, join, relative, resolve, sep } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

import { test as base, type CDPSession, expect } from "@playwright/test";

const browserDirectory = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(browserDirectory, "../../../..");
const sdkRoot = join(repositoryRoot, "sdk", "typescript");
const maxStderrBytes = 64 << 10;

interface ReadyDocument {
  api_major: number;
  grpc_address: string;
  http_address?: string;
  schema: string;
  transport: "tcp" | "unix";
}

export interface BrowserHarness {
  readonly baseUrl: string;
  readonly origin: string;
  readonly siblingOrigin: string;
  readonly workspace: string;
}

interface RunningHarness extends BrowserHarness {
  close(): Promise<void>;
}

interface OriginServer {
  close(): Promise<void>;
  readonly origin: string;
}

interface TestFixtures {
  loopbackOnly: undefined;
}

interface FetchRequestPausedEvent {
  request: { url: string };
  requestId: string;
}

interface WorkerFixtures {
  browserHarness: BrowserHarness;
}

export const test = base.extend<TestFixtures, WorkerFixtures>({
  browserHarness: [
    async ({ playwright: _playwright }, use, workerInfo) => {
      const harness = await startHarness(workerInfo.workerIndex, workerInfo.project.name);
      try {
        await use(harness);
      } finally {
        await harness.close();
      }
    },
    { scope: "worker", timeout: 60_000 },
  ],
  loopbackOnly: [
    async ({ browserHarness, browserName, context, page }, use) => {
      const allowedOrigins = new Set([
        browserHarness.baseUrl,
        browserHarness.origin,
        browserHarness.siblingOrigin,
      ]);
      const blocked: string[] = [];
      if (browserName !== "chromium") {
        await context.route("**/*", async (route) => {
          const url = new URL(route.request().url());
          if (allowedOrigins.has(url.origin)) {
            await route.continue();
            return;
          }
          blocked.push(url.href);
          await route.abort("blockedbyclient");
        });
        await use(undefined);
        await context.unrouteAll({ behavior: "wait" });
        expect(blocked, "browser tests attempted non-fixture network access").toEqual([]);
        return;
      }

      const pending = new Set<Promise<unknown>>();
      const session: CDPSession = await context.newCDPSession(page);
      // Do not use context.route() for this guard: Playwright fulfills browser
      // OPTIONS preflights itself whenever routing is enabled, which would bypass
      // the real daemon and invalidate the exact-origin proof.
      session.on("Fetch.requestPaused", (event: FetchRequestPausedEvent) => {
        const url = new URL(event.request.url);
        const allowed = allowedOrigins.has(url.origin);
        const command = allowed
          ? session.send("Fetch.continueRequest", { requestId: event.requestId })
          : session.send("Fetch.failRequest", {
              errorReason: "BlockedByClient",
              requestId: event.requestId,
            });
        if (!allowed) blocked.push(url.href);
        pending.add(command);
        void command.then(
          () => pending.delete(command),
          () => pending.delete(command),
        );
      });
      await session.send("Fetch.enable", {
        patterns: [{ requestStage: "Request", urlPattern: "*" }],
      });
      await use(undefined);
      await Promise.allSettled(pending);
      expect(blocked, "browser tests attempted non-fixture network access").toEqual([]);
      await session.send("Fetch.disable");
      await session.detach();
    },
    { auto: true },
  ],
});

export { expect };

async function startHarness(workerIndex: number, projectName: string): Promise<RunningHarness> {
  const scratchRoot = join(repositoryRoot, ".scratch");
  await mkdir(scratchRoot, { recursive: true });
  const runtimeDirectory = await mkdtemp(join(scratchRoot, `sdk-browser-${workerIndex}-`));
  const extractedRoot = join(runtimeDirectory, "packed");
  const storeDirectory = join(runtimeDirectory, "store");
  const workspace = join(runtimeDirectory, "workspace");
  await Promise.all([mkdir(extractedRoot), mkdir(storeDirectory), mkdir(workspace)]);

  let allowedServer: OriginServer | undefined;
  let siblingServer: OriginServer | undefined;
  let daemon: ChildProcess | undefined;
  try {
    await extractPackedSurface(extractedRoot);
    const packageRoot = join(extractedRoot, "package");
    await stat(join(packageRoot, "dist", "index.js"));

    allowedServer = await startOriginServer(packageRoot);
    siblingServer = await startOriginServer(packageRoot);

    const readyFile = join(runtimeDirectory, "ready.json");
    const running = await startDaemon({
      allowedOrigin: allowedServer.origin,
      projectName,
      readyFile,
      storeDirectory,
      workspace,
    });
    daemon = running.child;
    if (running.ready.http_address === undefined) {
      throw new Error("same-checkout mecated omitted its HTTP readiness address");
    }
    const baseUrl = `http://${running.ready.http_address}`;
    assertLoopback(baseUrl);

    return {
      baseUrl,
      close: async () => {
        await stopProcess(running.child);
        await Promise.all([allowedServer?.close(), siblingServer?.close()]);
        await rm(runtimeDirectory, { force: true, recursive: true });
      },
      origin: allowedServer.origin,
      siblingOrigin: siblingServer.origin,
      workspace,
    };
  } catch (error) {
    if (daemon !== undefined) await stopProcess(daemon);
    await Promise.all([allowedServer?.close(), siblingServer?.close()]);
    await rm(runtimeDirectory, { force: true, recursive: true });
    throw error;
  }
}

async function extractPackedSurface(destination: string): Promise<void> {
  const packageDocument = JSON.parse(await readFile(join(sdkRoot, "package.json"), "utf8")) as {
    name?: unknown;
    version?: unknown;
  };
  if (typeof packageDocument.name !== "string" || typeof packageDocument.version !== "string") {
    throw new Error("SDK package.json is missing its package name or version");
  }
  const archiveName = `${packageDocument.name.replace(/^@/u, "").replaceAll("/", "-")}-${packageDocument.version}.tgz`;
  const archive = join(sdkRoot, ".api-extractor-temp", archiveName);
  await stat(archive).catch((error: unknown) => {
    throw new Error(`packed SDK archive is unavailable; run task sdk:pack (${String(error)})`);
  });
  await runCommand("tar", ["-xzf", archive, "-C", destination], repositoryRoot);
}

function importMap(): string {
  return JSON.stringify({
    imports: {
      "@bufbuild/protobuf": "/deps/protobuf/index.js",
      "@bufbuild/protobuf/codegenv2": "/deps/protobuf/codegenv2/index.js",
      "@bufbuild/protobuf/wire": "/deps/protobuf/wire/index.js",
      "@bufbuild/protobuf/wkt": "/deps/protobuf/wkt/index.js",
      "@connectrpc/connect": "/deps/connect/index.js",
      "@stacklok/mecatl-sdk": "/package/dist/index.js",
    },
  }).replaceAll("<", "\\u003c");
}

async function startOriginServer(packageRoot: string): Promise<OriginServer> {
  const roots = [
    { prefix: "/package/", root: packageRoot },
    {
      prefix: "/deps/protobuf/",
      root: join(sdkRoot, "node_modules", "@bufbuild", "protobuf", "dist", "esm"),
    },
    {
      prefix: "/deps/connect/",
      root: join(sdkRoot, "node_modules", "@connectrpc", "connect", "dist", "esm"),
    },
  ] as const;
  const server = createServer((request, response) => {
    void (async () => {
      const pathname = new URL(request.url ?? "/", "http://fixture.invalid").pathname;
      if (pathname === "/") {
        response.writeHead(200, {
          "cache-control": "no-store",
          "content-type": "text/html; charset=utf-8",
        });
        response.end(
          `<!doctype html><html><head><meta charset="utf-8"><script type="importmap">${importMap()}</script></head><body>mecatl browser fixture</body></html>`,
        );
        return;
      }

      for (const mapping of roots) {
        if (!pathname.startsWith(mapping.prefix)) continue;
        const suffix = decodeURIComponent(pathname.slice(mapping.prefix.length));
        const candidate = resolve(mapping.root, suffix);
        const fromRoot = relative(mapping.root, candidate);
        if (fromRoot === ".." || fromRoot.startsWith(`..${sep}`)) {
          response.writeHead(403).end();
          return;
        }
        const contents = await readFile(candidate);
        response.writeHead(200, {
          "cache-control": "no-store",
          "content-type": contentType(candidate),
        });
        response.end(contents);
        return;
      }
      response.writeHead(404).end();
    })().catch(() => response.writeHead(404).end());
  });
  await listen(server);
  const address = server.address() as AddressInfo;
  return {
    close: () => closeServer(server),
    origin: `http://127.0.0.1:${address.port}`,
  };
}

function contentType(path: string): string {
  switch (extname(path)) {
    case ".js":
      return "text/javascript; charset=utf-8";
    case ".json":
      return "application/json; charset=utf-8";
    default:
      return "application/octet-stream";
  }
}

function listen(server: Server): Promise<void> {
  return new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolveListen();
    });
  });
}

function closeServer(server: Server): Promise<void> {
  return new Promise((resolveClose, reject) => {
    server.close((error) => (error === undefined ? resolveClose() : reject(error)));
  });
}

async function startDaemon(options: {
  allowedOrigin: string;
  projectName: string;
  readyFile: string;
  storeDirectory: string;
  workspace: string;
}): Promise<{ child: ChildProcess; ready: ReadyDocument }> {
  const fixtureName = options.projectName === "chromium-full" ? "chromium.json" : "smoke.json";
  const script = join(browserDirectory, "fixtures", fixtureName);
  const args = [
    "serve",
    "--mock-script",
    script,
    "--workspace",
    options.workspace,
    "--store-dir",
    options.storeDirectory,
    "--ready-file",
    options.readyFile,
    "--grpc-addr",
    "127.0.0.1:0",
    "--http-addr",
    "127.0.0.1:0",
    "--cors-origins",
    options.allowedOrigin,
    "--metrics-addr",
    "",
    "--no-soul",
    "--no-user-model",
    "--no-scheduler",
    "--flight-recorder=false",
  ];
  const environment = { ...process.env };
  delete environment.ANTHROPIC_API_KEY;
  delete environment.OPENAI_API_KEY;
  delete environment.OPENROUTER_API_KEY;
  const child = spawn(join(repositoryRoot, "bin", "mecated"), args, {
    cwd: repositoryRoot,
    env: environment,
    stdio: ["ignore", "ignore", "pipe"],
  });
  let stderr = "";
  child.stderr?.setEncoding("utf8");
  child.stderr?.on("data", (chunk: string) => {
    stderr = `${stderr}${chunk}`.slice(-maxStderrBytes);
  });
  try {
    const ready = await waitForReady(child, options.readyFile, () => stderr);
    return { child, ready };
  } catch (error) {
    await stopProcess(child);
    throw error;
  }
}

async function waitForReady(
  child: ChildProcess,
  readyFile: string,
  stderr: () => string,
): Promise<ReadyDocument> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`mecated exited before readiness (${child.exitCode}):\n${stderr()}`);
    }
    try {
      const ready = JSON.parse(await readFile(readyFile, "utf8")) as ReadyDocument;
      if (ready.schema === "mecated-ready/1") return ready;
    } catch {
      // The ready file is atomically published; absence means keep polling.
    }
    await delay(20);
  }
  throw new Error(`timed out waiting for mecated readiness:\n${stderr()}`);
}

function assertLoopback(rawUrl: string): void {
  const url = new URL(rawUrl);
  if (url.protocol !== "http:" || url.hostname !== "127.0.0.1") {
    throw new Error(`browser daemon is not loopback HTTP: ${rawUrl}`);
  }
}

async function stopProcess(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null) return;
  const exited = new Promise<void>((resolveExit) => child.once("exit", () => resolveExit()));
  child.kill("SIGTERM");
  if ((await Promise.race([exited.then(() => true), delay(3_000).then(() => false)])) === false) {
    child.kill("SIGKILL");
    await exited;
  }
}

async function runCommand(command: string, args: string[], cwd: string): Promise<void> {
  await new Promise<void>((resolveRun, reject) => {
    const child = spawn(command, args, { cwd, stdio: ["ignore", "ignore", "pipe"] });
    let stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => {
      stderr = `${stderr}${chunk}`.slice(-maxStderrBytes);
    });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) {
        resolveRun();
        return;
      }
      reject(
        new Error(`${command} failed (code=${String(code)}, signal=${String(signal)}): ${stderr}`),
      );
    });
  });
}
