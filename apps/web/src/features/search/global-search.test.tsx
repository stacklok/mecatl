// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ShortcutProvider, useShortcut } from "../shortcuts/shortcut-provider";
import { GlobalSearch } from "./global-search";

const navigation = vi.hoisted(() => vi.fn(async () => undefined));
type AuthSession = {
  account?: string;
  mode: "none" | "oidc" | "static";
  status: string;
};
const inventoryState = vi.hoisted(() => ({
  authCalls: 0,
  authPending: undefined as Promise<void> | undefined,
  authSession: {
    account: "account-a",
    mode: "oidc",
    status: "authenticated",
  } as AuthSession,
  calls: [] as string[],
  failAuth: false,
  authErrorStatus: undefined as number | undefined,
  inventoryCalls: [] as string[],
  failSchedules: false,
  failSessionsUnauthorized: false,
  queryArguments: [] as unknown[],
  schedulePending: false,
  sessions: [] as Array<{ id: string; modelId: string; state: string; title: string }>,
}));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigation }));
vi.mock("@mecatl-studio/contracts/generated", () => ({
  getAuthSession: async () => {
    inventoryState.authCalls += 1;
    await inventoryState.authPending;
    if (inventoryState.failAuth) {
      if (inventoryState.authErrorStatus) throw { status: inventoryState.authErrorStatus };
      throw new Error("auth unavailable");
    }
    return { data: inventoryState.authSession };
  },
}));
vi.mock("@mecatl-studio/contracts/query", () => {
  const options = (key: string, data: unknown) => () => ({
    queryFn: async () => {
      if (key !== "auth") inventoryState.inventoryCalls.push(key);
      return key === "auth" ? inventoryState.authSession : data;
    },
    queryKey: [key],
  });
  return {
    getAuthSessionOptions: options("auth", {
      account: "account-a",
      mode: "oidc",
      status: "authenticated",
    }),
    listConfiguredSkillsOptions: options("configured-skills", { items: [], supported: true }),
    listLearnedSkillsOptions: options("learned-skills", { items: [], supported: true }),
    listSchedulesOptions: () => ({
      queryFn: async () => {
        inventoryState.inventoryCalls.push("schedules");
        if (inventoryState.failSchedules) throw new Error("schedule inventory failed");
        if (inventoryState.schedulePending) await new Promise(() => {});
        return { items: [], supported: true };
      },
      queryKey: ["schedules"],
    }),
    listSessionsOptions: (options?: unknown) => {
      // The query text may not become a BFF request option.
      inventoryState.queryArguments.push(options);
      return {
        queryFn: async () => {
          inventoryState.calls.push("sessions");
          inventoryState.inventoryCalls.push("sessions");
          if (inventoryState.failSessionsUnauthorized) throw { status: 401 };
          return { items: inventoryState.sessions };
        },
        queryKey: ["sessions"],
      };
    },
    listUserMemoryOptions: options("memory", { items: [], supported: true }),
  };
});

let root: Root | undefined;
let container: HTMLDivElement | undefined;

function BackgroundShortcut({ onInvoke }: { onInvoke: () => void }) {
  useShortcut("chat.toggleList", onInvoke);
  return <button type="button">Behind search</button>;
}

async function mount(
  onBackgroundShortcut = () => {},
  authSession: AuthSession = {
    account: "account-a",
    mode: "oidc",
    status: "authenticated",
  },
) {
  inventoryState.authSession = authSession;
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(["auth"], authSession);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(
      <StrictMode>
        <QueryClientProvider client={client}>
          <ShortcutProvider>
            <BackgroundShortcut onInvoke={onBackgroundShortcut} />
            <GlobalSearch />
          </ShortcutProvider>
        </QueryClientProvider>
      </StrictMode>,
    );
  });
  return client;
}

function keydown(target: EventTarget, key: string, modifiers: KeyboardEventInit = {}) {
  target.dispatchEvent(
    new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key, ...modifiers }),
  );
}

