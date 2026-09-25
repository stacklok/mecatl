// SPDX-License-Identifier: Apache-2.0

import { threadKeyForMessage } from "../src/features/chat/thread-map";
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
          createdAt: "2026-09-24T00:00:00Z",
          debugTargetSessionId: "",
          id: "s1",
          kind: "main",
          modelId: "offline",
          state: "idle",
          title: state.account === "opaque-a" ? "Alice chat" : "Bob chat",
          titleProvenance: "",
          titleRevision: "0",
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
  offlineBff.json("GET", "/api/v1/sessions/s1/transcript", {
    complete: true,
    messages: [],
    sessionId: "s1",
  });
  offlineBff.on("GET", "/api/v1/sessions/s1/activity", () => ({
    body: "",
    contentType: "text/event-stream",
  }));
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
    expect(url.searchParams.get("return_to")).toBe("/workspace/chat?sessionId=s1#draft");
    if (!url.searchParams.has("flow")) {
      if (state.popupCompletes) state.signedIn = true;
      return {
        headers: { Location: url.searchParams.get("return_to") ?? "/" },
        status: 302,
      };
    }
    expect(url.searchParams.get("flow")).toBe("popup");
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
  offlineBff.on("POST", "/api/v1/auth/logout", () => {
    state.signedIn = false;
    return { status: 204 };
  });
  return state;
}

const draftRoute = "/workspace/chat?sessionId=s1#draft";

async function saveAccountArtifacts(page: import("@playwright/test").Page, owner: "Alice" | "Bob") {
  await page.evaluate((name) => {
    const folderId = `${name.toLowerCase()}-folder`;
    localStorage.setItem(
      "studio.chat.folders",
      JSON.stringify({
        assignments: { s1: folderId },
        folders: [{ id: folderId, name: `${name} folder` }],
      }),
    );
    localStorage.setItem(
      "studio.chat.queue.s1",
      JSON.stringify([
        { createdAt: 1, id: `${name.toLowerCase()}-queued`, text: `${name} queued prompt` },
      ]),
    );
    sessionStorage.setItem(
      "studio.chat.failedRun.s1",
      JSON.stringify({
        message: `${name} failed run`,
        permanent: false,
        prompt: `${name} failed prompt`,
      }),
    );
    localStorage.setItem("mecatl-studio-theme", "dark");
    localStorage.setItem("mecatl-studio.palette", "solar");
  }, owner);
}

async function accountArtifacts(page: import("@playwright/test").Page) {
  return page.evaluate(() => ({
    account: localStorage.getItem("studio.account"),
    folder: localStorage.getItem("studio.chat.folders"),
    queue: localStorage.getItem("studio.chat.queue.s1"),
    failedRun: sessionStorage.getItem("studio.chat.failedRun.s1"),
    localKeys: Object.keys(localStorage)
      .filter((key) => key.startsWith("studio."))
      .sort(),
    sessionKeys: Object.keys(sessionStorage)
      .filter((key) => key.startsWith("studio."))
      .sort(),
    theme: localStorage.getItem("mecatl-studio-theme"),
    palette: localStorage.getItem("mecatl-studio.palette"),
  }));
}

async function expectAccountArtifacts(
  page: import("@playwright/test").Page,
  owner: "Alice" | "Bob",
) {
  await expect(page.getByRole("heading", { name: `${owner} folder` })).toBeVisible();
  await expect(page.getByRole("region", { name: "Queued messages" })).toContainText(
    `${owner} queued prompt`,
  );
  await expect(page.getByText(`${owner} failed run`)).toBeVisible();
}

