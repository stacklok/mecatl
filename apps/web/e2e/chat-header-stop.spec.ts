// SPDX-License-Identifier: Apache-2.0

import { createServer, type Server } from "node:http";
import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { expect, type OfflineBff, test } from "./fixtures";

const sessionId = "s1";
const prefix = `/api/v1/sessions/${sessionId}`;
const runningFrames: RunStreamEvent[] = [{ runId: "run-a", sessionId, type: "run.started" }];
// The run parks on a connector that needs the user to authorize it.
const authorizationFrames = [
  ...runningFrames,
  {
    event: {
      kind: "authorization.required",
      payload: {
        authorizationId: "auth-1",
        callId: "call-1",
        displayName: "Calendar connector",
        status: "pending",
      },
      runId: "run-a",
      seq: "1",
      text: "",
      turn: 1,
      unknown: false,
    },
    type: "run.event",
  },
] as RunStreamEvent[];
let frames: RunStreamEvent[] = runningFrames;

let activityServer: Server | undefined;
let activityPort = 0;

test.beforeAll(async () => {
  // Writes the run frames and keeps the stream open, so the chat stays on a live run.
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

test.afterEach(() => {
  frames = runningFrames;
});

test.afterAll(() => {
  activityServer?.closeAllConnections();
  activityServer?.close();
});

function runningSession(offlineBff: OfflineBff) {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "stop-proof",
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
        title: "A running chat with a long title that should truncate first",
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

test("the running chat keeps Stop inside the header at phone widths", async ({
  offlineBff,
  page,
}) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 844, width: 390 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  const header = page.locator("header").filter({ has: page.getByRole("button", { name: "Stop" }) });
  const stop = header.getByRole("button", { exact: true, name: "Stop" });
  await expect(stop).toBeVisible();
  // The run status pill is present, so the row carries its widest running content.
  await expect(header.getByText("Reconnected to run")).toBeAttached();

  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ height: 844, width });
    await expect(stop).toBeVisible();
    const box = await stop.boundingBox();
    expect(box, `Stop has a box at ${width}px`).not.toBeNull();
    if (!box) continue;
    expect(box.x, `Stop starts inside the viewport at ${width}px`).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width, `Stop ends inside the viewport at ${width}px`).toBeLessThanOrEqual(
      width,
    );
    expect(box.width, `Stop is at least 44px wide at ${width}px`).toBeGreaterThanOrEqual(44);
    expect(box.height, `Stop is at least 44px tall at ${width}px`).toBeGreaterThanOrEqual(44);
    // Nothing else in the header covers the control's centre.
    const hit = await page.evaluate(
      ({ x, y }) => document.elementFromPoint(x, y)?.closest("button")?.textContent?.trim(),
      { x: box.x + box.width / 2, y: box.y + box.height / 2 },
    );
    expect(hit, `Stop is the hit target at ${width}px`).toBe("Stop");
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
  }
});

