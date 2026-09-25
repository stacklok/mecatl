// SPDX-License-Identifier: Apache-2.0

import { mkdir } from "node:fs/promises";
import { resolve } from "node:path";
import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { createApp } from "../../server/src/app";
import { expect, type OfflineBff, test } from "./fixtures";

const proofDir = resolve(import.meta.dirname, "../../../.scratch/studio-session-activity-browser");

function event(kind: string, seq: number, payload: unknown, text = ""): RunStreamEvent {
  return {
    event: { kind, payload, runId: "run-a", seq: String(seq), text, turn: 1, unknown: false },
    type: "run.event",
  };
}

function longInterleavedRun(): RunStreamEvent[] {
  const frames: RunStreamEvent[] = [{ runId: "run-a", sessionId: "s1", type: "run.started" }];
  let seq = 0;
  const send = (kind: string, payload: unknown, text = "") => {
    seq += 1;
    frames.push(event(kind, seq, payload, text));
  };
  send("user_prompt", { text: "Inspect delegated work" });
  send("tool.call", { args: "{}", id: "call-s", name: "Subagent" });
  send("tool.call", { args: "{}", id: "call-p", name: "Parallel" });
  send("tool.call", { args: "{}", id: "call-t", name: "Team" });
  send("subagent.start", { childId: "child-s", goal: "Inspect logs", parentCallId: "call-s" });
  send("parallel.start", { branchCount: 2, join: "first", parentCallId: "call-p" });
  send("parallel.branch", {
    branchIndex: 0,
    branchLabel: "Fast path",
    childId: "child-p0",
    goal: "Check",
    kind: "branch_start",
    parentCallId: "call-p",
  });
  send("parallel.branch", {
    branchIndex: 1,
    branchLabel: "Fallback",
    childId: "child-p1",
    goal: "Compare",
    kind: "branch_start",
    parentCallId: "call-p",
  });
  send("team.start", {
    parentCallId: "call-t",
    roster: [
      { lead: true, name: "lead", role: "Coordinator" },
      { lead: false, name: "worker", role: "Reviewer" },
    ],
    teamId: "team-a",
  });
  for (let index = 0; index < 18; index += 1) {
    const detail = `step-${index} ${"🧪".repeat(240)}`;
    send("subagent.tool", {
      childId: "child-s",
      detail,
      innerKind: "tool.result",
      parentCallId: "call-s",
      toolCount: index + 1,
      toolName: "Read",
    });
    send("parallel.branch", {
      branchIndex: 0,
      detail,
      innerKind: "tool.result",
      kind: "branch_tool",
      parentCallId: "call-p",
      toolCount: index + 1,
      toolName: "Read",
    });
    send("team.member", {
      detail,
      innerKind: "tool.result",
      member: "lead",
      parentCallId: "call-t",
      teamId: "team-a",
      toolName: "Read",
    });
  }
  send("team.tasks", {
    parentCallId: "call-t",
    tasks: [
      { assignee: "worker", deps: [], description: "Review output", id: "task-1", state: "done" },
    ],
    teamId: "team-a",
  });
  send("team.findings", {
    findings: [{ body: "Checks complete", member: "lead" }],
    parentCallId: "call-t",
    teamId: "team-a",
  });
  send("team.member", {
    innerKind: "result",
    member: "lead",
    parentCallId: "call-t",
    teamId: "team-a",
  });
  send("parallel.branch", {
    branchIndex: 1,
    failed: true,
    kind: "branch_end",
    parentCallId: "call-p",
    stop: "error",
  });
  send("parallel.branch", {
    branchIndex: 0,
    failed: false,
    kind: "branch_end",
    parentCallId: "call-p",
    stop: "end_turn",
  });
  send("parallel.end", {
    branchCount: 2,
    join: "first",
    parentCallId: "call-p",
    stop: "end_turn",
    winner: 0,
  });
  send("subagent.end", {
    childId: "child-s",
    parentCallId: "call-s",
    stop: "end_turn",
    toolCount: 18,
  });
  send("team.end", {
    dispositions: [
      { errorRounds: 0, name: "lead", reason: 0, stopped: false },
      { errorRounds: 1, name: "worker", reason: 3, stopped: true },
    ],
    findings: [{ body: "Checks complete", member: "lead" }],
    parentCallId: "call-t",
    rounds: 2,
    stop: "end_turn",
    tasks: [
      { assignee: "worker", deps: [], description: "Review output", id: "task-1", state: "done" },
    ],
    teamId: "team-a",
  });
  send("result", { stop: "end_turn", text: "Delegated work recorded" });
  return frames;
}