async function searchFor(value: string) {
  const input = document.querySelector<HTMLInputElement>('input[role="combobox"]');
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  await act(async () => {
    if (input) setter?.call(input, value);
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return input;
}

function touch(target: EventTarget, type: string, x: number, y: number) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "touches", { value: [{ clientX: x, clientY: y }] });
  target.dispatchEvent(event);
}

async function settleNavigationFocus() {
  await act(async () => {
    await new Promise((resolve) => requestAnimationFrame(resolve));
  });
}

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  container?.remove();
  container = undefined;
  document.body.replaceChildren();
  navigation.mockClear();
  inventoryState.authCalls = 0;
  inventoryState.authPending = undefined;
  inventoryState.authSession = {
    account: "account-a",
    mode: "oidc",
    status: "authenticated",
  };
  inventoryState.calls = [];
  inventoryState.failAuth = false;
  inventoryState.authErrorStatus = undefined;
  inventoryState.inventoryCalls = [];
  inventoryState.failSchedules = false;
  inventoryState.failSessionsUnauthorized = false;
  inventoryState.queryArguments = [];
  inventoryState.schedulePending = false;
  inventoryState.sessions = [];
});

describe("GlobalSearch", () => {
  it("traps focus and suppresses background shortcuts while open", async () => {
    const background = vi.fn();
    await mount(background);
    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    expect(trigger).not.toBeNull();
    trigger?.focus();

    await act(async () => keydown(document, "k", { ctrlKey: true }));
    const input = document.querySelector<HTMLInputElement>('input[role="combobox"]');
    expect(input).not.toBeNull();
    expect(document.activeElement).toBe(input);

    const behind = document.querySelector<HTMLButtonElement>("button:not([aria-label])");
    await act(async () => {
      keydown(input as HTMLInputElement, "Tab");
      behind?.focus();
    });
    expect(document.activeElement).toBe(input);

    await act(async () => keydown(input as HTMLInputElement, "?"));
    expect(navigation).not.toHaveBeenCalled();
    await act(async () => keydown(document, "b", { ctrlKey: true }));
    expect(background).not.toHaveBeenCalled();

    const main = document.createElement("main");
    const heading = document.createElement("h1");
    heading.textContent = "Keyboard shortcuts";
    heading.tabIndex = -1;
    main.append(heading);
    document.body.append(main);
    await searchFor("shortcuts");
    expect(document.querySelectorAll('[role="option"]')).toHaveLength(1);
    await act(async () => keydown(input as HTMLInputElement, "Enter"));
    await settleNavigationFocus();
    expect(navigation).toHaveBeenCalledExactlyOnceWith({ to: "/workspace/shortcuts" });
    await vi.waitFor(() => expect(document.activeElement).toBe(heading));
  });

  it("closes on account change and does not reuse another account's cached results", async () => {
    inventoryState.sessions = [
      { id: "session-a", modelId: "model", state: "idle", title: "Private Alpha" },
    ];
    const client = await mount();
    expect(inventoryState.calls).toEqual([]);

    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    await act(async () => trigger?.click());
    expect(inventoryState.calls).toEqual(["sessions"]);
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Alpha");

    inventoryState.sessions = [
      { id: "session-b", modelId: "model", state: "idle", title: "Private Beta" },
    ];
    inventoryState.authSession = {
      account: "account-b",
      mode: "oidc",
      status: "authenticated",
    };
    await act(async () => {
      client.setQueryData(["auth"], {
        account: "account-b",
        mode: "oidc",
        status: "authenticated",
      });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();

    const newTrigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    await act(async () => newTrigger?.click());
    expect(inventoryState.calls).toEqual(["sessions", "sessions"]);
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')).toBeNull();
    await searchFor("Private Beta");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Beta");

    await act(async () => {
      client.setQueryData(["auth"], { mode: "oidc", status: "anonymous" });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("keeps help search available without an account and never reuses private results", async () => {
    inventoryState.sessions = [
      { id: "session-a", modelId: "model", state: "idle", title: "Private Alpha" },
    ];
    const client = await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Alpha");
    expect(inventoryState.calls).toEqual(["sessions"]);
    const callsBeforeAccountLoss = [...inventoryState.inventoryCalls];
    expect(callsBeforeAccountLoss).toHaveLength(5);

    inventoryState.authSession = { mode: "oidc", status: "authenticated" };
    await act(async () => {
      client.setQueryData(["auth"], { mode: "oidc", status: "authenticated" });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();

    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    expect(trigger).not.toBeNull();
    await act(async () => keydown(document, "k", { ctrlKey: true }));
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')).toBeNull();
    await searchFor("shortcuts");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Keyboard shortcuts");
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(
      "Workspace inventories unavailable",
    );
    expect(inventoryState.calls).toEqual(["sessions"]);
    expect(inventoryState.inventoryCalls).toEqual(callsBeforeAccountLoss);
  });

  it("checks fresh identity before reopening cached private results", async () => {
    inventoryState.sessions = [
      { id: "session-a", modelId: "model", state: "idle", title: "Private Alpha" },
    ];
    const client = await mount();
    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    await act(async () => trigger?.click());
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Alpha");
    expect(inventoryState.authCalls).toBe(1);
    expect(inventoryState.calls).toEqual(["sessions"]);

    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Close search"]')?.click(),
    );
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(client.getQueryData(["auth"])).toEqual({
      account: "account-a",
      mode: "oidc",
      status: "authenticated",
    });

    inventoryState.sessions = [
      { id: "session-b", modelId: "model", state: "idle", title: "Private Beta" },
    ];
    inventoryState.authSession = {
      account: "account-b",
      mode: "oidc",
      status: "authenticated",
    };
    let releaseAuth: (() => void) | undefined;
    inventoryState.authPending = new Promise<void>((resolve) => {
      releaseAuth = resolve;
    });
    await act(async () => trigger?.click());
    await searchFor("Private Alpha");
    expect(document.body.textContent).not.toContain("Private Alpha");
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(inventoryState.authCalls).toBe(2);
    expect(inventoryState.calls).toEqual(["sessions"]);

    await act(async () => {
      releaseAuth?.();
      await inventoryState.authPending;
    });
    inventoryState.authPending = undefined;
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.body.textContent).not.toContain("Private Alpha");
    expect(client.getQueryData(["auth"])).toEqual(inventoryState.authSession);

    const newTrigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    await act(async () => newTrigger?.click());
    expect(inventoryState.authCalls).toBe(3);
    expect(inventoryState.calls).toEqual(["sessions", "sessions"]);
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')).toBeNull();
    await searchFor("Private Beta");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Beta");
  });

  it("shows only local help when a fresh identity check loses the BFF", async () => {
    inventoryState.sessions = [
      { id: "session-a", modelId: "model", state: "idle", title: "Private Alpha" },
    ];
    await mount();
    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    await act(async () => trigger?.click());
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Alpha");
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Close search"]')?.click(),
    );
    inventoryState.failAuth = true;
    await act(async () => trigger?.click());
    expect(document.body.textContent).not.toContain("Private Alpha");
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')).toBeNull();
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(
      "Workspace inventories unavailable",
    );
    await searchFor("shortcuts");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Keyboard shortcuts");
    expect(inventoryState.authCalls).toBe(2);
    expect(inventoryState.calls).toEqual(["sessions"]);

    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Close search"]')?.click(),
    );
    inventoryState.failAuth = false;
    await act(async () => trigger?.click());
    await searchFor("Private Alpha");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Alpha");
    expect(inventoryState.authCalls).toBe(3);
    expect(document.querySelector('[aria-live="polite"]')?.textContent).not.toContain(
      "Workspace inventories unavailable",
    );
  });

  it("keeps search closed after a rejected fresh identity check", async () => {
    await mount();
    inventoryState.failAuth = true;
    inventoryState.authErrorStatus = 401;
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(inventoryState.authCalls).toBe(1);
    expect(inventoryState.inventoryCalls).toEqual([]);
  });

  it("fetches only after an authorized session opens the palette and keeps the query local", async () => {
    await mount(() => {}, { mode: "oidc", status: "anonymous" });
    expect(document.querySelector('button[aria-label="Search"]')).toBeNull();
    expect(inventoryState.calls).toEqual([]);

    await act(async () => root?.unmount());
    container?.remove();
    await mount(() => {}, { mode: "static", status: "disabled" });
    expect(inventoryState.calls).toEqual([]);
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await searchFor("private local query");
    expect(inventoryState.calls).toEqual(["sessions"]);
    expect(inventoryState.queryArguments.every((argument) => argument === undefined)).toBe(true);
  });

  it("closes an expired session before stale inventory results can be selected", async () => {
    inventoryState.failSessionsUnauthorized = true;
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.disabled).toBe(
      true,
    );
  });

  it("treats a touch scroll as scrolling and a later tap as one choice", async () => {
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await searchFor("shortcuts");
    const option = document.querySelector<HTMLElement>('[role="option"]');
    expect(option).not.toBeNull();

    await act(async () => {
      touch(option as HTMLElement, "touchstart", 40, 100);
      touch(option as HTMLElement, "touchmove", 40, 145);
      option?.click();
    });
    expect(navigation).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();

    await act(async () => {
      touch(option as HTMLElement, "touchstart", 40, 100);
      option?.click();
    });
    expect(navigation).toHaveBeenCalledOnce();
    await settleNavigationFocus();
  });

  it("accepts a mouse click after a touch scroll that emitted no click", async () => {
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await searchFor("shortcuts");
    const option = document.querySelector<HTMLElement>('[role="option"]');
    expect(option).not.toBeNull();

    await act(async () => {
      touch(option as HTMLElement, "touchstart", 40, 100);
      touch(option as HTMLElement, "touchmove", 40, 145);
      touch(option as HTMLElement, "touchend", 40, 145);
    });
    expect(navigation).not.toHaveBeenCalled();

    await act(async () => {
      option?.dispatchEvent(
        new PointerEvent("pointerdown", { bubbles: true, pointerType: "mouse" }),
      );
      option?.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1 }));
    });
    expect(navigation).toHaveBeenCalledExactlyOnceWith({ to: "/workspace/shortcuts" });
    await settleNavigationFocus();
  });

  it("keeps static results when an inventory fails and reports the partial search", async () => {
    inventoryState.failSchedules = true;
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await searchFor("shortcuts");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Keyboard shortcuts");
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(
      "Some inventories could not be searched",
    );
  });

  it("retains successful dynamic results when a different inventory fails", async () => {
    inventoryState.sessions = [
      { id: "session-a", modelId: "model", state: "idle", title: "Private Alpha" },
    ];
    inventoryState.failSchedules = true;
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await searchFor("Private Alpha");
    await vi.waitFor(() =>
      expect(document.querySelector('[role="option"]')?.textContent).toContain("Private Alpha"),
    );
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(
      "Some inventories could not be searched",
    );
  });

  it("announces loading while an inventory remains pending", async () => {
    inventoryState.schedulePending = true;
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    await searchFor("shortcuts");
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Keyboard shortcuts");
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(
      "Loading searchable inventories",
    );
  });

  it("keeps the combobox linked to an empty listbox and announces no results", async () => {
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    const input = await searchFor("no-such-workspace-item");
    const listbox = document.querySelector('[role="listbox"]');
    expect(listbox?.id).toBe(input?.getAttribute("aria-controls"));
    expect(input?.getAttribute("aria-expanded")).toBe("true");
    expect(input?.hasAttribute("aria-activedescendant")).toBe(false);
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toBe("0 results");
    expect(document.body.textContent).toContain("No results for");
  });

  it("leaves IME candidate keys alone through the committing Enter", async () => {
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    const input = await searchFor("shortcuts");
    const activeOption = input?.getAttribute("aria-activedescendant");
    await act(async () => {
      input?.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
      keydown(input as HTMLInputElement, "ArrowDown");
      input?.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
      keydown(input as HTMLInputElement, "Enter");
    });
    expect(input?.getAttribute("aria-activedescendant")).toBe(activeOption);
    expect(navigation).not.toHaveBeenCalled();
    await act(async () => keydown(input as HTMLInputElement, "Enter"));
    expect(navigation).toHaveBeenCalledOnce();
  });

  it.each(["pointerdown", "touchstart"])(
    "allows Enter after an observable %s candidate choice without a committing keydown",
    async (type) => {
      await mount();
      await act(async () =>
        document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
      );
      const input = await searchFor("shortcuts");
      await act(async () => {
        input?.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
        if (type === "pointerdown") {
          input?.dispatchEvent(new PointerEvent(type, { bubbles: true, pointerType: "mouse" }));
        } else {
          touch(input as HTMLInputElement, type, 40, 100);
        }
        input?.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
      });
      await act(async () => keydown(input as HTMLInputElement, "Enter"));
      expect(navigation).toHaveBeenCalledExactlyOnceWith({ to: "/workspace/shortcuts" });
    },
  );

  it("respects native isComposing and key code 229 in the combobox handler", async () => {
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    const input = await searchFor("help");
    const initialOption = input?.getAttribute("aria-activedescendant");
    expect(document.querySelectorAll('[role="option"]').length).toBeGreaterThan(1);
    await act(async () => {
      keydown(input as HTMLInputElement, "ArrowDown", { isComposing: true });
      const processKey = new KeyboardEvent("keydown", {
        bubbles: true,
        cancelable: true,
        key: "Enter",
      });
      Object.defineProperty(processKey, "keyCode", { value: 229 });
      input?.dispatchEvent(processKey);
    });
    expect(input?.getAttribute("aria-activedescendant")).toBe(initialOption);
    expect(navigation).not.toHaveBeenCalled();
  });

  it("moves the active option with arrow keys and activates it only once", async () => {
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    const input = await searchFor("help");
    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')];
    expect(options.length).toBeGreaterThan(1);
    expect(input?.getAttribute("aria-activedescendant")).toBe(options[0]?.id);
    await act(async () => keydown(input as HTMLInputElement, "ArrowDown"));
    expect(input?.getAttribute("aria-activedescendant")).toBe(options[1]?.id);
    expect(navigation).not.toHaveBeenCalled();
    await act(async () => keydown(input as HTMLInputElement, "ArrowUp"));
    expect(input?.getAttribute("aria-activedescendant")).toBe(options[0]?.id);
    await act(async () => {
      keydown(input as HTMLInputElement, "Enter");
      keydown(input as HTMLInputElement, "Enter");
    });
    expect(navigation).toHaveBeenCalledOnce();
  });

  it("restores focus after Escape and keeps pointer hover separate from activation", async () => {
    await mount();
    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    trigger?.focus();
    await act(async () => trigger?.click());
    const input = await searchFor("help");
    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')];
    expect(options.length).toBeGreaterThan(1);
    await act(async () =>
      options[1]?.dispatchEvent(new MouseEvent("mousemove", { bubbles: true })),
    );
    expect(input?.getAttribute("aria-activedescendant")).toBe(options[1]?.id);
    expect(navigation).not.toHaveBeenCalled();
    await act(async () => keydown(input as HTMLInputElement, "Escape"));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("closes from the close control or an outside pointer and restores the trigger", async () => {
    await mount();
    const trigger = document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
    trigger?.focus();
    await act(async () => trigger?.click());
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Close search"]')?.click(),
    );
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull());
    await vi.waitFor(() => expect(document.activeElement).toBe(trigger));

    await act(async () => trigger?.click());
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
      document.body.dispatchEvent(
        new PointerEvent("pointerdown", { bubbles: true, cancelable: true, pointerType: "mouse" }),
      );
    });
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull());
    await vi.waitFor(() => expect(document.activeElement).toBe(trigger));
    expect(navigation).not.toHaveBeenCalled();
  });

  it("navigates through a palette shortcut once and closes search", async () => {
    await mount();
    await act(async () =>
      document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    const main = document.createElement("main");
    main.tabIndex = -1;
    document.body.append(main);
    await act(async () => keydown(document, ",", { ctrlKey: true }));
    await settleNavigationFocus();
    expect(navigation).toHaveBeenCalledExactlyOnceWith({ to: "/workspace/settings" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    await vi.waitFor(() => expect(document.activeElement).toBe(main));
  });
});
