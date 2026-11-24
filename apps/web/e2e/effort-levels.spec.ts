// SPDX-License-Identifier: Apache-2.0

import { expect, type OfflineBff, test } from "./fixtures";

const sessionId = "chat-a";
const lowered = "This model doesn't accept Extra high; requests use High.";

/**
 * A live chat on a model that takes low, medium, and high. The session asks
 * for xhigh, so its requests carry high.
 */
function serve(bff: OfflineBff) {
  bff.json("GET", "/api/v1/auth/session", {
    account: "effort",
    mode: "oidc",
    status: "authenticated",
  });
  bff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  bff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, manualCompaction: false, modelSelection: true },
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
        contextLimit: "400000",
        displayName: "GPT-5",
        effortLevels: { known: true, levels: ["low", "medium", "high"] },
        id: "gpt-5",
        image: false,
        providerDisplayName: "OpenAI",
        providerId: "openai",
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
        modelId: "gpt-5",
        state: "idle",
        title: "Effort levels",
        turns: 1,
        updatedAt: "2026-09-25T12:00:00Z",
      },
    ],
  });
  // The successor a fork opens gets the same detail; only its request is read here.
  for (const id of [sessionId, "chat-b"]) {
    bff.json("GET", `/api/v1/sessions/${id}`, {
      capabilities: {
        audio: false,
        builtInShell: false,
        executionFiles: false,
        image: false,
        manualCompaction: false,
        modelSelection: true,
      },
      id,
      mode: "default",
      model: {
        contextWindow: "400000",
        id: "gpt-5",
        providerDisplayName: "OpenAI",
        providerId: "openai",
        reasoningEffort: "high",
        requestedReasoningEffort: "xhigh",
      },
      state: "idle",
      usage: {
        cacheReadTokens: "0",
        cacheWriteTokens: "0",
        inputTokens: "0",
        outputTokens: "0",
        reasoningTokens: "0",
      },
    });
    bff.json("GET", `/api/v1/sessions/${id}/transcript`, {
      complete: true,
      messages: [{ images: [], role: "user", text: "Think hard about this.", toolCalls: [] }],
      sessionId: id,
    });
    bff.on("GET", `/api/v1/sessions/${id}/activity`, () => ({
      body: "",
      contentType: "text/event-stream",
    }));
  }
  bff.json("POST", `/api/v1/sessions/${sessionId}/fork`, { id: "chat-b" }, 201);
}

function forkBodies(bff: OfflineBff) {
  return bff
    .requestsFor("POST", `/api/v1/sessions/${sessionId}/fork`)
    .map((request) => request.postDataJSON());
}

test("the Effort menu offers the model's levels and Auto resets the fork", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-desktop", "the phone sheet has its own journey");
  serve(offlineBff);
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const tray = page.getByRole("region", { name: "Chat configuration and usage" });

  await tray.getByRole("button", { name: /^Model GPT-5 · Extra high/ }).click();
  await page.getByRole("menuitem", { name: /^Effort/ }).hover();
  // The Effort submenu, not the Model menu that opened it.
  const effort = page
    .getByRole("menu")
    .filter({ hasNot: page.getByRole("menuitem", { name: /^Effort/ }) });
  await expect(effort.getByRole("menuitem")).toHaveText(["Auto", "Low", "Medium", "High"]);
  await expect(effort.getByRole("note")).toHaveText(lowered);

  await effort.getByRole("menuitem", { name: "Auto" }).click();
  await expect
    .poll(() => forkBodies(offlineBff))
    .toEqual([{ model: { id: "gpt-5", providerId: "openai" }, reasoningEffort: "default" }]);
});

test("the phone sheet offers the same levels and note", async ({ offlineBff, page }, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-mobile", "the mobile project sets the width");
  serve(offlineBff);
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);

  await page.getByRole("button", { name: "Composer options" }).click();
  const sheet = page.getByRole("dialog", { name: "Composer options" });
  await sheet.getByRole("button", { name: /^Model GPT-5 · Extra high/ }).click();
  const effort = sheet.getByRole("group", { name: "Effort" });
  await expect(effort.getByRole("button")).toHaveText(["Auto", "Low", "Medium", "High"]);
  await expect(effort.getByRole("note")).toHaveText(lowered);

  await effort.getByRole("button", { name: "Medium" }).click();
  await expect
    .poll(() => forkBodies(offlineBff))
    .toEqual([{ model: { id: "gpt-5", providerId: "openai" }, reasoningEffort: "medium" }]);
});