function arrangeChat(offlineBff: OfflineBff) {
  let running = true;
  offlineBff.json("GET", "/api/v1/auth/session", {
    mode: "oidc",
    status: "authenticated",
    account: "offline",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    connection: "online",
    capabilities: { image: false, posture: "managed" },
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { models: [], modelsSupported: true });
  offlineBff.on("GET", "/api/v1/sessions", () => ({
    body: JSON.stringify({
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
          kind: "main",
          modelId: "offline",
          state: running ? "running" : "idle",
          title: "Delegation journey",
          turns: 1,
          updatedAt: "2026-09-24T00:00:00Z",
        },
      ],
    }),
    contentType: "application/json",
  }));
  offlineBff.json("GET", "/api/v1/sessions/s1", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "s1",
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
  offlineBff.json("GET", "/api/v1/sessions/s1/transcript", {
    complete: true,
    sessionId: "s1",
    messages: [
      { images: [], role: "user", text: "Inspect delegated work", toolCalls: [] },
      {
        images: [],
        role: "assistant",
        text: "Delegated work recorded",
        toolCalls: [
          { args: "{}", id: "call-s", name: "Subagent" },
          { args: "{}", id: "call-p", name: "Parallel" },
          { args: "{}", id: "call-t", name: "Team" },
        ],
      },
    ],
  });
  offlineBff.on("GET", "/api/v1/sessions/s1/activity", () => {
    running = false;
    return {
      body: longInterleavedRun()
        .map((frame) => `data: ${JSON.stringify(frame)}\n\n`)
        .join(""),
      contentType: "text/event-stream",
    };
  });
}

test("session activity remains reachable at mobile and desktop widths", async ({
  offlineBff,
  page,
}, testInfo) => {
  arrangeChat(offlineBff);
  const servedCsp = process.env.STUDIO_BROWSER_SERVE_BUILT === "1";
  let policy: string | null = null;
  if (servedCsp) {
    policy = (await createApp().request("/api/health")).headers.get("content-security-policy");
    expect(policy).toContain("script-src 'self'");
    expect(policy).not.toContain("unsafe-eval");
    if (!policy) throw new Error("Studio did not serve a content security policy");
    const servedPolicy = policy;
    await page.route("**/workspace/chat?sessionId=s1", async (route) => {
      const response = await route.fetch();
      await route.fulfill({
        response,
        headers: { ...response.headers(), "content-security-policy": servedPolicy },
      });
    });
  }
  const documentResponse = await page.goto("/workspace/chat?sessionId=s1", {
    waitUntil: "domcontentloaded",
  });
  if (servedCsp) expect(documentResponse?.headers()["content-security-policy"]).toBe(policy);
  const card = page.locator('button[data-delegation-focus*="subagent"]');
  await expect(card).toBeVisible();
  await mkdir(proofDir, { recursive: true });
  for (const width of [320, 500, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    await card.focus();
    await page.keyboard.press("Enter");
    const panel = page.getByRole("complementary", { name: "Session activity" });
    await expect(panel).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Subagent child-s" })).toBeFocused();
    await expect(panel.getByText("Older entries omitted: 6")).toBeVisible();
    await expect(panel.locator('[aria-label="Recent trace"] li')).toHaveCount(12);
    await expect(panel.getByText("step-17", { exact: false })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(1);
    expect(
      await panel.evaluate((element) => element.scrollWidth - element.clientWidth),
    ).toBeLessThanOrEqual(1);
    await page.screenshot({
      path: resolve(proofDir, `${servedCsp ? "csp" : "dev"}-${testInfo.project.name}-${width}.png`),
    });
    await panel.getByRole("tab", { name: /Parallel/ }).focus();
    await page.keyboard.press("Enter");
    const groupRow = panel.getByRole("button", { name: /Parallel group call-p/ });
    await groupRow.focus();
    await expect(groupRow).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(panel.getByRole("heading", { name: "Parallel group call-p" })).toBeFocused();
    const branchRow = panel.getByRole("button", { name: /Branch 1.*Winner/ });
    await branchRow.focus();
    await expect(branchRow).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(panel.getByRole("heading", { name: "Branch 1" })).toBeFocused();
    await expect(panel.getByText("Older entries omitted: 6")).toBeVisible();
    await expect(panel.locator('[aria-label="Recent trace"] li')).toHaveCount(12);
    await panel.getByRole("tab", { name: /Teams/ }).focus();
    await page.keyboard.press("Enter");
    const teamRow = panel.getByRole("button", { name: /Team team-a/ });
    await teamRow.focus();
    await expect(teamRow).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(panel.getByRole("heading", { name: "Team team-a" })).toBeFocused();
    const memberRow = panel.getByRole("button", { name: /lead.*Coordinator/ });
    await memberRow.focus();
    await expect(memberRow).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(panel.getByRole("heading", { name: "Member lead" })).toBeFocused();
    await expect(panel.getByText("Checks complete")).toBeVisible();
    await expect(panel.getByText("Older entries omitted: 6")).toBeVisible();
    await expect(panel.locator('[aria-label="Recent trace"] li')).toHaveCount(12);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(1);
    await page.screenshot({
      path: resolve(
        proofDir,
        `${servedCsp ? "csp" : "dev"}-${testInfo.project.name}-${width}-team.png`,
      ),
    });
    await page.keyboard.press("Escape");
    await expect(panel).toHaveCount(0);
    await expect(card).toBeFocused();
  }
  const sessionControl = page.getByRole("button", { name: "Open session activity" });
  await sessionControl.focus();
  await page.keyboard.press("Enter");
  const panel = page.getByRole("complementary", { name: "Session activity" });
  await expect(panel.getByRole("heading", { name: "Session activity" })).toBeFocused();
  await panel.getByRole("button", { name: "Close panel" }).click();
  await expect(sessionControl).toBeFocused();
});
