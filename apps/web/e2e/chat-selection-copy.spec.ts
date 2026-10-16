// SPDX-License-Identifier: Apache-2.0

import { createServer, type Server } from "node:http";
import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { expect, type OfflineBff, test } from "./fixtures";

const sessionId = "s1";
const prefix = `/api/v1/sessions/${sessionId}`;

function event(kind: string, seq: number, payload: unknown, text = ""): RunStreamEvent {
  return {
    event: { kind, payload, runId: "run-a", seq: String(seq), text, turn: 1, unknown: false },
    type: "run.event",
  };
}

const frames: RunStreamEvent[] = [
  { runId: "run-a", sessionId, type: "run.started" },
  event("user_prompt", 1, { text: "List the files" }),
  event("message.delta", 2, undefined, "Listing the files now."),
  event("tool.call", 3, { args: '{"command":"ls"}', id: "call-1", name: "Bash" }),
  event("tool.result", 4, { callId: "call-1", content: "report.txt", isError: false }),
];

let activityServer: Server | undefined;
let activityPort = 0;

test.beforeAll(async () => {
  activityServer = createServer((_request, response) => {
    response.writeHead(200, { "Cache-Control": "no-store", "Content-Type": "text/event-stream" });
    response.write(
      frames.map((frame, index) => `id: ${index + 1}\ndata: ${JSON.stringify(frame)}\n\n`).join(""),
    );
  });
  await new Promise<void>((done) => activityServer?.listen(0, "127.0.0.1", done));
  const address = activityServer.address();
  if (!address || typeof address === "string") throw new Error("activity server has no port");
  activityPort = address.port;
});

test.afterAll(() => {
  activityServer?.closeAllConnections();
  activityServer?.close();
});

function runningSession(offlineBff: OfflineBff) {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "selection-proof",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { models: [], modelsSupported: false });
  offlineBff.json("GET", "/api/v1/sessions", {
    complete: true,
    items: [
      {
        capabilities: {
          delete: false,
          deleteReason: "",
          publicChat: true,
          publicChatReason: "",
          rename: false,
          renameReason: "",
        },
        createdAt: "2026-09-24T00:00:00Z",
        debugTargetSessionId: "",
        id: sessionId,
        modelId: "offline",
        state: "running",
        title: "A running chat",
        turns: 1,
        updatedAt: "2026-09-24T00:00:00Z",
      },
    ],
  });
  offlineBff.json("GET", prefix, {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: sessionId,
    mode: "default",
    state: "running",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  });
  offlineBff.json("GET", `${prefix}/transcript`, { complete: true, messages: [], sessionId });
  offlineBff.proxyLoopback(
    "GET",
    `${prefix}/activity`,
    `http://127.0.0.1:${activityPort}${prefix}/activity`,
  );
}

test("Add to chat leaves the screen-reader-only tool text out of a selection", async ({
  offlineBff,
  page,
}) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 800, width: 1280 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  // Tool rows are hidden by default.
  await page.getByRole("button", { name: "Chat options" }).click();
  await page.getByRole("menuitem", { name: "Show Tools" }).click();
  const transcript = page.getByRole("region", { name: "Conversation transcript" });
  await transcript.getByRole("button", { name: /1 tool/ }).click();
  const row = transcript.getByText("Tool: Bash").locator("xpath=..");
  await expect(row).toContainText("ls");
  await row.evaluate((element) => {
    const range = document.createRange();
    range.selectNodeContents(element);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
  });
  await transcript.dispatchEvent("pointerup");
  await page.getByRole("button", { name: "Add to chat" }).click();
  const composer = page.getByRole("textbox", { name: "Message Mecatl" });
  // The draft lands after a state update; wait for it instead of reading it once.
  await expect(composer).toHaveValue(/Bash/);
  const draft = await composer.inputValue();
  expect(draft).toContain("Bash");
  expect(draft).not.toContain("Tool:");
  expect(draft).not.toContain("completed");
});
