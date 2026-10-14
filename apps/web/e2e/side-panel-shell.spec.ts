// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "./fixtures";

test("canvas panel stays in the viewport at mobile and desktop widths", async ({
  offlineBff,
  page,
}, testInfo) => {
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
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });

  await page.goto("/workspace/chat");
  const trigger = page.getByRole("button", { name: "Open local canvas" });
  await expect(trigger).toBeVisible();
  await trigger.click();
  const panel = page.getByRole("complementary", { name: "Local canvas" });
  await expect(panel).toBeVisible();
  await expect(page.getByRole("button", { name: "Close panel" })).toBeFocused();

  for (const width of [320, 500, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    const bounds = await panel.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((bounds?.x ?? 0) + (bounds?.width ?? 0)).toBeLessThanOrEqual(width + 1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
    if (width < 760) expect(bounds?.width).toBeGreaterThanOrEqual(width - 2);
  }

  const resize = page.getByRole("button", { name: "Resize panel" });
  await resize.focus();
  await resize.press("ArrowLeft");
  await expect(panel).toHaveCSS("width", "268px");
  await resize.press("ArrowRight");
  await expect(panel).toHaveCSS("width", "256px");
  const handle = await resize.boundingBox();
  expect(handle).not.toBeNull();
  const handleX = (handle?.x ?? 0) + (handle?.width ?? 0) / 2;
  const handleY = (handle?.y ?? 0) + (handle?.height ?? 0) / 2;
  await page.mouse.move(handleX, handleY);
  await page.mouse.down();
  await page.mouse.move(handleX - 40, handleY);
  await page.mouse.up();
  await expect(panel).toHaveCSS("width", "296px");

  await page.setViewportSize({ width: 320, height: 800 });
  const backdrop = page.getByRole("button", { name: "Dismiss panel backdrop" });
  if (testInfo.project.name === "chromium-mobile") {
    await backdrop.tap({ position: { x: 10, y: 10 } });
  } else {
    await backdrop.click({ position: { x: 10, y: 10 } });
  }
  await expect(panel).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await trigger.click();
  await expect(panel).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
});

test("the side thread's model pill stays inside the panel", async ({ offlineBff, page }) => {
  const capabilities = {
    copyId: true,
    copyIdReason: "",
    delete: true,
    deleteReason: "",
    fork: true,
    forkReason: "",
    inspect: true,
    inspectReason: "",
    publicChat: true,
    publicChatReason: "",
    rename: true,
    renameReason: "",
    viewTranscript: true,
    viewTranscriptReason: "",
  };
  const row = (id: string, title: string, kind = "main") => ({
    capabilities,
    createdAt: "2026-09-25T09:00:00Z",
    debugTargetSessionId: "",
    id,
    kind,
    modelId: "claude-sonnet-4-5",
    state: "idle",
    title,
    titleProvenance: "",
    titleRevision: "0",
    turns: 1,
    updatedAt: "2026-09-25T09:00:00Z",
  });
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", {
    buildId: "",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "",
      routingConfiguration: false,
      routingConfigurationReason: "",
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
    serverImplementation: "",
  });
  offlineBff.json("GET", "/api/v1/sessions", {
    complete: true,
    items: [row("chat", "Fix the flaky scheduler test")],
  });
  for (const id of ["chat", "thread"]) {
    offlineBff.json("GET", `/api/v1/sessions/${id}`, {
      capabilities: { image: false, manualCompaction: false, modelSelection: true },
      id,
      kind: id === "thread" ? "side" : "main",
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
        inputTokens: "0",
        outputTokens: "0",
        reasoningTokens: "0",
      },
    });
    offlineBff.json("GET", `/api/v1/sessions/${id}/transcript`, {
      complete: true,
      messages:
        id === "chat"
          ? [
              { images: [], role: "user", text: "Why does the test flake?", toolCalls: [] },
              {
                images: [],
                role: "assistant",
                text: "It sleeps instead of waiting.",
                toolCalls: [],
              },
            ]
          : [],
      sessionId: id,
    });
    offlineBff.on("GET", `/api/v1/sessions/${id}/activity`, () => ({
      body: "",
      contentType: "text/event-stream",
    }));
  }
  offlineBff.json("POST", "/api/v1/sessions/chat/fork", { id: "thread" }, 201);
  offlineBff.json("PATCH", "/api/v1/sessions/thread", {
    ...row("thread", "Thread: Fix the flaky scheduler test", "side"),
  });

  await page.setViewportSize({ height: 800, width: 1280 });
  await page.goto("/workspace/chat?sessionId=chat");
  const answer = page
    .getByRole("region", { name: "Conversation transcript" })
    .getByRole("article")
    .last();
  await expect(answer).toContainText("It sleeps instead of waiting.");
  await answer.hover();
  await answer.getByRole("button", { name: "Reply in side thread" }).click();
  const thread = page.getByRole("complementary", { name: "Thread" });
  await expect(thread).toBeVisible();
  const pill = thread.getByRole("button", { name: /^Model/ });
  await expect(pill).toBeVisible();
  // The full choice stays available on hover, whatever the pill can show.
  await expect(pill).toHaveAttribute("title", "Model: Claude Sonnet 4.5 · Medium");
  for (const width of [1280, 1024, 760]) {
    await page.setViewportSize({ height: 800, width });
    const panel = await thread.boundingBox();
    const box = await pill.boundingBox();
    expect(panel).not.toBeNull();
    expect(box).not.toBeNull();
    expect(
      (box?.x ?? 0) + (box?.width ?? 0),
      `the pill ends inside the panel at ${width}px`,
    ).toBeLessThanOrEqual((panel?.x ?? 0) + (panel?.width ?? 0) - 8);
    expect(
      await pill.evaluate((element) => element.scrollWidth <= element.clientWidth),
      `the pill's own content isn't cut at ${width}px`,
    ).toBe(true);
  }
});
