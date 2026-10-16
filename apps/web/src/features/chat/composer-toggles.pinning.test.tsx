// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the composer area's pressed controls before they become the shadcn
 * Toggle: the composer's and the side thread's dictation buttons, and the
 * phone sheet's Mode rows, Model rows and Effort pills. Each is a `button`
 * whose name and `aria-pressed` follow its state; a click, Enter or Space
 * acts; pressing the chosen choice again picks it again; and every choice is
 * its own Tab stop (no roving focus, no radio roles). Roles, names, state and
 * callbacks only, so these hold on both implementations.
 */

import {
  getRuntimeSettingsOptions,
  getSessionDetailOptions,
  getSessionTranscriptOptions,
  listSessionsOptions,
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { AuthRecoveryContext } from "../auth/auth-recovery-context";
import { ChatComposer, type DraftChatConfiguration } from "./chat-composer";
import { SideThreadPanel } from "./side-thread-panel";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

class FakeRecognition {
  onresult: ((event: unknown) => void) | null = null;
  onerror: (() => void) | null = null;
  onend: (() => void) | null = null;
  continuous = false;
  interimResults = false;
  lang = "";
  start() {}
  stop() {
    this.onend?.();
  }
}

beforeEach(() => {
  clearUserScopedStorage();
  Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
  Object.defineProperty(window, "SpeechRecognition", {
    configurable: true,
    value: FakeRecognition,
  });
});

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(window, "SpeechRecognition");
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const models = [
  {
    id: "deep",
    image: false,
    label: "Deep model",
    providerId: "provider",
    providerLabel: "Provider",
    reasoning: true,
  },
  {
    id: "plain",
    image: false,
    label: "Plain model",
    providerId: "provider",
    providerLabel: "Provider",
    reasoning: true,
  },
];

function pressed(element: HTMLElement) {
  return element.getAttribute("aria-pressed");
}

function expectPlainButton(element: HTMLElement) {
  expect(element.tagName).toBe("BUTTON");
  expect(element.getAttribute("role")).toBeNull();
  expect(element.getAttribute("aria-checked")).toBeNull();
  expect(element.getAttribute("type")).toBe("button");
}

/** A draft composer that keeps the configuration it reports. */
function DraftComposer({
  initial,
  onChange,
}: {
  initial: DraftChatConfiguration;
  onChange: (configuration: DraftChatConfiguration) => void;
}) {
  const [configuration, setConfiguration] = useState(initial);
  return (
    <ChatComposer
      configuration={configuration}
      models={models}
      onConfigurationChange={(next) => {
        onChange(next);
        setConfiguration(next);
      }}
      onSend={vi.fn().mockResolvedValue(true)}
    />
  );
}

async function openSheetView(user: ReturnType<typeof userEvent.setup>, row: RegExp) {
  await user.click(screen.getByRole("button", { name: "Composer options" }));
  const sheet = screen.getByRole("dialog", { name: "Composer options" });
  await user.click(within(sheet).getByRole("button", { name: row }));
  return sheet;
}

describe("composer dictation toggle pins", () => {
  it("presses and releases on click, renaming itself and showing Listening", async () => {
    const user = userEvent.setup();
    render(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    const toggle = screen.getByRole("button", { name: "Start dictation" });
    expectPlainButton(toggle);
    expect(pressed(toggle)).toBe("false");
    expect(screen.queryByText("Listening")).toBeNull();

    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Stop dictation" })).toBe(toggle);
    expect(pressed(toggle)).toBe("true");
    expect(screen.getByText("Listening")).toBeTruthy();

    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Start dictation" })).toBe(toggle);
    expect(pressed(toggle)).toBe("false");
    expect(screen.queryByText("Listening")).toBeNull();
  });

  it("toggles with Enter and Space and keeps focus", async () => {
    const user = userEvent.setup();
    render(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    const toggle = screen.getByRole("button", { name: "Start dictation" });
    act(() => toggle.focus());

    await user.keyboard("{Enter}");
    expect(pressed(toggle)).toBe("true");
    expect(document.activeElement).toBe(toggle);

    await user.keyboard(" ");
    expect(pressed(toggle)).toBe("false");
    expect(document.activeElement).toBe(toggle);
  });

  it("is disabled while the composer is busy", async () => {
    const user = userEvent.setup();
    render(<ChatComposer disabled onSend={vi.fn().mockResolvedValue(true)} />);
    const toggle = screen.getByRole<HTMLButtonElement>("button", { name: "Start dictation" });
    expect(toggle.disabled).toBe(true);
    await user.click(toggle);
    expect(pressed(toggle)).toBe("false");
  });
});

describe("phone sheet Mode rows pins", () => {
  it("marks only the current mode pressed, each row its own Tab stop", async () => {
    const user = userEvent.setup();
    render(
      <DraftComposer initial={{ mode: "plan", reasoningEffort: "default" }} onChange={vi.fn()} />,
    );
    const sheet = await openSheetView(user, /^Mode\s/);
    const manual = within(sheet).getByRole("button", { name: /^Manual/ });
    const accept = within(sheet).getByRole("button", { name: /^Accept edits/ });
    const plan = within(sheet).getByRole("button", { name: /^Plan/ });
    for (const row of [manual, accept, plan]) expectPlainButton(row);
    expect([pressed(manual), pressed(accept), pressed(plan)]).toEqual(["false", "false", "true"]);
    expect(manual.textContent).toContain("Always ask before making changes.");

    act(() => manual.focus());
    await user.tab();
    expect(document.activeElement).toBe(accept);
    await user.tab();
    expect(document.activeElement).toBe(plan);
  });

  it("picks a mode on click and closes the sheet", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer
        initial={{ mode: "default", reasoningEffort: "default" }}
        onChange={onChange}
      />,
    );
    let sheet = await openSheetView(user, /^Mode\s/);
    await user.click(within(sheet).getByRole("button", { name: /^Accept edits/ }));
    expect(onChange).toHaveBeenLastCalledWith({ mode: "acceptEdits", reasoningEffort: "default" });
    expect(screen.queryByRole("dialog", { name: "Composer options" })).toBeNull();

    sheet = await openSheetView(user, /^Mode\s/);
    expect(pressed(within(sheet).getByRole("button", { name: /^Accept edits/ }))).toBe("true");
    expect(pressed(within(sheet).getByRole("button", { name: /^Manual/ }))).toBe("false");
  });

  it("picks the current mode again when it is pressed again", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer initial={{ mode: "plan", reasoningEffort: "default" }} onChange={onChange} />,
    );
    const sheet = await openSheetView(user, /^Mode\s/);
    await user.click(within(sheet).getByRole("button", { name: /^Plan/ }));
    expect(onChange).toHaveBeenLastCalledWith({ mode: "plan", reasoningEffort: "default" });
    expect(screen.queryByRole("dialog", { name: "Composer options" })).toBeNull();
  });

  it("picks a mode with Enter and with Space", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer
        initial={{ mode: "default", reasoningEffort: "default" }}
        onChange={onChange}
      />,
    );
    let sheet = await openSheetView(user, /^Mode\s/);
    act(() => within(sheet).getByRole("button", { name: /^Plan/ }).focus());
    await user.keyboard("{Enter}");
    expect(onChange).toHaveBeenLastCalledWith({ mode: "plan", reasoningEffort: "default" });

    sheet = await openSheetView(user, /^Mode\s/);
    act(() =>
      within(sheet)
        .getByRole("button", { name: /^Manual/ })
        .focus(),
    );
    await user.keyboard(" ");
    expect(onChange).toHaveBeenLastCalledWith({ mode: "default", reasoningEffort: "default" });
  });
});

