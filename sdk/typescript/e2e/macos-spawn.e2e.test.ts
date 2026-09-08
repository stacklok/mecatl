import { stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { expect, it } from "vitest";

import { spawn } from "../src/node.js";
import { fixture, repositoryRoot } from "./harness.js";

const DARWIN_SUN_PATH_BYTES = 104;

function processAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ESRCH") return false;
    throw error;
  }
}

it.skipIf(process.platform !== "darwin")("macOS spawns over UDS and exits cleanly", async () => {
  const representativePrimarySocket = join(tmpdir(), "mecatl-sdk-123456", "mecated.sock");
  const fallbackExpected = Buffer.byteLength(representativePrimarySocket) >= DARWIN_SUN_PATH_BYTES;
  const client = await spawn({
    args: ["--mock-script", fixture("attach.json")],
    binaryPath: join(repositoryRoot, "bin", "mecated"),
  });
  const { pid, socketPath } = client.daemon;
  const runtimeDirectory = dirname(socketPath);

  try {
    expect(client.daemon.transport).toBe("unix");
    expect(Buffer.byteLength(socketPath)).toBeLessThan(DARWIN_SUN_PATH_BYTES);
    if (fallbackExpected) {
      expect(socketPath).toMatch(/^\/tmp\/mecatl-sdk-[^/]+\/mecated\.sock$/u);
    }
    expect(processAlive(pid)).toBe(true);

    const session = await client.sessions.create({});
    const run = await session.run("complete the macOS spawn smoke");
    const terminal = await run.result();
    expect(terminal).toMatchObject({ stopReason: "end_turn" });
    expect(terminal.runId).not.toBe("");
    await session.delete();
  } finally {
    await client.close();
  }

  expect(processAlive(pid)).toBe(false);
  await expect(stat(runtimeDirectory)).rejects.toMatchObject({ code: "ENOENT" });
});