for (const signIn of ["popup", "new tab"] as const) {
  test(`large seeded link stays in the original tab through ${signIn} sign-in until explicit Send`, async ({
    offlineBff,
    page,
  }) => {
    const state = setup(offlineBff, { signedIn: false });
    const seed = `Review ${"x".repeat(3_000)}`;
    const arrival = `/workspace/chat?sessionId=s1&prompt=${encodeURIComponent(seed)}&send=1`;
    offlineBff.on("GET", "/api/v1/auth/login", (request) => {
      const url = new URL(request.url());
      const returnTo = url.searchParams.get("return_to");
      expect(returnTo).toBe("/workspace/chat?sessionId=s1");
      expect(returnTo?.length).toBeLessThanOrEqual(2_048);
      state.signedIn = true;
      return url.searchParams.get("flow") === "popup"
        ? {
            body: '<!doctype html><html data-result="success"><title>Signed in</title><script src="/api/v1/auth/callback.js" defer></script></html>',
            contentType: "text/html",
          }
        : { headers: { Location: returnTo ?? "/" }, status: 302 };
    });

    await page.goto(arrival);
    await expect(page.getByRole("button", { name: "Sign in to Mecatl" })).toBeVisible();
    expect(new URL(page.url()).searchParams.get("prompt")).toBe(seed);
    expect(state.runWrites).toBe(0);
    if (signIn === "new tab") {
      await page.evaluate(() => {
        window.open = () => null;
      });
      await page.getByRole("button", { name: "Sign in to Mecatl" }).click();
      const opened = page.waitForEvent("popup");
      await page.getByRole("link", { name: "Open sign-in in a new tab" }).first().click();
      const tab = await opened;
      await expect(tab).toHaveURL(/\/workspace\/chat\?sessionId=s1$/);
      await tab.close();
      await page.bringToFront();
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    } else {
      const opened = page.waitForEvent("popup");
      await page.getByRole("button", { name: "Sign in to Mecatl" }).click();
      await opened;
    }
    const confirmation = page.getByRole("dialog", { name: "Send this prompt?" });
    await expect(confirmation).toBeVisible();
    await expect(confirmation).toContainText(seed);
    await expect(confirmation).toContainText("Alice chat");
    await expect(confirmation).toContainText("offline");
    await expect(confirmation).toContainText("Manual");
    expect(new URL(page.url()).searchParams.get("sessionId")).toBe("s1");
    expect(new URL(page.url()).searchParams.has("prompt")).toBe(false);
    expect(state.runWrites).toBe(0);
    await confirmation.getByRole("button", { name: "Send prompt" }).click();
    await expect.poll(() => state.runWrites).toBe(1);
  });
}

test("seed confirmation and composer remain in the viewport at 320, 500, and 1280 px", async ({
  offlineBff,
  page,
}) => {
  setup(offlineBff);
  await page.goto("/workspace/chat?sessionId=s1&prompt=Review%20this&send=1");
  const confirmation = page.getByRole("dialog", { name: "Send this prompt?" });
  for (const width of [320, 500, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    await expect(confirmation).toBeVisible();
    await expect(confirmation.getByRole("button", { name: "Send prompt" })).toBeVisible();
    const bounds = await confirmation.boundingBox();
    if (!bounds) throw new Error("Seed confirmation has no visible bounds");
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
  }
  await confirmation.getByRole("button", { name: "Edit prompt" }).click();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
});

test("returning after 20 seconds shows one verified offline notice without persisting it", async ({
  offlineBff,
  page,
}) => {
  setup(offlineBff);
  let offline = false;
  offlineBff.on("GET", "/api/v1/runtime", () => ({
    body: JSON.stringify({
      capabilities: { image: false, posture: "managed" },
      connection: offline ? "offline" : "online",
    }),
    contentType: "application/json",
  }));
  await page.clock.install({ time: new Date("2026-09-24T12:00:00Z") });
  await page.goto("/workspace/chat?sessionId=s1");
  await expect(page.getByRole("heading", { name: "Alice chat" })).toBeVisible();
  const storageBefore = await page.evaluate(() => ({
    local: Object.keys(localStorage).sort(),
    session: Object.keys(sessionStorage).sort(),
  }));
  // Chromium headless keeps background pages visible, so drive the document's
  // visibility API while exercising the real rendered app and BFF requests.
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.fastForward(20_000);
  offline = true;
  offlineBff.json("GET", "/api/v1/sessions", { status: 503 }, 503);
  offlineBff.json("GET", "/api/v1/sessions/s1", { status: 503 }, 503);
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect(page.getByText("Mecatl is offline.")).toBeVisible();
  expect(
    await page.evaluate(() => ({
      local: Object.keys(localStorage).sort(),
      session: Object.keys(sessionStorage).sort(),
    })),
  ).toEqual(storageBefore);
  await page.reload();
  await expect(page.getByText("Mecatl is offline.")).toHaveCount(0);
});

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
  const readsBefore = offlineBff.requestsFor("GET", "/api/v1/sessions").length;
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
  await expect
    .poll(() => offlineBff.requestsFor("GET", "/api/v1/sessions").length)
    .toBeGreaterThan(readsBefore);
});