describe("phone sheet Model rows pins", () => {
  it("marks only the chosen model pressed, Default included, each row its own Tab stop", async () => {
    const user = userEvent.setup();
    render(
      <DraftComposer
        initial={{
          mode: "default",
          model: { id: "plain", providerId: "provider" },
          reasoningEffort: "default",
        }}
        onChange={vi.fn()}
      />,
    );
    const sheet = await openSheetView(user, /^Model/);
    const auto = within(sheet).getByRole("button", { name: "Default" });
    const deep = within(sheet).getByRole("button", { name: "Deep model" });
    const plain = within(sheet).getByRole("button", { name: "Plain model" });
    for (const row of [auto, deep, plain]) expectPlainButton(row);
    expect([pressed(auto), pressed(deep), pressed(plain)]).toEqual(["false", "false", "true"]);

    act(() => auto.focus());
    await user.tab();
    expect(document.activeElement).toBe(deep);
    await user.tab();
    expect(document.activeElement).toBe(plain);
  });

  it("picks a model and Default on click, closing the sheet each time", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer
        initial={{ mode: "default", reasoningEffort: "default" }}
        onChange={onChange}
      />,
    );
    let sheet = await openSheetView(user, /^Model/);
    expect(pressed(within(sheet).getByRole("button", { name: "Default" }))).toBe("true");
    await user.click(within(sheet).getByRole("button", { name: "Deep model" }));
    expect(onChange).toHaveBeenLastCalledWith({
      mode: "default",
      model: { id: "deep", providerId: "provider" },
      reasoningEffort: "default",
    });
    expect(screen.queryByRole("dialog", { name: "Composer options" })).toBeNull();

    sheet = await openSheetView(user, /^Model/);
    expect(pressed(within(sheet).getByRole("button", { name: "Deep model" }))).toBe("true");
    expect(pressed(within(sheet).getByRole("button", { name: "Default" }))).toBe("false");
    await user.click(within(sheet).getByRole("button", { name: "Default" }));
    expect(onChange).toHaveBeenLastCalledWith({
      mode: "default",
      model: undefined,
      reasoningEffort: "default",
    });
  });

  it("picks the chosen model again when it is pressed again", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer
        initial={{
          mode: "default",
          model: { id: "deep", providerId: "provider" },
          reasoningEffort: "default",
        }}
        onChange={onChange}
      />,
    );
    const sheet = await openSheetView(user, /^Model/);
    await user.click(within(sheet).getByRole("button", { name: "Deep model" }));
    expect(onChange).toHaveBeenLastCalledWith({
      mode: "default",
      model: { id: "deep", providerId: "provider" },
      reasoningEffort: "default",
    });
    expect(screen.queryByRole("dialog", { name: "Composer options" })).toBeNull();
  });

  it("picks a model with Space", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer
        initial={{ mode: "default", reasoningEffort: "default" }}
        onChange={onChange}
      />,
    );
    const sheet = await openSheetView(user, /^Model/);
    act(() => within(sheet).getByRole("button", { name: "Plain model" }).focus());
    await user.keyboard(" ");
    expect(onChange).toHaveBeenLastCalledWith({
      mode: "default",
      model: { id: "plain", providerId: "provider" },
      reasoningEffort: "default",
    });
  });
});

