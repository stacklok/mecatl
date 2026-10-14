// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { studioRouterOptions } from "../../components/error-page/error-routes";
import { stringSearchParams } from "../../lib/search-params";
import { routeTree } from "../../routeTree.gen";
import { AuthRecoveryContext } from "../auth/auth-recovery-context";
import { isSettingsSection, settingsGroups } from "./settings-sections";

/**
 * Pins the Settings navigation that #2261 moves from a native phone picker to
 * the prototype's section index: Studio's slugs and legacy redirects, the
 * desktop landing on You, and every section reachable at phone and desktop
 * widths with the current one announced. Each check finds a section by the
 * name a person sees and activates whatever control offers it, so the same
 * checks hold for either phone navigation.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const PHONE = 390;
const DESKTOP = 1280;

/** Studio's slugs in their route-contract order, with the label a person sees. */
const sections = [
  ["profile", "You"],
  ["appearance", "Personalise"],
  ["agent", "Agent"],
  ["permissions", "Permissions"],
  ["providers", "Providers"],
  ["models", "Models"],
  ["mcp-tools", "MCP tools"],
  ["storage", "Storage"],
  ["memory", "Memory"],
  ["learning", "Learning"],
  ["diagnostics", "Diagnostics"],
  ["labs", "Labs"],
  ["about", "About"],
] as const;

const responses: Record<string, unknown> = {
  "/api/v1/auth/session": { mode: "none", status: "disabled" },
  "/api/v1/runtime": {
    apiMajor: 1,
    capabilities: { mcp: true, memory: true, posture: "auto", steer: true, storageHealth: true },
    connection: "online",
    features: [],
    mock: false,
    sdkVersion: "0.9.2",
    source: "external",
    studioBuildId: "v0.9.3",
  },
  "/api/v1/settings/runtime": {
    buildId: "v0.0.40",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "Managed by the deployment.",
      routingConfiguration: false,
      routingConfigurationReason: "Managed by the deployment.",
    },
    models: [],
    modelsReason: "",
    modelsSupported: true,
    providerEndpoint: "",
    providers: [],
    serverImplementation: "mecated",
  },
  "/api/v1/storage/health": { supported: false },
  "/api/v1/user-memory": { items: [], reason: "", sha256: "", sizeBytes: "0", supported: true },
};

/** A window `width` CSS pixels wide, as matchMedia's min-width and max-width queries see it. */
function viewport(width: number) {
  vi.stubGlobal("matchMedia", (query: string) => {
    const min = /\(min-width:\s*([\d.]+)px\)/u.exec(query);
    const max = /\(max-width:\s*([\d.]+)px\)/u.exec(query);
    return {
      addEventListener: () => {},
      addListener: () => {},
      dispatchEvent: () => true,
      matches:
        (min !== null || max !== null) &&
        (min === null || width >= Number(min[1])) &&
        (max === null || width <= Number(max[1])),
      media: query,
      onchange: null,
      removeEventListener: () => {},
      removeListener: () => {},
    };
  });
}

async function load(path: string) {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
    ...stringSearchParams,
  });
  await router.load();
  return router;
}

/** Where a direct load lands; in a DOM environment the router follows redirects. */
async function landsOn(path: string): Promise<string> {
  return (await load(path)).state.location.href;
}

let host: HTMLDivElement | undefined;
let root: Root | undefined;

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  host?.remove();
  root = undefined;
  host = undefined;
});