test("cross-origin issuer returns to a popup that can message its opener", async ({
  offlineBff,
  page,
}) => {
  const state = setup(offlineBff);
  await page.route("**/workspace/chat**", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      headers: { ...response.headers(), "cross-origin-opener-policy": "same-origin-allow-popups" },
    });
  });
  await page.goto(draftRoute);
  await expireWrite(page, state);
  const origin = new URL(page.url()).origin;
  await page.evaluate(() => {
    (window as Window & { popupMessages?: unknown[] }).popupMessages = [];
    window.addEventListener("message", (event) => {
      (window as Window & { popupMessages?: unknown[] }).popupMessages?.push(event.data);
    });
  });
  // The BFF's 302 is pinned by the server test. A scripted first hop lets
  // Playwright intercept the popup's issuer document and exercise COOP here.
  offlineBff.on("GET", "/api/v1/auth/login", () => ({
    body: '<!doctype html><script>location.replace("https://issuer.offline.invalid/authorize")</script>',
    contentType: "text/html",
  }));
  let issuerVisits = 0;
  offlineBff.onIssuer("/authorize", () => {
    issuerVisits += 1;
    return {
      body: `<!doctype html><script>location.replace(${JSON.stringify(`${origin}/api/v1/auth/callback?code=code-1`)})</script>`,
      contentType: "text/html",
    };
  });
  offlineBff.on("GET", "/api/v1/auth/callback", () => {
    state.signedIn = true;
    return {
      body: '<!doctype html><html data-result="success"><script src="/api/v1/auth/callback.js" defer></script></html>',
      contentType: "text/html",
      headers: { "Cross-Origin-Opener-Policy": "unsafe-none" },
    };
  });
  const popupEvent = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await popupEvent;
  await expect
    .poll(() =>
      page.evaluate(() => (window as Window & { popupMessages?: unknown[] }).popupMessages),
    )
    .toEqual([{ type: "studio.auth.result", result: "success" }]);
  expect(issuerVisits).toBe(1);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );
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

