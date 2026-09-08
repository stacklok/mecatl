import { type ChildProcess, spawn as spawnChild } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { createInterface } from "node:readline";

import type { DiagnosticRecord } from "../src/index.js";
import { type SpawnedClient, spawn } from "../src/node.js";
import { fixture, repositoryRoot } from "./harness.js";

const mecatedBinary = join(repositoryRoot, "bin", "mecated");
const runtimeHelper = fixture("runtime-helper.mjs");

export interface ProductFixture {
  readonly client: SpawnedClient;
  readonly root: string;
  readonly workspace: string;
  close(): Promise<void>;
}

interface ProductFixtureOptions {
  args?: readonly string[];
  diagnostics?: (record: DiagnosticRecord) => void;
}

interface RuntimeHelperRecord {
  readonly content?: string;
  readonly daemonPid: number;
  readonly invoked?: boolean;
  readonly runtimeDirectory: string;
  readonly runtimeRemoved?: boolean;
  readonly stop?: string;
}

export interface RunningRuntimeHelper {
  readonly child: ChildProcess;
  readonly record: RuntimeHelperRecord;
  readonly root: string;
  readonly stderr: () => string;
  cleanup(): Promise<void>;
}

export async function spawnProductFixture(
  script: string,
  options: ProductFixtureOptions = {},
): Promise<ProductFixture> {
  const root = await createScratchRoot();
  const workspace = join(root, "workspace");
  await mkdir(workspace);
  try {
    const client = await spawn({
      args: daemonArguments(script, workspace, options.args),
      binaryPath: mecatedBinary,
      ...(options.diagnostics === undefined ? {} : { diagnostics: options.diagnostics }),
      env: isolatedEnvironment(root),
    });
    return {
      client,
      root,
      workspace,
      close: async () => {
        try {
          await client.close();
        } finally {
          await rm(root, { force: true, recursive: true });
        }
      },
    };
  } catch (error) {
    await rm(root, { force: true, recursive: true });
    throw error;
  }
}

export async function startRuntimeHelper(
  runtime: "bun" | "node",
  mode: "hold" | "roundtrip",
): Promise<RunningRuntimeHelper> {
  const root = await createScratchRoot();
  const workspace = join(root, "workspace");
  await mkdir(workspace);
  const executable = runtime === "node" ? process.execPath : (process.env.BUN_BIN ?? "bun");
  const child = spawnChild(
    executable,
    [runtimeHelper, mode, mecatedBinary, fixture("tool-lookup.json"), workspace],
    {
      cwd: repositoryRoot,
      env: { ...process.env, ...isolatedEnvironment(root) },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let stderr = "";
  child.stderr?.setEncoding("utf8");
  child.stderr?.on("data", (chunk: string) => {
    stderr += chunk;
  });

  try {
    const record = await firstRecord(child, () => stderr);
    return {
      child,
      record,
      root,
      stderr: () => stderr,
      cleanup: async () => {
        if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
        await childExit(child).catch(() => undefined);
        await rm(root, { force: true, recursive: true });
      },
    };
  } catch (error) {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    await childExit(child).catch(() => undefined);
    await rm(root, { force: true, recursive: true });
    throw error;
  }
}

export function childExit(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
  return new Promise((resolve) => child.once("exit", () => resolve()));
}

export function binaryPath(): string {
  return mecatedBinary;
}

function daemonArguments(
  script: string,
  workspace: string,
  extra: readonly string[] = [],
): string[] {
  return [
    "--mock-script",
    fixture(script),
    "--workspace",
    workspace,
    "--no-soul",
    "--no-user-model",
    "--no-scheduler",
    "--flight-recorder=false",
    // Client MCP tools are per-session catalog additions. The current root
    // authority snapshot is minted from the process-wide catalog before those
    // additions, so this wire-focused fixture selects the explicit no-op
    // evaluator while permission asks remain fully active.
    "--authority-evaluator",
    "noop",
    ...extra,
  ];
}

async function createScratchRoot(): Promise<string> {
  const scratch = join(repositoryRoot, ".scratch");
  await mkdir(scratch, { recursive: true });
  return mkdtemp(join(scratch, "sdk-product-e2e-"));
}

function isolatedEnvironment(root: string): NodeJS.ProcessEnv {
  return {
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_CONFIG_HOME: join(root, "config"),
    XDG_DATA_HOME: join(root, "data"),
    XDG_STATE_HOME: join(root, "state"),
  };
}

function firstRecord(child: ChildProcess, stderr: () => string): Promise<RuntimeHelperRecord> {
  return new Promise((resolve, reject) => {
    if (child.stdout === null) {
      reject(new Error("runtime helper stdout was not piped"));
      return;
    }
    const lines = createInterface({ input: child.stdout });
    const onError = (error: Error) => {
      lines.close();
      reject(error);
    };
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      lines.close();
      reject(
        new Error(
          `runtime helper exited before reporting readiness (code=${String(code)}, signal=${String(signal)}):\n${stderr()}`,
        ),
      );
    };
    child.once("error", onError);
    child.once("exit", onExit);
    lines.once("line", (line) => {
      child.off("error", onError);
      child.off("exit", onExit);
      lines.close();
      try {
        resolve(JSON.parse(line) as RuntimeHelperRecord);
      } catch (error) {
        reject(
          new Error(`runtime helper emitted invalid JSON ${JSON.stringify(line)}`, {
            cause: error,
          }),
        );
      }
    });
  });
}
