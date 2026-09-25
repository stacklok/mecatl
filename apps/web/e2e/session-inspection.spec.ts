// SPDX-License-Identifier: Apache-2.0

import { expect, type OfflineBff, test } from "./fixtures";

const inspectOnly = {
  capabilities: {
    copyId: true,
    copyIdReason: "",
    delete: false,
    deleteReason: "inspect_only_kind",
    fork: false,
    forkReason: "inspect_only_kind",
    inspect: true,
    inspectReason: "",
    publicChat: false,
    publicChatReason: "inspect_only_kind",
    rename: false,
    renameReason: "inspect_only_kind",
    viewTranscript: false,
    viewTranscriptReason: "Transcript unavailable for this session.",
  },
  createdAt: "2026-09-25T12:00:00Z",
  debugTargetSessionId: "",
  id: "inspect-only",
  kind: "subagent",
  modelId: "offline",
  state: "idle",
  title: "Recorded worker",
  titleProvenance: "",
  titleRevision: "0",
  turns: 2,
  updatedAt: "2026-09-25T12:00:00Z",
};

function commonRoutes(offlineBff: OfflineBff) {
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
    capabilities: {
      image: false,
      posture: "managed",
      sessionDebug: false,
      soul: false,
      worktrees: false,
    },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { models: [], modelsSupported: false });
  offlineBff.json("GET", "/api/v1/sessions/inspect-only", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "inspect-only",
    kind: "subagent",
    mode: "default",
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "2",
      outputTokens: "3",
      reasoningTokens: "0",
    },
  });
}

test("inspect-only direct link stays read-only and explains disabled capabilities", async ({
  offlineBff,
  page,
}) => {
  commonRoutes(offlineBff);
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [inspectOnly] });

  await page.goto("/workspace/chat?sessionId=inspect-only");
  const details = page.getByRole("dialog", { name: "Session details" });
  await expect(details).toBeVisible();
  await expect(page.getByText("Inspect-only sessions")).toBeVisible();
  await expect(details.getByText("Transcript unavailable for this session.")).toBeVisible();
  await expect(details.getByRole("button", { name: "View transcript" })).toBeDisabled();
  await expect(details.getByRole("button", { name: "Inspect soul" })).toBeDisabled();
  await expect(details.getByRole("button", { name: "Choose worktree" })).toBeDisabled();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send message" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Stop" })).toHaveCount(0);

  await page.keyboard.press("Escape");
  await expect(details).toHaveCount(0);
  await expect(page.getByText("Inspect-only session · Read-only")).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveCount(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/sessions/inspect-only/transcript")).toHaveLength(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/soul")).toHaveLength(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/sessions/inspect-only/worktrees")).toHaveLength(0);
});

test("an unclassified direct link withholds chat controls", async ({ offlineBff, page }) => {
  commonRoutes(offlineBff);
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });

  await page.goto("/workspace/chat?sessionId=inspect-only");
  await expect(page.getByText("This session is unavailable as a chat.")).toBeVisible();
  await expect(page.getByRole("dialog", { name: "Session details" })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send message" })).toHaveCount(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/sessions/inspect-only/transcript")).toHaveLength(0);
});
