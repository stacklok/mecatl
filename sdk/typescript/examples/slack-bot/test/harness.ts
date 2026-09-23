import { type ChildProcess, spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

/** Same fixture the SDK's own e2e harness (sdk/typescript/e2e/harness.ts) asserts against. */
export const cannedMockReply =
  "Mock provider: no real model is configured. Set OPENAI_API_KEY for live use.";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../..");
const testDirectory = dirname(fileURLToPath(import.meta.url));

/** Resolves a `--mock-script` fixture under this package's own `test/fixtures/`. */
export function fixture(name: string): string {
  return join(testDirectory, "fixtures", name);
}

interface ReadyDocument {
  grpc_address: string;
  schema: string;
}

export interface Daemon {
  baseUrl: string;
  workspace: string;
}

export interface MockDaemonOptions {
  /** Path to a `--mock-script` fixture (see `sdk/typescript/e2e/fixtures/*.json` for the
   * format) — drives the mock provider through scripted tool calls instead of always
   * returning `cannedMockReply`. Omit for the default canned-reply behavior. */
  script?: string;
}

/** Spawns a real, offline `mecated --mock` daemon for the duration of `run`. */
export async function withMockDaemon<T>(
  run: (daemon: Daemon) => Promise<T>,
  options: MockDaemonOptions = {},
): Promise<T> {
  const runtimeDirectory = await mkdtemp(join(tmpdir(), "mecatl-slack-bot-test-"));
  const readyFile = join(runtimeDirectory, "ready.json");
  const workspace = join(runtimeDirectory, "workspace");
  const configHome = join(runtimeDirectory, "config");
  const stateHome = join(runtimeDirectory, "state");
  await Promise.all([mkdir(workspace), mkdir(configHome), mkdir(stateHome)]);
  const args = [
    "serve",
    "--mock",
    "--workspace",
    workspace,
    "--ready-file",
    readyFile,
    "--grpc-addr",
    "127.0.0.1:0",
    "--http-addr",
    "",
    "--metrics-addr",
    "",
    "--no-soul",
    "--no-user-model",
    "--permissions-conventional=false",
    "--no-scheduler",
    "--flight-recorder=false",
  ];
  if (options.script !== undefined) args.push("--mock-script", options.script);

  const environment = { ...process.env };
  environment.HOME = runtimeDirectory;
  environment.XDG_CONFIG_HOME = configHome;
  environment.XDG_STATE_HOME = stateHome;
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
    stderr += chunk;
  });

  try {
    const ready = await waitForReady(child, readyFile, () => stderr);
    return await run({ baseUrl: `http://${ready.grpc_address}`, workspace });
  } finally {
    await stop(child);
    await rm(runtimeDirectory, { force: true, recursive: true });
  }
}

async function waitForReady(
  child: ChildProcess,
  path: string,
  stderr: () => string,
): Promise<ReadyDocument> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`mecated exited before readiness (${child.exitCode}):\n${stderr()}`);
    }
    try {
      const ready = JSON.parse(await readFile(path, "utf8")) as ReadyDocument;
      if (ready.schema === "mecated-ready/1") return ready;
    } catch {
      // The ready file is atomically published; absence means keep polling.
    }
    await delay(20);
  }
  throw new Error(`timed out waiting for mecated readiness:\n${stderr()}`);
}

async function stop(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null) return;
  child.kill("SIGTERM");
  const exited = new Promise<void>((resolveExit) => child.once("exit", () => resolveExit()));
  if ((await Promise.race([exited.then(() => true), delay(3_000).then(() => false)])) === false) {
    child.kill("SIGKILL");
    await exited;
  }
}
