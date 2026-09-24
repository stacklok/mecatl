// SPDX-License-Identifier: Apache-2.0

import { expect, type OfflineBff, test } from "./fixtures";

type OfflineState = {
  account: string;
  signedIn: boolean;
  sessionFails: boolean;
  expireNextWrite: boolean;
  runWrites: number;
  popupCompletes: boolean;
};

function setup(offlineBff: OfflineBff, initial: Partial<OfflineState> = {}): OfflineState {
  const state: OfflineState = {
    account: "opaque-a",
    signedIn: true,
    sessionFails: false,
    expireNextWrite: false,
    runWrites: 0,
    popupCompletes: true,
    ...initial,
  };
  offlineBff.on("GET", "/api/v1/auth/session", () => {
    if (state.sessionFails) return { status: 503, body: "verification unavailable" };
    return {
      body: JSON.stringify(
        state.signedIn
          ? { mode: "oidc", status: "authenticated", account: state.account }
          : { mode: "oidc", status: "anonymous" },
      ),
      contentType: "application/json",
    };
  });
  offlineBff.on("GET", "/api/v1/status", () => ({
    body: JSON.stringify({ connection: "reachable", signInRequired: !state.signedIn }),
    contentType: "application/json",
  }));
  offlineBff.json("GET", "/api/v1/runtime", {
    apiMajor: 1,
    capabilities: {
      agents: true,
      audio: false,
      bash: true,
      debugMcp: false,
      image: false,
      learnedSkills: false,
      learningProposals: false,
      manualCompaction: false,
      mcp: false,
      mcpConnectorStatus: false,
      memory: false,
      modelSelection: false,
      posture: "managed",
      reflection: false,
      scheduling: false,
      sessionDebug: false,
      skills: false,
      slashCommands: false,
      soul: false,
      steer: false,
      storageCleanup: false,
      storageHealth: false,
      storageMigration: false,
      teams: false,
      userModel: false,
      workspaceEnrollment: false,
      worktrees: false,
    },
    connection: "online",
    features: [],
    mock: false,
    source: "external",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", {
    buildId: "offline",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "",
      routingConfiguration: false,
      routingConfigurationReason: "",
    },
    models: [],
    modelsReason: "",
    modelsSupported: false,
    providerEndpoint: "",
    providers: [],
    serverImplementation: "offline",
  });
  offlineBff.on("GET", "/api/v1/sessions", () => ({
    body: JSON.stringify({
      complete: true,
      items: [
        {
          capabilities: { delete: false, deleteReason: "", rename: false, renameReason: "" },
          createdAt: "2026-09-24T00:00:00Z",
          debugTargetSessionId: "",
          id: "s1",
          modelId: "offline",
          state: "idle",
          title: state.account === "opaque-a" ? "Alice chat" : "Bob chat",
          turns: 0,
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
  offlineBff.on("POST", "/api/v1/sessions/s1/runs", () => {
    state.runWrites += 1;
    if (state.expireNextWrite) {
      state.expireNextWrite = false;
      state.signedIn = false;
      return {
        body: JSON.stringify({ code: "session_expired", status: 401 }),
        contentType: "application/problem+json",
        status: 401,
      };
    }
    return {
      body: `data: ${JSON.stringify({ type: "run.started", runId: "r1", sessionId: "s1" })}\n\n`,
      contentType: "text/event-stream",
    };
  });
  offlineBff.on("GET", "/api/v1/auth/login", (request) => {
    const url = new URL(request.url());
    expect(url.searchParams.get("flow")).toBe("popup");
    expect(url.searchParams.get("return_to")).toBe("/workspace/chat?sessionId=s1#draft");
    if (state.popupCompletes) state.signedIn = true;
    return {
      body: state.popupCompletes
        ? '<!doctype html><html data-result="success"><title>Signed in</title><script src="/api/v1/auth/callback.js" defer></script></html>'
        : "<!doctype html><title>Sign-in pending</title><p>Return to Studio after sign-in.</p>",
      contentType: "text/html",
    };
  });
  offlineBff.on("GET", "/api/v1/auth/callback.js", () => ({
    body: 'window.opener?.postMessage({type:"studio.auth.result",result:document.documentElement.dataset.result},window.location.origin);window.close();',
    contentType: "text/javascript",
  }));
  return state;
}

const draftRoute = "/workspace/chat?sessionId=s1#draft";

async function expireWrite(page: import("@playwright/test").Page, state: OfflineState) {
  state.expireNextWrite = true;
  await page.getByRole("textbox", { name: "Message Mecatl" }).fill("Keep this unsent draft");
  await page.getByRole("button", { name: "Send message" }).click();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
}

test("popup sign-in preserves route and draft", async ({ offlineBff, page }) => {
  const state = setup(offlineBff);
  await page.goto(draftRoute);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  await expireWrite(page, state);
  const popupEvent = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await popupEvent;
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );
  expect(new URL(page.url()).pathname + new URL(page.url()).search + new URL(page.url()).hash).toBe(
    draftRoute,
  );
  expect(state.runWrites).toBe(1);
});

test("blocked and closed popups offer a recoverable sign-in", async ({ offlineBff, page }) => {
  const state = setup(offlineBff);
  await page.goto(draftRoute);
  await expireWrite(page, state);
  await page.evaluate(() => {
    (window as Window & { savedOpen?: typeof window.open }).savedOpen = window.open;
    window.open = () => null;
  });
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  const newTab = page.getByRole("link", { name: "Open sign-in in a new tab" }).first();
  await expect(newTab).toBeVisible();
  await expect(newTab).toHaveAttribute("rel", /noopener/);
  await expect(page.getByRole("button", { name: "Retry sign-in" }).first()).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );

  state.popupCompletes = false;
  await page.evaluate(() => {
    const saved = (window as Window & { savedOpen?: typeof window.open }).savedOpen;
    if (saved) window.open = saved;
  });
  const popupEvent = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Retry sign-in" }).first().click();
  const popup = await popupEvent;
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(page.getByRole("link", { name: "Open sign-in in a new tab" }).first()).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );
  await popup.close();
  await expect(newTab).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );

  state.popupCompletes = true;
  const newTabEvent = page.waitForEvent("popup");
  await newTab.click();
  const openerless = await newTabEvent;
  await expect.poll(() => state.signedIn).toBe(true);
  if (!openerless.isClosed()) await openerless.close();
  await page.bringToFront();
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );
});

test("expired session requires explicit retry for a write", async ({ offlineBff, page }) => {
  const state = setup(offlineBff);
  await page.goto(draftRoute);
  await expireWrite(page, state);
  const popupEvent = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await popupEvent;
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toHaveCount(0);
  expect(state.runWrites).toBe(1);
  await page.getByRole("button", { name: "Send message" }).click();
  await expect.poll(() => state.runWrites).toBe(2);
});

test("different account clears the mounted draft before showing its data", async ({
  offlineBff,
  page,
}) => {
  const state = setup(offlineBff);
  await page.goto(draftRoute);
  await expect(page.getByRole("heading", { name: "Alice chat" })).toBeVisible();
  await page.evaluate(() => localStorage.setItem("studio.chat.queue", "Alice's queued draft"));
  await expireWrite(page, state);
  state.account = "opaque-b";
  const popupEvent = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await popupEvent;
  await expect(page.getByRole("heading", { name: "Bob chat" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue("");
  const scoped = await page.evaluate(() => ({
    account: localStorage.getItem("studio.account"),
    queue: localStorage.getItem("studio.chat.queue"),
  }));
  expect(scoped).toEqual({ account: "opaque-b", queue: null });
});

test("anonymous shell shows outage before sign-in", async ({ offlineBff, page }) => {
  setup(offlineBff, { signedIn: false });
  offlineBff.json("GET", "/api/v1/status", { connection: "unavailable", signInRequired: true });
  await page.goto(draftRoute);
  await expect(
    page.getByRole("status").filter({ hasText: "Mecatl instance is unavailable" }),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "Mecatl Studio" })).toBeVisible();
  expect(offlineBff.requestsFor("GET", "/api/v1/runtime")).toHaveLength(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/storage/health")).toHaveLength(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/settings/runtime")).toHaveLength(0);
});

test("session check outage keeps the shell and route", async ({ offlineBff, page }) => {
  const state = setup(offlineBff, { sessionFails: true });
  await page.clock.install();
  await page.goto(draftRoute);
  await expect(page.getByRole("heading", { name: "Mecatl Studio" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry session check" })).toBeVisible();
  expect(offlineBff.requestsFor("GET", "/api/v1/runtime")).toHaveLength(0);
  state.sessionFails = false;
  await page.getByRole("button", { name: "Retry session check" }).click();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  await page
    .getByRole("textbox", { name: "Message Mecatl" })
    .fill("Keep this draft during verification");
  state.sessionFails = true;
  await page.clock.runFor(60_001);
  await expect(page.getByRole("button", { name: "Retry session check" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this draft during verification",
  );
  expect(new URL(page.url()).pathname + new URL(page.url()).search + new URL(page.url()).hash).toBe(
    draftRoute,
  );
});