describe("phone sheet Effort pills pins", () => {
  it("marks only the current effort pressed, each pill its own Tab stop", async () => {
    const user = userEvent.setup();
    render(
      <DraftComposer initial={{ mode: "default", reasoningEffort: "high" }} onChange={vi.fn()} />,
    );
    const sheet = await openSheetView(user, /^Model/);
    const group = within(sheet).getByRole("group", { name: "Effort" });
    const pills = within(group).getAllByRole("button");
    expect(pills.map((pill) => pill.textContent)).toEqual([
      "Auto",
      "Low",
      "Medium",
      "High",
      "Extra high",
      "Max",
    ]);
    for (const pill of pills) expectPlainButton(pill);
    expect(pills.map(pressed)).toEqual(["false", "false", "false", "true", "false", "false"]);

    act(() => pills[0]?.focus());
    for (const pill of pills.slice(1)) {
      await user.tab();
      expect(document.activeElement).toBe(pill);
    }
  });

  it("picks an effort on click, and the current one again, closing the sheet", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer
        initial={{ mode: "default", reasoningEffort: "default" }}
        onChange={onChange}
      />,
    );
    let sheet = await openSheetView(user, /^Model/);
    await user.click(within(sheet).getByRole("button", { name: "Max" }));
    expect(onChange).toHaveBeenLastCalledWith({ mode: "default", reasoningEffort: "max" });
    expect(screen.queryByRole("dialog", { name: "Composer options" })).toBeNull();

    sheet = await openSheetView(user, /^Model/);
    const max = within(sheet).getByRole("button", { name: "Max" });
    expect(pressed(max)).toBe("true");
    await user.click(max);
    expect(onChange).toHaveBeenLastCalledWith({ mode: "default", reasoningEffort: "max" });
    expect(screen.queryByRole("dialog", { name: "Composer options" })).toBeNull();
  });

  it("picks an effort with Enter", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <DraftComposer
        initial={{ mode: "default", reasoningEffort: "default" }}
        onChange={onChange}
      />,
    );
    const sheet = await openSheetView(user, /^Model/);
    act(() => within(sheet).getByRole("button", { name: "Low" }).focus());
    await user.keyboard("{Enter}");
    expect(onChange).toHaveBeenLastCalledWith({ mode: "default", reasoningEffort: "low" });
  });
});

