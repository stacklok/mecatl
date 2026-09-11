import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures.js";

interface SmokeResult {
  readonly attachedTerminal:
    | {
        readonly runId: string;
        readonly stopReason: string;
        readonly text: string;
      }
    | undefined;
  readonly nodeSpawn: boolean;
  readonly ownedRunId: string;
  readonly ownedStopReason: string;
  readonly phases: readonly string[];
  readonly status: string;
}

async function browserSmoke(page: Page, origin: string, baseUrl: string): Promise<SmokeResult> {
  await page.goto(origin, { waitUntil: "domcontentloaded" });
  return page.evaluate(
    async ({ daemonUrl }) => {
      const packageName: string = "@stacklok-oss/mecatl-sdk";
      const sdk = (await import(packageName)) as typeof import("../../src/index.js");
      const client = sdk.connect({ baseUrl: daemonUrl, credentials: "include" });
      try {
        const session = await client.sessions.create({});
        const run = await session.run("complete the browser engine smoke");
        const ownedResult = run.result();
        const attached = await session.attach(run.id);
        const phases: string[] = [];
        let attachedTerminal: { runId: string; stopReason: string; text: string } | undefined;
        for await (const envelope of attached) {
          phases.push(envelope.phase);
          if (envelope.kind === "event" && envelope.event.kind === "result") {
            attachedTerminal = {
              runId: envelope.event.runId,
              stopReason: envelope.event.payload.stop,
              text: envelope.event.payload.text,
            };
          }
        }
        const owned = await ownedResult;
        await session.delete();
        return {
          attachedTerminal,
          nodeSpawn: "spawn" in sdk,
          ownedRunId: owned.runId,
          ownedStopReason: owned.stopReason,
          phases,
          status: client.status.getSnapshot(),
        };
      } finally {
        await client.close();
      }
    },
    { daemonUrl: baseUrl },
  );
}

function expectSmoke(result: SmokeResult): void {
  expect(result).toMatchObject({
    attachedTerminal: {
      runId: result.ownedRunId,
      stopReason: "end_turn",
      text: "Browser engine smoke completed",
    },
    nodeSpawn: false,
    ownedStopReason: "end_turn",
    status: "online",
  });
  expect(result.ownedRunId).not.toBe("");
  expect(result.phases.length).toBeGreaterThan(0);
  expect(result.phases.every((phase) => phase === "replay" || phase === "live")).toBe(true);
}

test("Firefox imports connects runs and attaches", async ({ browserHarness, page }) => {
  expectSmoke(await browserSmoke(page, browserHarness.origin, browserHarness.baseUrl));
});

test("WebKit imports connects runs and attaches", async ({ browserHarness, page }) => {
  expectSmoke(await browserSmoke(page, browserHarness.origin, browserHarness.baseUrl));
});