test("manual new-tab sign-in redirects back to the current route", async ({ offlineBff, page }) => {
  const state = setup(offlineBff);
  await page.goto(draftRoute);
  await expireWrite(page, state);
  await page.evaluate(() => {
    window.open = () => null;
  });
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  const link = page.getByRole("link", { name: "Open sign-in in a new tab" }).first();
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute("rel", /noopener noreferrer/);
  const login = new URL((await link.getAttribute("href")) ?? "", page.url());
  expect(login.searchParams.has("flow")).toBe(false);
  expect(login.searchParams.get("return_to")).toBe(draftRoute);

  offlineBff.on("GET", "/api/v1/auth/login", (request) => {
    const url = new URL(request.url());
    expect(url.searchParams.has("flow")).toBe(false);
    expect(url.searchParams.get("return_to")).toBe(draftRoute);
    state.signedIn = true;
    return { headers: { Location: draftRoute }, status: 302 };
  });
  const opened = page.waitForEvent("popup");
  await link.click();
  const tab = await opened;
  await expect
    .poll(() => {
      const url = new URL(tab.url());
      return url.pathname + url.search + url.hash;
    })
    .toBe(draftRoute);
  expect(await tab.evaluate(() => window.opener)).toBeNull();
  expect(new URL(page.url()).pathname + new URL(page.url()).search + new URL(page.url()).hash).toBe(
    draftRoute,
  );
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue(
    "Keep this unsent draft",
  );
  await tab.close();
  await page.bringToFront();
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toHaveCount(0);
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

test("side-thread SSE expiry preserves its draft until an explicit retry", async ({
  offlineBff,
  page,
}) => {
  const state = setup(offlineBff);
  const rootMessage = { content: "Root message", role: "user" };
  const messageKey = threadKeyForMessage(rootMessage);
  await page.addInitScript(
    ({ key }) => {
      // This saved thread belongs to the account returned by the BFF.
      localStorage.setItem("studio.account", "opaque-a");
      sessionStorage.setItem("studio.account", "opaque-a");
      localStorage.setItem(
        "studio.chat.threads.s1",
        JSON.stringify({ [key]: { sessionId: "s2" } }),
      );
    },
    { key: messageKey },
  );
  offlineBff.json("GET", "/api/v1/sessions/s1/transcript", {
    complete: true,
    messages: [{ images: [], role: "user", text: rootMessage.content, toolCalls: [] }],
    sessionId: "s1",
  });
  offlineBff.json("GET", "/api/v1/sessions/s2", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "s2",
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
  offlineBff.json("GET", "/api/v1/sessions/s2/transcript", {
    complete: true,
    messages: [],
    sessionId: "s2",
  });
  let sideWrites = 0;
  offlineBff.on("POST", "/api/v1/sessions/s2/runs", () => {
    sideWrites += 1;
    if (sideWrites === 1) {
      state.signedIn = false;
      return {
        body: JSON.stringify({ code: "session_expired", status: 401 }),
        contentType: "application/problem+json",
        status: 401,
      };
    }
    return {
      body: `data: ${JSON.stringify({ type: "run.started", runId: "r2", sessionId: "s2" })}\n\n`,
      contentType: "text/event-stream",
    };
  });

  await page.goto(draftRoute);
  await page.getByRole("button", { name: "Open side thread" }).click();
  const draft = page.getByRole("textbox", { name: "Reply in thread" });
  await draft.fill("Keep this thread reply");
  await page.getByRole("button", { name: "Send reply" }).click();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  await expect(draft).toHaveValue("Keep this thread reply");
  const popupEvent = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await popupEvent;
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toHaveCount(0);
  expect(sideWrites).toBe(1);
  await page.getByRole("button", { name: "Send reply" }).click();
  await expect.poll(() => sideWrites).toBe(2);
});

test("same-account reload keeps artifacts and a different account clears them before rendering", async ({
  offlineBff,
  page,
}) => {
  const state = setup(offlineBff);
  await page.goto(draftRoute);
  await expect(page.getByRole("heading", { name: "Alice chat" })).toBeVisible();
  await saveAccountArtifacts(page, "Alice");
  const aliceArtifacts = await accountArtifacts(page);
  await page.reload();
  await expectAccountArtifacts(page, "Alice");
  expect(await accountArtifacts(page)).toEqual(aliceArtifacts);

  await expireWrite(page, state);
  state.account = "opaque-b";
  const popupEvent = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await popupEvent;
  await expect(page.getByRole("heading", { name: "Bob chat" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveValue("");
  await expect(page.getByRole("heading", { name: "Alice folder" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Queued messages" })).toHaveCount(0);
  await expect(page.getByText("Alice failed run")).toHaveCount(0);
  expect(await accountArtifacts(page)).toEqual({
    account: "opaque-b",
    folder: null,
    queue: null,
    failedRun: null,
    localKeys: ["studio.account"],
    sessionKeys: ["studio.account"],
    theme: "dark",
    palette: "solar",
  });
});

test("accountless session clears mounted private data", async ({ offlineBff, page }) => {
  const state = setup(offlineBff);
  await page.clock.install();
  await page.goto(draftRoute);
  await expect(page.getByRole("heading", { name: "Alice chat" })).toBeVisible();
  await page.getByRole("textbox", { name: "Message Mecatl" }).fill("Alice's draft");
  await saveAccountArtifacts(page, "Alice");
  state.account = "";
  await page.clock.runFor(60_001);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Mecatl Studio" })).toBeVisible();
  expect(await accountArtifacts(page)).toEqual({
    account: null,
    folder: null,
    queue: null,
    failedRun: null,
    localKeys: [],
    sessionKeys: [],
    theme: "dark",
    palette: "solar",
  });
});

test("peer account marker protects Bob's data while the stale Alice tab rechecks", async ({
  context,
  offlineBff,
  page,
}) => {
  const state = setup(offlineBff);
  await page.goto(draftRoute);
  await expect(page.getByRole("heading", { name: "Alice chat" })).toBeVisible();
  await saveAccountArtifacts(page, "Alice");
  const peer = await context.newPage();
  await peer.goto(draftRoute);
  await expect(peer.getByRole("heading", { name: "Alice chat" })).toBeVisible();

  let releaseAliceCheck!: () => void;
  let observeAliceCheck!: () => void;
  const aliceCheckPending = new Promise<void>((resolve) => {
    observeAliceCheck = resolve;
  });
  const aliceCheckGate = new Promise<void>((resolve) => {
    releaseAliceCheck = resolve;
  });
  await page.route("**/api/v1/auth/session", async (route) => {
    observeAliceCheck();
    await aliceCheckGate;
    await route.fulfill({
      body: JSON.stringify({ mode: "oidc", status: "authenticated", account: "opaque-b" }),
      contentType: "application/json",
    });
  });

  try {
    state.account = "opaque-b";
    await peer.reload();
    await expect(peer.getByRole("heading", { name: "Bob chat" })).toBeVisible();
    await aliceCheckPending;
    await expect(page.getByRole("heading", { name: "Alice chat" })).toHaveCount(0);
    await saveAccountArtifacts(peer, "Bob");
    await peer.reload();
    await expectAccountArtifacts(peer, "Bob");
    const bobArtifacts = await accountArtifacts(peer);

    const staleAccess = await page.evaluate(async () => {
      const scoped = (await import(
        /* @vite-ignore */ `${window.location.origin}/src/lib/account-storage.ts`
      )) as typeof import("../src/lib/account-storage");
      const folders = scoped.readUserScopedItem("studio.chat.folders");
      const queue = scoped.readUserScopedItem("studio.chat.queue.s1");
      const keys = scoped.listUserScopedKeys("studio.chat.queue.");
      scoped.writeUserScopedItem("studio.chat.folders", "Alice stale folder");
      scoped.writeUserScopedItem("studio.chat.queue.s1", "Alice stale queue");
      scoped.writeUserScopedItem(
        "studio.chat.failedRun.s1",
        "Alice stale failed prompt",
        sessionStorage,
      );
      return { folders, queue, keys };
    });
    expect(staleAccess).toEqual({ folders: null, queue: null, keys: [] });
    expect(await accountArtifacts(peer)).toEqual(bobArtifacts);
    expect((await accountArtifacts(page)).failedRun).toBeNull();
  } finally {
    releaseAliceCheck();
  }
  await expect(page.getByRole("heading", { name: "Bob chat" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Bob folder" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Queued messages" })).toContainText(
    "Bob queued prompt",
  );
  await expect(page.getByText("Alice failed run")).toHaveCount(0);
});

test("peer-tab sign-out removes the mounted private workspace", async ({
  context,
  offlineBff,
  page,
}) => {
  setup(offlineBff);
  await page.goto(draftRoute);
  await expect(page.getByRole("heading", { name: "Alice chat" })).toBeVisible();
  await page.getByRole("textbox", { name: "Message Mecatl" }).fill("Alice's draft");
  await saveAccountArtifacts(page, "Alice");
  const peer = await context.newPage();
  await peer.goto("/workspace/settings/about");
  await peer.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Mecatl Studio" })).toBeVisible();
  expect(new URL(page.url()).pathname + new URL(page.url()).search + new URL(page.url()).hash).toBe(
    draftRoute,
  );
  expect(await accountArtifacts(page)).toEqual({
    account: null,
    folder: null,
    queue: null,
    failedRun: null,
    localKeys: [],
    sessionKeys: [],
    theme: "dark",
    palette: "solar",
  });
});

test("anonymous shell shows outage before sign-in", async ({ offlineBff, page }) => {
  setup(offlineBff, { signedIn: false });
  await page.goto(draftRoute);
  await expect(page.getByRole("button", { name: /^Sign in( to Mecatl)?$/ })).toHaveCount(1);
  await expect(page.getByRole("status")).toHaveCount(0);
  await expect(page.getByText("Mecatl Studio", { exact: true })).toHaveCount(1);

  offlineBff.json("GET", "/api/v1/status", { connection: "unavailable", signInRequired: true });
  await page.reload();
  await expect(page.getByText("The Mecatl instance is unavailable right now.")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Sign in( to Mecatl)?$/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Mecatl Studio" })).toBeVisible();
  expect(offlineBff.requestsFor("GET", "/api/v1/runtime")).toHaveLength(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/storage/health")).toHaveLength(0);
  expect(offlineBff.requestsFor("GET", "/api/v1/settings/runtime")).toHaveLength(0);
});

test("failed public status fetch shows BFF unavailable", async ({ offlineBff, page }) => {
  setup(offlineBff, { signedIn: false });
  offlineBff.fail("GET", "/api/v1/status");
  await page.goto(draftRoute);
  await expect(page.getByText("Studio is unavailable right now.")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Sign in( to Mecatl)?$/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
});

test("session check outage keeps the shell and route", async ({ offlineBff, page }) => {
  const state = setup(offlineBff, { sessionFails: true });
  await page.clock.install();
  await page.goto(draftRoute);
  await expect(page.getByRole("heading", { name: "Mecatl Studio" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  expect(offlineBff.requestsFor("GET", "/api/v1/runtime")).toHaveLength(0);
  state.sessionFails = false;
  await page.getByRole("button", { name: "Try again" }).click();
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