async function renderSideThread() {
  vi.stubGlobal("fetch", () => new Promise<Response>(() => {}));
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  client.setQueryData(listSessionsOptions().queryKey, { complete: true, items: [] });
  client.setQueryData(getRuntimeSettingsOptions().queryKey, {
    buildId: "test",
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
    serverImplementation: "test",
  });
  client.setQueryData(getSessionDetailOptions({ path: { sessionId: "thread-1" } }).queryKey, {
    capabilities: {
      image: false,
      audio: false,
      executionFiles: false,
      builtInShell: false,
      manualCompaction: false,
      modelSelection: false,
    },
    id: "thread-1",
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
  client.setQueryData(getSessionTranscriptOptions({ path: { sessionId: "thread-1" } }).queryKey, {
    complete: true,
    messages: [],
    sessionId: "thread-1",
  });
  const route = createRootRoute({
    component: () => (
      <SideThreadPanel
        messageKey="occurrence"
        onClose={() => {}}
        parentSessionId="parent-1"
        sessionId="thread-1"
      />
    ),
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/"] }),
    routeTree: route,
  });
  await act(async () => {
    render(
      <QueryClientProvider client={client}>
        <AuthRecoveryContext.Provider
          value={{
            banner: { authenticated: true, publicStatusFailed: false, sessionCheckFailed: false },
            loginUrl: "/api/v1/auth/login",
            phase: "ready",
            popupIssue: null,
            retrySession: () => {},
            startPopupLogin: () => {},
          }}
        >
          <RouterProvider router={router} />
        </AuthRecoveryContext.Provider>
      </QueryClientProvider>,
    );
    await router.load();
  });
  return client;
}

describe("side thread dictation toggle pins", () => {
  it("presses and releases on click and from the keyboard, renaming itself", async () => {
    const user = userEvent.setup();
    const client = await renderSideThread();
    const toggle = await screen.findByRole("button", { name: "Start dictation" });
    expectPlainButton(toggle);
    expect(pressed(toggle)).toBe("false");

    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Stop dictation" })).toBe(toggle);
    expect(pressed(toggle)).toBe("true");

    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Start dictation" })).toBe(toggle);
    expect(pressed(toggle)).toBe("false");

    act(() => toggle.focus());
    await user.keyboard("{Enter}");
    expect(pressed(toggle)).toBe("true");
    expect(document.activeElement).toBe(toggle);
    await user.keyboard(" ");
    expect(pressed(toggle)).toBe("false");
    expect(document.activeElement).toBe(toggle);
    client.clear();
  });

  it("shares the composer's look and leaves its glyph to the Toggle phone rule", async () => {
    render(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    const composer = screen.getByRole("button", { name: "Start dictation" });
    const composerLook = composer.className;
    cleanup();
    const client = await renderSideThread();
    const thread = await screen.findByRole("button", { name: "Start dictation" });
    expect(thread.className).toBe(composerLook);
    for (const toggle of [composer, thread]) {
      // The phone rule in styles.css sizes a Toggle's glyph that has no size of its own.
      expect(toggle.hasAttribute("data-toggle")).toBe(true);
      expect(toggle.querySelector("svg")?.getAttribute("class")).not.toMatch(/(^|\s)(size|h|w)-/);
    }
    client.clear();
  });

  it("is absent where the browser cannot dictate", async () => {
    Reflect.deleteProperty(window, "SpeechRecognition");
    const client = await renderSideThread();
    expect(await screen.findByRole("textbox", { name: "Reply in thread" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start dictation" })).toBeNull();
    client.clear();
  });
});