test("the running chat keeps Activity and Canvas reachable on a phone", async ({
  offlineBff,
  page,
}) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 844, width: 390 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  await expect(page.getByRole("button", { name: "Stop" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open session activity" })).toBeVisible();

  // On a phone, Canvas moves from the header into the chat options menu.
  await expect(page.getByRole("button", { name: "Open local canvas" })).toHaveCount(0);
  await page.getByRole("button", { name: "Chat options" }).click();
  await page.getByRole("menuitem", { name: "Open local canvas" }).click();
  const canvas = page.getByRole("complementary", { name: "Local canvas" });
  await expect(canvas).toBeVisible();
  await expect(page.getByRole("button", { name: "Close panel" })).toBeFocused();
  await page.getByRole("button", { name: "Close panel" }).click();
  await expect(canvas).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Chat options" })).toBeFocused();
});

test("the running chat header keeps its desktop layout", async ({ offlineBff, page }) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 800, width: 1280 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const header = page.locator("header").filter({ has: page.getByRole("button", { name: "Stop" }) });
  // Desktop shows the labelled Stop, the status pill and the keyboard hint, as before.
  await expect(header.getByRole("button", { exact: true, name: "Stop" })).toHaveText("Stop", {
    useInnerText: true,
  });
  await expect(header.getByText("Reconnected to run")).toBeVisible();
  await expect(header.getByText("Esc to Stop")).toBeVisible();
  await expect(header.getByRole("button", { name: "Open session activity" })).toHaveText(
    "Activity",
    { useInnerText: true },
  );
  // Canvas stays a header button, and the chat options menu does not repeat it.
  await expect(header.getByRole("button", { name: "Open local canvas" })).toHaveText("Canvas", {
    useInnerText: true,
  });
  await header.getByRole("button", { name: "Chat options" }).click();
  await expect(page.getByRole("menuitem", { name: "Inspect session" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Open local canvas" })).toHaveCount(0);
});

test("a phone keeps a distinct cue while the run waits for authorization", async ({
  offlineBff,
  page,
}) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 844, width: 390 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const header = page.locator("header").filter({ has: page.getByRole("button", { name: "Stop" }) });
  // A working run shows the spinner.
  await expect(header.locator(".animate-spin")).toBeVisible();

  frames = authorizationFrames;
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const parkedHeader = page
    .locator("header")
    .filter({ has: page.getByText("Waiting for authorization") });
  const parkedCue = parkedHeader.locator('[data-slot="run-status-cue"]');
  // The header has parked on authorization before anything is measured.
  await expect(parkedHeader).toHaveCount(1);
  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ height: 844, width });
    // Not the working spinner: a still, amber key that tells the user to act.
    await expect(parkedHeader.locator(".animate-spin"), `no spinner at ${width}px`).toHaveCount(0);
    await expect(parkedCue, `cue visible at ${width}px`).toBeVisible();
    await expect(parkedCue).toHaveAttribute("data-cue", "authorization");
    const box = await parkedCue.boundingBox();
    expect(box && box.x + box.width, `cue inside the viewport at ${width}px`).toBeLessThanOrEqual(
      width,
    );
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
  }
  // The status text stays available to screen readers.
  await expect(parkedHeader.getByText("Waiting for authorization")).toBeAttached();

  // Desktop keeps the labelled pill and shows no phone cue.
  await page.setViewportSize({ height: 800, width: 1280 });
  await expect(parkedHeader.getByText("Waiting for authorization")).toBeVisible();
  await expect(parkedCue).toBeHidden();
});

test("the running chat header fits from 500px, beside the chat list too", async ({
  offlineBff,
  page,
}) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 800, width: 1280 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const header = page.locator("header").filter({ has: page.getByRole("button", { name: "Stop" }) });
  const stop = header.getByRole("button", { exact: true, name: "Stop" });
  await expect(stop).toBeVisible();
  // From 760px the chat list sits beside the chat, so the header is narrower than the viewport;
  // the Activity and Canvas labels follow the header's own width, not the viewport's.
  for (const width of [500, 560, 639, 640, 760, 820, 900]) {
    await page.setViewportSize({ height: 800, width });
    await expect(stop).toBeVisible();
    const fit = await header.evaluate((element) => ({
      client: element.clientWidth,
      right: element.getBoundingClientRect().right,
      scroll: element.scrollWidth,
    }));
    expect(fit.scroll, `the header's content fits it at ${width}px`).toBeLessThanOrEqual(
      fit.client,
    );
    const box = await stop.boundingBox();
    expect(box, `Stop has a box at ${width}px`).not.toBeNull();
    if (!box) continue;
    expect(box.x + box.width, `Stop ends inside the header at ${width}px`).toBeLessThanOrEqual(
      fit.right,
    );
  }
});

/** One delegation event on the live run. */
function delegation(seq: number, kind: string, payload: Record<string, unknown>): RunStreamEvent {
  return {
    event: { kind, payload, runId: "run-a", seq: String(seq), text: "", turn: 1, unknown: false },
    type: "run.event",
  } as RunStreamEvent;
}

