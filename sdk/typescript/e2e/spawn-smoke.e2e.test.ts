import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, it, vi } from "vitest";

import { spawn } from "../src/node.js";
import { fixture, repositoryRoot } from "./harness.js";
import { isolatedEnvironment } from "./product-harness.js";

it("the default spawn argv starts a real mecated to readiness", async () => {
  const root = await mkdtemp(join(tmpdir(), "mecatl-sdk-spawn-smoke-"));
  const ambient = join(root, "ambient");
  const owned = join(root, "owned");
  try {
    await mkdir(join(ambient, ".config", "mecatl"), { recursive: true });
    await mkdir(owned);
    await writeFile(join(ambient, ".config", "mecatl", "settings.yaml"), "permissionMode: auto\n");
    vi.stubEnv("HOME", ambient);
    vi.stubEnv("XDG_CONFIG_HOME", join(ambient, ".config"));

    const client = await spawn({
      args: ["--mock-script", fixture("attach.json")],
      binaryPath: join(repositoryRoot, "bin", "mecated"),
      env: isolatedEnvironment(owned),
    });
    try {
      const session = await client.sessions.create({});
      expect(session.id).not.toBe("");
    } finally {
      await client.close();
    }
    await expect(
      spawn({
        args: ["--mock-script", fixture("attach.json")],
        binaryPath: join(repositoryRoot, "bin", "mecated"),
        env: { ...isolatedEnvironment(ambient), XDG_CONFIG_HOME: join(ambient, ".config") },
      }).then((unexpectedClient) => unexpectedClient.close()),
    ).rejects.toMatchObject({ code: "spawn_failed" });
  } finally {
    vi.unstubAllEnvs();
    await rm(root, { force: true, recursive: true });
  }
});
