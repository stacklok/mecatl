// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "./fixtures";

test("an idle open chat picks up a short recorded delivery and its reply", async ({
  offlineBff,
  page,
}) => {
  let updatedAt = "2026-09-24T12:00:00Z";
  let savedMessages: unknown[] = [];
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", {
    connection: "reachable",
    signInRequired: false,
  });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed", scheduling: true },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", {
    models: [],
    modelsSupported: false,
  });
  offlineBff.on("GET", "/api/v1/sessions", () => ({
    body: JSON.stringify({
      complete: true,
      items: [
        {
          capabilities: {
            copyId: true,
            copyIdReason: "",
            delete: false,
            deleteReason: "",
            fork: true,
            forkReason: "",
            inspect: true,
            inspectReason: "",
            publicChat: true,
            publicChatReason: "",
            rename: false,
            renameReason: "",
            viewTranscript: true,
            viewTranscriptReason: "",
          },
          createdAt: "2026-09-24T12:00:00Z",
          debugTargetSessionId: "",
          id: "chat-a",
          kind: "main",
          modelId: "offline",
          state: "idle",
          title: "Scheduled chat",
          titleProvenance: "",
          titleRevision: "0",
          turns: 0,
          updatedAt,
        },
      ],
    }),
    contentType: "application/json",
  }));
  offlineBff.json("GET", "/api/v1/sessions/chat-a", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "chat-a",
    kind: "main",
    mode: "default",
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  });
  offlineBff.on("GET", "/api/v1/sessions/chat-a/transcript", () => ({
    body: JSON.stringify({ complete: true, messages: savedMessages, sessionId: "chat-a" }),
    contentType: "application/json",
  }));
  offlineBff.on("GET", "/api/v1/sessions/chat-a/activity", () => ({
    body: "",
    contentType: "text/event-stream",
  }));

  await page.clock.install({ time: new Date("2026-09-24T12:00:00Z") });
  await page.goto("/workspace/chat?sessionId=chat-a");
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Scheduled chat" })).toBeVisible();
  await expect
    .poll(() => offlineBff.requestsFor("GET", "/api/v1/sessions/chat-a/transcript").length)
    .toBe(1);

  updatedAt = "2026-09-24T12:00:20Z";
  savedMessages = [
    {
      delivery: { fireId: "fire-1", kind: "completed", scheduleName: "Daily report" },
      images: [],
      role: "user",
      text: "Recorded output",
      toolCalls: [],
    },
    { images: [], role: "assistant", text: "Scheduled answer", toolCalls: [] },
  ];
  await page.clock.fastForward(20_000);
  await expect(page.getByText("Recorded output")).toBeVisible();
  await expect(page.getByText("Scheduled answer")).toBeVisible();
  await expect(page.locator("[data-delivery-note]")).toHaveCount(1);
  await expect
    .poll(() => offlineBff.requestsFor("GET", "/api/v1/sessions/chat-a/transcript").length)
    .toBe(2);
});

test("a fresh legacy inventory title updates the chat header and folder row", async ({
  offlineBff,
  page,
}) => {
  let title = "Legacy title";
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
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
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", {
    models: [],
    modelsSupported: false,
  });
  offlineBff.on("GET", "/api/v1/sessions", () => ({
    body: JSON.stringify({
      complete: true,
      items: [
        {
          capabilities: {
            copyId: true,
            copyIdReason: "",
            delete: false,
            deleteReason: "",
            fork: true,
            forkReason: "",
            inspect: true,
            inspectReason: "",
            publicChat: true,
            publicChatReason: "",
            rename: false,
            renameReason: "",
            viewTranscript: true,
            viewTranscriptReason: "",
          },
          createdAt: "2026-09-24T12:00:00Z",
          debugTargetSessionId: "",
          id: "chat-a",
          kind: "main",
          modelId: "offline",
          state: "idle",
          title,
          titleProvenance: "",
          titleRevision: "0",
          turns: 0,
          updatedAt: "2026-09-24T12:00:00Z",
        },
      ],
    }),
    contentType: "application/json",
  }));
  offlineBff.json("GET", "/api/v1/sessions/chat-a", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "chat-a",
    kind: "main",
    mode: "default",
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  });
  offlineBff.json("GET", "/api/v1/sessions/chat-a/transcript", {
    complete: true,
    messages: [],
    sessionId: "chat-a",
  });
  offlineBff.on("GET", "/api/v1/sessions/chat-a/activity", () => ({
    body: "",
    contentType: "text/event-stream",
  }));
  await page.addInitScript(() => {
    localStorage.setItem("studio.account", "opaque-a");
    localStorage.setItem(
      "studio.chat.folders",
      JSON.stringify({
        assignments: { "chat-a": "project-folder" },
        folders: [{ id: "project-folder", name: "Project" }],
      }),
    );
  });
  await page.clock.install({ time: new Date("2026-09-24T12:00:00Z") });
  await page.goto("/workspace/chat?sessionId=chat-a");
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Legacy title" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Project" })).toBeVisible();
  title = "Renamed legacy title";
  await page.clock.fastForward(20_000);
  await expect(page.getByRole("heading", { name: "Renamed legacy title" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Renamed legacy title idle" })).toBeVisible();
});