test("a non-default mode, the debug badge, and the agents chip fit the running header", async ({
  offlineBff,
  page,
}) => {
  // The widest running header: the Accept edits badge, the Debug badge, and a fleet of all
  // three families. The agents chip follows the header's own content width (52rem).
  frames = [
    ...runningFrames,
    delegation(1, "subagent.start", { childId: "c1", goal: "Read", parentCallId: "call-1" }),
    delegation(2, "parallel.start", { parentCallId: "call-2" }),
    delegation(3, "team.start", {
      parentCallId: "call-3",
      roster: [{ lead: true, name: "lead" }, { name: "worker" }],
      teamId: "team-a",
    }),
  ];
  try {
    runningSession(offlineBff);
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
          debugTargetSessionId: "target-chat",
          id: sessionId,
          kind: "debug",
          modelId: "offline",
          state: "running",
          title: "A running chat with a long title that should truncate first",
          turns: 1,
          updatedAt: "2026-09-24T00:00:00Z",
        },
      ],
    });
    offlineBff.json("GET", prefix, {
      capabilities: { image: false, manualCompaction: false, modelSelection: false },
      id: sessionId,
      mode: "acceptEdits",
      state: "running",
      usage: {
        cacheReadTokens: "0",
        cacheWriteTokens: "0",
        inputTokens: "0",
        outputTokens: "0",
        reasoningTokens: "0",
      },
    });
    await page.setViewportSize({ height: 800, width: 1280 });
    await page.goto(`/workspace/chat?sessionId=${sessionId}`);
    const header = page
      .locator("header")
      .filter({ has: page.getByRole("button", { name: "Stop" }) });
    const stop = header.getByRole("button", { exact: true, name: "Stop" });
    const chip = header.getByRole("list", { name: "Agents in this chat" });
    const badges = [
      header.locator("[title='Permission mode: Accept edits']"),
      header.getByText("Debug", { exact: true }),
    ];
    for (const badge of badges) await expect(badge).toBeVisible();
    await expect(chip.getByRole("button")).toHaveCount(3);

    const shown: number[] = [];
    const hidden: number[] = [];
    // Every 34px from the 500px pivot, plus the widths around the chip's own 52rem threshold.
    const widths = Array.from({ length: 33 }, (_, index) => 500 + index * 34);
    for (const width of [...widths, 1150, 1160, 1170, 1180]) {
      await page.setViewportSize({ height: 800, width });
      await expect(stop).toBeVisible();
      const fit = await header.evaluate((element) => {
        const style = getComputedStyle(element);
        return {
          client: element.clientWidth,
          // The container query reads the content box, inside the header's padding.
          content:
            element.clientWidth -
            Number.parseFloat(style.paddingLeft) -
            Number.parseFloat(style.paddingRight),
          right: element.getBoundingClientRect().right,
          scroll: element.scrollWidth,
        };
      });
      const at = `in a ${fit.client}px header (${width}px viewport)`;
      expect(fit.scroll, `the header's content fits it ${at}`).toBeLessThanOrEqual(fit.client);
      const box = await stop.boundingBox();
      expect(box, `Stop has a box ${at}`).not.toBeNull();
      expect((box?.x ?? 0) + (box?.width ?? 0), `Stop ends inside ${at}`).toBeLessThanOrEqual(
        fit.right,
      );
      // The badges keep their room; the title is what gives way.
      for (const badge of badges) {
        await expect(badge, `a badge stays visible ${at}`).toBeVisible();
        const badgeBox = await badge.boundingBox();
        expect(
          (badgeBox?.x ?? 0) + (badgeBox?.width ?? 0),
          `a badge ends inside ${at}`,
        ).toBeLessThanOrEqual(fit.right);
      }
      if (fit.content >= 832) {
        await expect(chip, `the agents chip shows ${at}`).toBeVisible();
        const chipBox = await chip.boundingBox();
        expect(
          (chipBox?.x ?? 0) + (chipBox?.width ?? 0),
          `the agents chip ends inside ${at}`,
        ).toBeLessThanOrEqual(fit.right);
        shown.push(fit.client);
      } else {
        await expect(chip, `the agents chip hides ${at}`).toBeHidden();
        hidden.push(fit.client);
      }
    }
    // The sweep crosses the chip's threshold inside an 832–900px header.
    expect(shown.some((width) => width >= 832 && width <= 900)).toBe(true);
    expect(hidden.some((width) => width >= 832 && width <= 900)).toBe(true);
  } finally {
    frames = runningFrames;
  }
});
