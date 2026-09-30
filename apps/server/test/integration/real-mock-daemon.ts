// SPDX-License-Identifier: Apache-2.0

import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import type { Client } from "@stacklok-oss/mecatl-sdk";
import { connect } from "@stacklok-oss/mecatl-sdk/node";

const repositoryRoot = resolve(import.meta.dirname, "../../../..");

/** A test-owned loopback daemon. Its only provider is the checked-in mock script. */
export async function withRealMockDaemon<T>(
  script: string,
  exercise: (client: Client) => Promise<T>,
): Promise<T> {
  const scratch = join(repositoryRoot, ".scratch");
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "studio-approval-real-"));
  const readyFile = join(root, "ready.json");
  const workspace = join(root, "workspace");
  await Promise.all([mkdir(workspace), mkdir(join(root, "home")), mkdir(join(root, "tmp"))]);
  const binary = process.env.STUDIO_MECATED_BIN ?? join(repositoryRoot, "bin", "mecated");
  const child = spawn(
    binary,
    [
      "serve",
      "--mock-script",
      join(repositoryRoot, "apps", "web", "e2e", "fixtures", script),
      "--workspace",
      workspace,
      "--store-dir",
      join(root, "store"),
      "--user-model-dir",
      join(root, "user-model"),
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
      "--no-scheduler",
      "--flight-recorder=false",
      "--authority-evaluator",
      "noop",
      "--trust-project",
    ],
    {
      cwd: repositoryRoot,
      env: {
        HOME: join(root, "home"),
        PATH: process.env.PATH,
        TMPDIR: join(root, "tmp"),
        DO_NOT_TRACK: "1",
        XDG_CACHE_HOME: join(root, "cache"),
        XDG_CONFIG_HOME: join(root, "config"),
        XDG_DATA_HOME: join(root, "data"),
        XDG_STATE_HOME: join(root, "state"),
      },
      stdio: ["ignore", "ignore", "pipe"],
    },
  );
  let stderr = "";
  child.stderr?.setEncoding("utf8");
  child.stderr?.on("data", (chunk: string) => {
    stderr += chunk;
  });
  let client: Client | undefined;
  try {
    const deadline = Date.now() + 10_000;
    let address: string | undefined;
    while (Date.now() < deadline) {
      if (child.exitCode !== null) throw new Error(`mock daemon exited: ${stderr}`);
      try {
        const ready = JSON.parse(await readFile(readyFile, "utf8")) as {
          grpc_address?: string;
          schema?: string;
        };
        if (ready.schema === "mecated-ready/1") address = ready.grpc_address;
      } catch {
        // Readiness is atomically published after the loopback listener starts.
      }
      if (address !== undefined) break;
      await delay(20);
    }
    if (address === undefined) throw new Error(`mock daemon readiness timed out: ${stderr}`);
    if (!/^127\.0\.0\.1:\d+$/u.test(address)) throw new Error("mock daemon is not on loopback");
    client = connect({ baseUrl: `http://${address}` });
    return await exercise(client);
  } finally {
    await client?.close();
    if (child.exitCode === null && child.signalCode === null) {
      const exited = new Promise<void>((done) => child.once("exit", () => done()));
      child.kill("SIGTERM");
      if (!(await Promise.race([exited.then(() => true), delay(3_000).then(() => false)]))) {
        child.kill("SIGKILL");
        await exited;
      }
    }
    await rm(root, { force: true, recursive: true });
  }
}
