// SPDX-License-Identifier: Apache-2.0

import type { Locator, Page } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

const sessionId = "chat-a";
const widths = [640, 1280];

function serve(bff: OfflineBff) {
  bff.json("GET", "/api/v1/auth/session", {
    account: "composer",
    mode: "oidc",
    status: "authenticated",
  });
  bff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  bff.json("GET", "/api/v1/runtime", {
    capabilities: {
      image: false,
      manualCompaction: true,
      modelSelection: true,
      posture: "managed",
    },
    connection: "online",
    features: [],
  });
  bff.json("GET", "/api/v1/settings/runtime", {
    buildId: "test",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "Managed by the deployment.",
      routingConfiguration: false,
      routingConfigurationReason: "Managed by the deployment.",
    },
    models: [
      {
        contextLimit: "200000",
        displayName: "Claude Sonnet 4.5",
        id: "claude-sonnet-4-5",
        image: false,
        providerDisplayName: "Anthropic",
        providerId: "anthropic",
        reasoning: true,
      },
    ],
    modelsReason: "",
    modelsSupported: true,
    providerEndpoint: "",
    providers: [],
    serverImplementation: "test",
  });
  bff.json("GET", "/api/v1/sessions", {
    complete: true,
    items: [
      {
        capabilities: {
          delete: true,
          deleteReason: "",
          fork: true,
          publicChat: true,
          publicChatReason: "",
          rename: true,
          renameReason: "",
        },
        createdAt: "2026-09-25T10:00:00Z",
        debugTargetSessionId: "",
        id: sessionId,
        kind: "main",
        modelId: "claude-sonnet-4-5",
        state: "idle",
        title: "Refactor the permission engine",
        turns: 1,
        updatedAt: "2026-09-25T12:00:00Z",
      },
    ],
  });
  bff.json("GET", `/api/v1/sessions/${sessionId}`, {
    capabilities: {
      audio: false,
      builtInShell: false,
      executionFiles: true,
      image: false,
      manualCompaction: true,
      modelSelection: true,
    },
    id: sessionId,
    mode: "default",
    model: {
      contextWindow: "200000",
      id: "claude-sonnet-4-5",
      providerDisplayName: "Anthropic",
      providerId: "anthropic",
      reasoningEffort: "medium",
    },
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "36000",
      outputTokens: "4000",
      reasoningTokens: "0",
    },
  });
  bff.json("GET", `/api/v1/sessions/${sessionId}/transcript`, {
    complete: true,
    messages: [{ images: [], role: "user", text: "Split the permission engine.", toolCalls: [] }],
    sessionId,
  });
  bff.on("GET", `/api/v1/sessions/${sessionId}/activity`, () => ({
    body: "",
    contentType: "text/event-stream",
  }));
}

/** The control lies wholly inside the tray: the pill row did not scroll it out of view. */
async function expectInsideTray(page: Page, control: Locator) {
  const tray = await page
    .getByRole("region", { name: "Chat configuration and usage" })
    .boundingBox();
  const box = await control.boundingBox();
  expect(tray).not.toBeNull();
  expect(box).not.toBeNull();
  if (!tray || !box) return;
  expect(box.x).toBeGreaterThanOrEqual(tray.x);
  expect(box.x + box.width).toBeLessThanOrEqual(tray.x + tray.width + 0.5);
}

/** `line` sits on its own line under the pill row's `pill`, not beside it. */
async function expectBelow(line: Locator, pill: Locator) {
  const lineBox = await line.boundingBox();
  const pillBox = await pill.boundingBox();
  expect(lineBox).not.toBeNull();
  expect(pillBox).not.toBeNull();
  if (!lineBox || !pillBox) return;
  expect(lineBox.y).toBeGreaterThanOrEqual(pillBox.y + pillBox.height - 0.5);
}

test("a draft's Execution picker takes its own line and leaves Mode and Model in view", async ({
  offlineBff,
  page,
}) => {
  serve(offlineBff);
  await page.goto("/workspace/chat");
  const tray = page.getByRole("region", { name: "Chat configuration and usage" });
  for (const width of widths) {
    await page.setViewportSize({ height: 800, width });
    const execution = tray.getByLabel("Execution", { exact: true });
    await expect(execution).toBeVisible();
    await expectInsideTray(page, execution);
    const mode = tray.getByRole("button", { name: /^Mode(\s|$)/ });
    await expectInsideTray(page, mode);
    await expectInsideTray(page, tray.getByRole("button", { name: /^Model(\s|$)/ }));
    await expectBelow(execution, mode);
  }
});

test("a live chat's execution status takes its own line and leaves Compact in view", async ({
  offlineBff,
  page,
}) => {
  serve(offlineBff);
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const tray = page.getByRole("region", { name: "Chat configuration and usage" });
  for (const width of widths) {
    await page.setViewportSize({ height: 800, width });
    const status = tray.getByRole("status", { name: "Session execution capabilities" });
    await expect(status).toHaveText("Files: available · Built-in Shell: unavailable");
    await expectInsideTray(page, status);
    const model = tray.getByRole("button", { name: /^Model(\s|$)/ });
    await expectInsideTray(page, model);
    await expectBelow(status, model);
  }
  // With room for Mode, Model, usage, and Compact, the status no longer pushes Compact out.
  await page.setViewportSize({ height: 800, width: 1280 });
  await expectInsideTray(page, tray.getByRole("button", { name: "Compact" }));
});
