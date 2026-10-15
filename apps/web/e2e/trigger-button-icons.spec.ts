// SPDX-License-Identifier: Apache-2.0

import type { Locator, TestInfo } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

// A Radix trigger with asChild merges its own props, data-slot included, onto
// the Button it wraps. These journeys measure the drawn glyph of a bare-icon
// Button inside each kind of trigger Studio uses: 20px on phones, as the phone
// rule in styles.css draws every Button glyph, and 16px on desktop. Without
// that rule a bare glyph is size-4, 1rem, which is 18px at the phone root size.

const PHONE = 20;
const DESKTOP = 16;

async function glyphSize(button: Locator) {
  await expect(button).toBeVisible();
  return button.locator("svg.lucide").evaluate((svg) => {
    const box = svg.getBoundingClientRect();
    return { height: Math.round(box.height), width: Math.round(box.width) };
  });
}

function expected(testInfo: TestInfo) {
  const size = testInfo.project.name === "chromium-mobile" ? PHONE : DESKTOP;
  return { height: size, width: size };
}

function signedIn(offlineBff: OfflineBff, capabilities: Record<string, unknown>) {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { posture: "managed", ...capabilities },
    connection: "online",
  });
}

test("a Button inside a TooltipTrigger glyph size at phone and desktop widths", async ({
  offlineBff,
  page,
}, testInfo) => {
  signedIn(offlineBff, { learningProposals: true });
  offlineBff.json("GET", "/api/v1/learning-proposals", {
    complete: true,
    items: [],
    nextCursor: "",
    reason: "",
    supported: true,
  });
  await page.goto("/workspace/settings/learning");

  const refresh = page.getByRole("button", { name: "Refresh suggestions" });
  expect(await glyphSize(refresh)).toEqual(expected(testInfo));
});

test("a Button inside a DropdownMenuTrigger glyph size at phone and desktop widths", async ({
  offlineBff,
  page,
}, testInfo) => {
  signedIn(offlineBff, { scheduling: true });
  offlineBff.json("GET", "/api/v1/schedules", {
    items: [
      {
        enabled: true,
        fireCount: 0,
        lastFireAt: "",
        lastFireSessionId: "",
        maxFires: 0,
        mode: "plan",
        modelId: "",
        mutating: false,
        name: "daily-report",
        nextFireAt: "2026-10-08T08:00:00Z",
        oneShotMaxRetries: 0,
        oneShotRetry: false,
        owner: "operator",
        profile: "all",
        prompt: "Summarize project activity",
        providerId: "",
        status: "scheduled",
        trigger: { expression: "0 8 * * *", kind: "cron", timezone: "Europe/Rome" },
      },
    ],
    reason: "",
    supported: true,
  });
  offlineBff.json("GET", "/api/v1/schedules/daily-report/fires", { items: [] });
  await page.goto("/workspace/schedules/daily-report");

  const actions = page.getByRole("button", { name: "Actions for daily-report" });
  expect(await glyphSize(actions)).toEqual(expected(testInfo));
});

test("the chat header's options Button glyph size at phone and desktop widths", async ({
  offlineBff,
  page,
}, testInfo) => {
  signedIn(offlineBff, { image: false });
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
        id: "s1",
        modelId: "offline",
        state: "idle",
        title: "An idle chat",
        turns: 1,
        updatedAt: "2026-09-24T00:00:00Z",
      },
    ],
  });
  offlineBff.json("GET", "/api/v1/sessions/s1", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "s1",
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
  offlineBff.json("GET", "/api/v1/sessions/s1/transcript", {
    complete: true,
    messages: [],
    sessionId: "s1",
  });
  // An idle chat's activity stream has nothing to replay.
  offlineBff.on("GET", "/api/v1/sessions/s1/activity", () => ({
    body: "",
    contentType: "text/event-stream",
  }));
  await page.goto("/workspace/chat?sessionId=s1");

  const options = page.getByRole("button", { name: "Chat options" });
  expect(await glyphSize(options)).toEqual(expected(testInfo));
});
