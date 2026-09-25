// SPDX-License-Identifier: Apache-2.0

import { mkdir, rm } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import { resolve } from "node:path";
import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { type Bootstrapped, bootstrap } from "../../server/src/bootstrap";
import { silentLogger } from "../../server/src/log";
import { expect, test } from "./fixtures";

const root = resolve(import.meta.dirname, "../../..");
const binary = process.env.STUDIO_MECATED_BIN ?? resolve(root, "bin/mecated");
const home = resolve(root, ".scratch/studio-chat-approval-browser-home");
const csrf = {
  Cookie: "studio_csrf=browser-proof",
  "Sec-Fetch-Site": "same-origin",
  "X-Studio-CSRF": "browser-proof",
  "Content-Type": "application/json",
};

let booted: Bootstrapped;
let activityServer: Server | undefined;

test.beforeAll(async () => {
  await rm(home, { force: true, recursive: true });
  await mkdir(home, { recursive: true });
  const saved = new Map<string, string | undefined>();
  for (const key of [
    "HOME",
    "XDG_CACHE_HOME",
    "XDG_CONFIG_HOME",
    "XDG_DATA_HOME",
    "XDG_STATE_HOME",
    "DO_NOT_TRACK",
  ]) {
    saved.set(key, process.env[key]);
    process.env[key] = key === "DO_NOT_TRACK" ? "1" : resolve(home, key.toLowerCase());
  }
  try {
    booted = await bootstrap({
      environment: { MECATED_BIN: binary, MECATL_DEV_MOCK: "1", STUDIO_LOG_LEVEL: "error" },
      logger: silentLogger,
    });
    await booted.runtime.ready();
  } finally {
    for (const [key, value] of saved) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
});

test.afterAll(async () => {
  activityServer?.closeAllConnections();
  activityServer?.close();
  await booted?.runtime.close();
  await rm(home, { force: true, recursive: true });
});

function started(sessionId: string, runId: string): RunStreamEvent {
  return { runId, sessionId, type: "run.started" };
}

function event(kind: string, runId: string, seq: number, payload: unknown): RunStreamEvent {
  return {
    event: { kind, payload, runId, seq: String(seq), text: "", turn: 1, unknown: false },
    type: "run.event",
  };
}

function stream(frames: RunStreamEvent[]): string {
  return frames
    .map((frame, index) => `id: ${index + 1}\ndata: ${JSON.stringify(frame)}\n\n`)
    .join("");
}

test("approves and denies exact asks through the browser journey", async ({
  context,
  offlineBff,
  page,
}) => {
  test.setTimeout(120_000);
  const created = await booted.app.request("/api/v1/sessions", {
    body: JSON.stringify({ mode: "default", reasoningEffort: "default", toolAccess: "all" }),
    headers: csrf,
    method: "POST",
  });
  expect(created.status).toBe(201);
  const { id: sessionId } = (await created.json()) as { id: string };
  const prefix = `/api/v1/sessions/${sessionId}`;
  const verdicts = new Map<string, number>();
  let frames: RunStreamEvent[] = [];
  let state = "running";
  const ask = (runId: string, askId: string, tool: string, reason: string, seq = 2) => [
    started(sessionId, runId),
    event("tool.call", runId, 1, {
      id: `call-${askId}`,
      name: tool,
      args: '{"path":"report.txt"}',
    }),
    event("permission.ask", runId, seq, {
      askId,
      args: tool === "PresentPlan" ? '{"plan":"Review the report"}' : '{"path":"report.txt"}',
      callId: `call-${askId}`,
      reason,
      tool,
    }),
  ];
  const verdict = (runId: string, askId: string, status = 204) => {
    const path = `${prefix}/runs/${runId}/${runId.startsWith("plan") ? "plan-asks" : "permissions"}/${askId}`;
    offlineBff.on("POST", path, () => {
      verdicts.set(path, (verdicts.get(path) ?? 0) + 1);
      return status === 204
        ? { status }
        : {
            body: JSON.stringify({ code: "stale_run_control", detail: "Ask already resolved" }),
            contentType: "application/problem+json",
            status,
          };
    });
    return path;
  };
  const open = async () => {
    await page.goto(`/workspace/chat?sessionId=${sessionId}`);
    await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  };
  const reload = async () => {
    await page.reload();
    await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  };

  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "approval-proof",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", {
    connection: "reachable",
    signInRequired: false,
  });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
    features: ["exact_plan_ask_control"],
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { models: [], modelsSupported: false });
  offlineBff.on("GET", "/api/v1/sessions", () => ({
    body: JSON.stringify({
      complete: true,
      items: [
        {
          capabilities: { delete: false, deleteReason: "", rename: false, renameReason: "" },
          createdAt: "2026-09-24T00:00:00Z",
          debugTargetSessionId: "",
          id: sessionId,
          modelId: "offline",
          state,
          title: "Approval journey",
          turns: 1,
          updatedAt: "2026-09-24T00:00:00Z",
        },
      ],
    }),
    contentType: "application/json",
  }));
  offlineBff.on("GET", prefix, () => ({
    body: JSON.stringify({
      capabilities: { image: false, manualCompaction: false, modelSelection: false },
      id: sessionId,
      mode: "default",
      state,
      usage: {
        cacheReadTokens: "0",
        cacheWriteTokens: "0",
        inputTokens: "0",
        outputTokens: "0",
        reasoningTokens: "0",
      },
    }),
    contentType: "application/json",
  }));
  offlineBff.json("GET", `${prefix}/transcript`, { complete: true, messages: [], sessionId });
  activityServer = createServer((_request, response) => {
    response.writeHead(200, {
      "Cache-Control": "no-store",
      "Content-Type": "text/event-stream",
    });
    response.write(stream(frames));
  });
  await new Promise<void>((resolveListen) => activityServer?.listen(0, "127.0.0.1", resolveListen));
  const address = activityServer.address();
  if (!address || typeof address === "string") throw new Error("missing offline activity port");
  offlineBff.proxyLoopback(
    "GET",
    `${prefix}/activity`,
    `http://127.0.0.1:${address.port}${prefix}/activity`,
  );

  frames = ask("tool-allow", "ask-allow", "Read", "Allow this read");
  const allowPath = verdict("tool-allow", "ask-allow");
  offlineBff.on("POST", allowPath, async () => {
    verdicts.set(allowPath, (verdicts.get(allowPath) ?? 0) + 1);
    await new Promise((done) => setTimeout(done, 200));
    return { status: 204 };
  });
  await open();
  const allowCard = page.getByRole("region", { name: "Permission required: Read" });
  await expect(allowCard.getByText("Allow this read")).toBeVisible();
  await allowCard.getByRole("button", { name: "Allow once" }).evaluate((button) => {
    (button as HTMLButtonElement).click();
    (button as HTMLButtonElement).click();
  });
  await expect.poll(() => verdicts.get(allowPath)).toBe(1);
  await expect(allowCard).toHaveCount(0);
  expect(offlineBff.requestsFor("POST", allowPath)[0]?.postDataJSON()).toEqual({
    verdict: "allow_once",
  });

  frames = ask("tool-deny", "ask-deny", "Write", "Deny this write");
  const denyPath = verdict("tool-deny", "ask-deny");
  await reload();
  await page
    .getByRole("region", { name: "Permission required: Write" })
    .getByRole("button", { name: "Deny" })
    .click();
  await expect.poll(() => verdicts.get(denyPath)).toBe(1);
  expect(offlineBff.requestsFor("POST", denyPath)[0]?.postDataJSON()).toEqual({ verdict: "deny" });

  frames = ask("plan-approve", "ask-approve", "PresentPlan", "Approve this plan");
  const approvePath = verdict("plan-approve", "ask-approve");
  await reload();
  const approveCard = page.getByRole("region", { name: "Plan review" });
  await expect(approveCard.getByText("Review the report", { exact: true })).toBeVisible();
  await approveCard.getByRole("button", { name: "Approve & run" }).click();
  await expect.poll(() => verdicts.get(approvePath)).toBe(1);
  expect(offlineBff.requestsFor("POST", approvePath)[0]?.postDataJSON()).toEqual({
    verdict: "approve",
  });

  frames = ask("plan-iterate", "ask-iterate", "PresentPlan", "Iterate this plan");
  const iteratePath = verdict("plan-iterate", "ask-iterate");
  await reload();
  await page
    .getByRole("region", { name: "Plan review" })
    .getByRole("button", { name: "Iterate" })
    .click();
  await expect.poll(() => verdicts.get(iteratePath)).toBe(1);
  expect(offlineBff.requestsFor("POST", iteratePath)[0]?.postDataJSON()).toEqual({
    verdict: "iterate",
  });

  // Both tabs saw the same scripted ask. The first acknowledgement is scripted;
  // the second submit traverses the real BFF, released SDK, and mock daemon.
  // That daemon has no such pending ask, so its exact target must be rejected.
  // The daemon's consumed-ask duplicate invariant is pinned separately by its
  // exact control tests; this browser step verifies cross-tab reconciliation.
  frames = ask("plan-two-tabs", "ask-shared", "PresentPlan", "Shared plan");
  const sharedPath = verdict("plan-two-tabs", "ask-shared");
  await reload();
  const other = await context.newPage();
  await other.goto(`/workspace/chat?sessionId=${sessionId}`);
  await expect(other.getByRole("region", { name: "Plan review" })).toBeVisible();
  await page
    .getByRole("region", { name: "Plan review" })
    .getByRole("button", { name: "Approve & run" })
    .click();
  await expect.poll(() => verdicts.get(sharedPath)).toBe(1);
  let realRejection: { code: string; status: number } | undefined;
  offlineBff.on("POST", sharedPath, async (request) => {
    const response = await booted.app.request(new URL(request.url()).pathname, {
      body: request.postData() ?? "",
      headers: csrf,
      method: "POST",
    });
    const body = await response.text();
    realRejection = { code: (JSON.parse(body) as { code: string }).code, status: response.status };
    return {
      body,
      contentType: response.headers.get("content-type") ?? "application/problem+json",
      status: response.status,
    };
  });
  await other
    .getByRole("region", { name: "Plan review" })
    .getByRole("button", { name: "Approve & run" })
    .click();
  await expect(other.getByText(/outcome is uncertain/i)).toBeVisible();
  expect(realRejection).toEqual({ code: "stale_run_control", status: 409 });
  expect(offlineBff.requestsFor("POST", sharedPath)).toHaveLength(2);
  await other.close();

  frames = [
    ...ask("plan-old", "ask-reused", "PresentPlan", "Old plan"),
    ...ask("plan-new", "ask-reused", "PresentPlan", "New plan"),
  ];
  await reload();
  const oldCard = page.getByText("Old plan", { exact: true }).locator("xpath=ancestor::section[1]");
  await expect(oldCard.getByText(/plan ask is stale/i)).toBeVisible();
  await expect(oldCard.getByRole("button", { name: "Approve & run" })).toHaveCount(0);
  const currentCard = page
    .getByText("New plan", { exact: true })
    .locator("xpath=ancestor::section[1]");
  await expect(currentCard.getByRole("button", { name: "Approve & run" })).toBeEnabled();
  expect(
    offlineBff.requestsFor("POST", `${prefix}/runs/plan-old/plan-asks/ask-reused`),
  ).toHaveLength(0);

  frames = ask("tool-escape", "ask-escape", "Read", "Escape denies this ask");
  const escapePath = verdict("tool-escape", "ask-escape");
  await reload();
  await page.getByRole("button", { name: "Chat options" }).click();
  await page.getByRole("menuitem", { name: /Show Tools|Hide Tools/ }).focus();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Chat options" })).toBeFocused();
  await expect(page.getByRole("region", { name: "Permission required: Read" })).toBeVisible();
  expect(offlineBff.requestsFor("POST", escapePath)).toHaveLength(0);
  // Mobile browsers can briefly move focus to body after closing a portaled menu.
  await page.getByRole("button", { name: "Chat options" }).evaluate((button) => button.blur());
  expect(await page.evaluate(() => document.activeElement === document.body)).toBe(true);
  await page.keyboard.press("Escape");
  await expect.poll(() => verdicts.get(escapePath)).toBe(1);
  expect(offlineBff.requestsFor("POST", escapePath)[0]?.postDataJSON()).toEqual({
    verdict: "deny",
  });

  frames = [];
  state = "idle";
  await reload();
  const composer = page.getByRole("textbox", { name: "Message Mecatl" });
  await composer.fill("Keep this draft");
  await composer.focus();
  await page.keyboard.press("Escape");
  await expect(page.getByText("Press Escape again to clear the unsent draft.")).toBeVisible();
  await expect(composer).toHaveValue("Keep this draft");
  await page.keyboard.press("Escape");
  await expect(composer).toHaveValue("");
  await page.close();
});
