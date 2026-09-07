import { join } from "node:path";
import { expect, it } from "vitest";

import { spawn } from "../src/node.js";
import { fixture, repositoryRoot } from "./harness.js";

it("the default spawn argv starts a real mecated to readiness", async () => {
  const client = await spawn({
    args: ["--mock-script", fixture("attach.json")],
    binaryPath: join(repositoryRoot, "bin", "mecated"),
  });
  try {
    const session = await client.sessions.create({});
    expect(session.id).not.toBe("");
  } finally {
    await client.close();
  }
});