/** The production route tree in the real workspace shell, over a faked BFF. */
async function mountStudio(path: string) {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const { pathname } = new URL(
      input instanceof Request ? input.url : String(input),
      window.location.href,
    );
    return Response.json(responses[pathname] ?? { items: [] });
  });
  apiClient.setConfig({ baseUrl: window.location.origin, fetch });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(getAuthSessionOptions().queryKey, { mode: "none", status: "disabled" });
  const router = createRouter({
    ...studioRouterOptions,
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
    ...stringSearchParams,
  });
  const element = document.createElement("div");
  document.body.append(element);
  host = element;
  root = createRoot(element);
  await act(async () => {
    root?.render(
      <QueryClientProvider client={client}>
        <AuthRecoveryContext.Provider
          value={{
            banner: {
              authenticated: true,
              publicStatus: { connection: "reachable", signInRequired: false },
              publicStatusFailed: false,
              sessionCheckFailed: false,
            },
            loginUrl: "/api/v1/auth/login?return_to=%2Fworkspace%2Fsettings",
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
  return { main: () => element.querySelector("main") ?? element, router };
}

/**
 * Whether breakpoint classes on the element or an ancestor hide it at this
 * width; happy-dom applies no CSS.
 */
function hiddenAt(element: Element, width: number): boolean {
  const phone = width < 500;
  const hides = phone ? "max-[499px]:hidden" : "min-[500px]:hidden";
  const reveals = phone
    ? /^max-\[499px\]:(block|flex|grid|inline|inline-flex)$/u
    : /^min-\[500px\]:(block|flex|grid|inline|inline-flex)$/u;
  for (let node: Element | null = element; node; node = node.parentElement) {
    const names = [...node.classList];
    if (node.hasAttribute("hidden") || names.includes(hides)) return true;
    if (names.includes("hidden") && !names.some((name) => reveals.test(name))) return true;
  }
  return false;
}

function visibleAt<T extends Element>(width: number, elements: Iterable<T>): T[] {
  return [...elements].filter((element) => !hiddenAt(element, width));
}

function nameOf(element: Element): string | undefined {
  return (element.getAttribute("aria-label") ?? element.textContent)?.trim();
}

/** Activates the control a person at this width would use to open a section, whichever it is. */
async function open(page: HTMLElement, width: number, label: string) {
  const option = visibleAt(width, page.querySelectorAll("option")).find(
    (item) => item.textContent?.trim() === label,
  );
  const select = option?.closest("select");
  if (option && select) {
    await act(async () => {
      select.value = option.value;
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    return;
  }
  const control = visibleAt(width, page.querySelectorAll<HTMLElement>("a, button")).find(
    (item) => nameOf(item) === label,
  );
  expect(control, `a control for ${label} at ${width}px`).toBeDefined();
  await act(async () => control?.click());
}

/**
 * The section a phone page announces: a visible section picker's value, or
 * else the page's visible level-one heading.
 */
function phoneAnnouncement(page: HTMLElement): string | undefined {
  const picker = visibleAt(PHONE, page.querySelectorAll("select"))[0];
  // happy-dom can leave `selectedOptions` stale; `selectedIndex` follows the value.
  if (picker) return picker.options[picker.selectedIndex]?.textContent?.trim();
  const headings = visibleAt(PHONE, page.querySelectorAll("h1"));
  expect(headings).toHaveLength(1);
  return headings[0]?.textContent?.trim();
}

describe("settings reachability pins", () => {
  it("keeps every section slug and legacy redirect", async () => {
    expect(settingsGroups.flatMap((group) => group.items.map((item) => item.value))).toEqual(
      sections.map(([slug]) => slug),
    );
    expect(settingsGroups.flatMap((group) => group.items.map((item) => item.label))).toEqual(
      sections.map(([, label]) => label),
    );
    for (const [slug] of sections) {
      expect(isSettingsSection(slug), slug).toBe(true);
      const router = await load(`/workspace/settings/${slug}`);
      expect(router.state.location.href, slug).toBe(`/workspace/settings/${slug}`);
      expect(router.state.matches.at(-1)?.routeId, slug).toBe("/workspace/settings_/$section");
    }
    for (const slug of ["help", "provider", "gateway", "chat", "notifications", "not-real"]) {
      expect(isSettingsSection(slug), slug).toBe(false);
      expect(await landsOn(`/workspace/settings/${slug}`), slug).toBe(
        "/workspace/settings/profile",
      );
    }
    expect(await landsOn("/workspace/settings/memory?item=team%2Fvoice")).toBe(
      "/workspace/memory?item=team%2Fvoice",
    );
    expect(await landsOn("/workspace/memory")).toBe("/workspace/settings/memory");
    expect(await landsOn("/workspace/memory?item=")).toBe("/workspace/settings/memory");
    expect(await landsOn("/workspace/provider")).toBe("/workspace/settings/providers");
  });

  it("opens Settings on You in a desktop window, replacing the bare URL", async () => {
    viewport(DESKTOP);
    const router = await load("/workspace/settings");
    expect(router.state.location.href).toBe("/workspace/settings/profile");
    expect(router.state.matches.at(-1)?.params).toMatchObject({ section: "profile" });
    expect(router.history.length).toBe(1);
  });

  it("reaches every section from the desktop nav and marks the current one", async () => {
    viewport(DESKTOP);
    const { main, router } = await mountStudio("/workspace/settings");
    await vi.waitFor(() =>
      expect(router.state.location.pathname).toBe("/workspace/settings/profile"),
    );
    for (const [slug, label] of sections) {
      await open(main(), DESKTOP, label);
      await vi.waitFor(() => {
        expect(router.state.location.pathname, label).toBe(`/workspace/settings/${slug}`);
        const current = visibleAt(DESKTOP, main().querySelectorAll('[aria-current="page"]'));
        expect(current.map(nameOf), label).toEqual([label]);
      });
    }
  });

  it("reaches every section on a phone from Settings and announces the current one", async () => {
    viewport(PHONE);
    const { main, router } = await mountStudio("/workspace/settings");
    for (const [slug, label] of sections) {
      await act(async () => router.navigate({ to: "/workspace/settings" }));
      await open(main(), PHONE, label);
      await vi.waitFor(() => {
        expect(router.state.location.pathname, label).toBe(`/workspace/settings/${slug}`);
        expect(phoneAnnouncement(main()), label).toBe(label);
      });
    }
  });
});
