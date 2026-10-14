// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRouter,
  RouterContextProvider,
  RouterProvider,
} from "@tanstack/react-router";
import { fireEvent } from "@testing-library/react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { studioRouterOptions } from "../../components/error-page/error-routes";
import { stringSearchParams } from "../../lib/search-params";
import { routeTree } from "../../routeTree.gen";
import { AuthRecoveryContext } from "../auth/auth-recovery-context";
import { SettingsIndex } from "./settings-index";

/**
 * The phone's section index and back bar (#2261), in the prototype's
 * grouping and look, and the desktop layout they leave unchanged.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const PHONE = 390;
const DESKTOP = 1280;

/** The prototype's groups, with Studio's slugs, labels, and the prototype's icon for each. */
const groups = [
  [
    "Preferences",
    [
      ["profile", "You", "lucide-user-round"],
      ["appearance", "Personalise", "lucide-palette"],
    ],
  ],
  [
    "Agent runtime",
    [
      ["agent", "Agent", "lucide-bot"],
      ["permissions", "Permissions", "lucide-shield-check"],
      ["providers", "Providers", "lucide-server"],
      ["models", "Models", "lucide-boxes"],
      ["mcp-tools", "MCP tools", "lucide-network"],
      ["storage", "Storage", "lucide-database"],
      ["memory", "Memory", "lucide-brain"],
      ["learning", "Learning", "lucide-graduation-cap"],
      ["diagnostics", "Diagnostics", "lucide-activity"],
    ],
  ],
  ["Experimental", [["labs", "Labs", "lucide-flask-conical"]]],
  ["Support", [["about", "About", "lucide-circle-help"]]],
] as const;

const sections = groups.flatMap(([, items]) =>
  items.map(([slug, label]) => [slug, label] as const),
);

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
    models: [
      {
        contextLimit: "128000",
        displayName: "Model One",
        id: "model-one",
        image: false,
        providerId: "openai",
        reasoning: false,
      },
    ],
    modelsReason: "",
    modelsSupported: true,
    providerEndpoint: "",
    providers: [],
    serverImplementation: "mecated",
  },
  "/api/v1/storage/health": { supported: false },
  "/api/v1/user-memory": { items: [], reason: "", sha256: "", sizeBytes: "0", supported: true },
};

/**
 * A window `width` CSS pixels wide, as matchMedia's min-width and max-width
 * queries see it. `resize` moves the window and tells the listeners.
 */
function viewport(width: number) {
  let current = width;
  const listeners = new Set<() => void>();
  vi.stubGlobal("matchMedia", (query: string) => {
    const min = /\(min-width:\s*([\d.]+)px\)/u.exec(query);
    const max = /\(max-width:\s*([\d.]+)px\)/u.exec(query);
    return {
      addEventListener: (_type: string, listener: () => void) => listeners.add(listener),
      addListener: () => {},
      dispatchEvent: () => true,
      matches:
        (min !== null || max !== null) &&
        (min === null || current >= Number(min[1])) &&
        (max === null || current <= Number(max[1])),
      media: query,
      onchange: null,
      removeEventListener: (_type: string, listener: () => void) => listeners.delete(listener),
      removeListener: () => {},
    };
  });
  return {
    async resize(next: number) {
      current = next;
      await act(async () => {
        for (const listener of [...listeners]) listener();
      });
    },
  };
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
  const main = () => element.querySelector("main") ?? element;
  return {
    back: () => main().querySelector<HTMLAnchorElement>('a[aria-label="Back to Settings"]'),
    headings: () => [...main().querySelectorAll("h1")].map((heading) => heading.textContent),
    index: () =>
      [...main().querySelectorAll('nav[aria-label="Settings sections"]')].find(
        (nav) => nav.querySelector("a") !== null,
      ) ?? null,
    main,
    router,
  };
}

describe("the phone section index", () => {
  it("lists every section in the prototype's groups, each with its icon", async () => {
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/settings"] }),
      routeTree,
    });
    const page = new DOMParser().parseFromString(
      renderToStaticMarkup(
        <QueryClientProvider client={new QueryClient()}>
          <RouterContextProvider router={router}>
            <SettingsIndex />
          </RouterContextProvider>
        </QueryClientProvider>,
      ),
      "text/html",
    ).body;
    expect(page.querySelector("h1")?.textContent).toBe("Settings");
    const nav = page.querySelector('nav[aria-label="Settings sections"]');
    // The same 500px edge as the section nav, so a window never shows both.
    expect(nav?.classList.contains("min-[500px]:hidden")).toBe(true);
    const lists = [...(nav?.querySelectorAll("ul") ?? [])];
    expect(lists).toHaveLength(groups.length);
    for (const [position, [title, items]] of groups.entries()) {
      const list = lists[position];
      // Each group's list is named by its visible title.
      const label = list?.getAttribute("aria-labelledby");
      expect(label ? page.querySelector(`[id="${label}"]`)?.textContent : null).toBe(title);
      const rows = [...(list?.querySelectorAll("a") ?? [])];
      expect(
        rows.map((row) => row.textContent),
        title,
      ).toEqual(items.map(([, name]) => name));
      const expected: ReadonlyArray<readonly [string, string, string]> = items;
      for (const [at, [slug, name, icon]] of expected.entries()) {
        const row = rows[at];
        expect(row?.getAttribute("href"), name).toBe(`/workspace/settings/${slug}`);
        const glyphs = row?.querySelectorAll('svg[aria-hidden="true"]');
        expect(glyphs?.[0]?.getAttribute("class"), name).toContain(icon);
        expect(glyphs?.[1]?.getAttribute("class"), name).toContain("lucide-chevron-right");
      }
    }
    expect(page.querySelector("select")).toBeNull();
  });

  it("opens a section from its row, and Back or the back bar returns to the index", async () => {
    viewport(PHONE);
    const studio = await mountStudio("/workspace/settings");
    expect(studio.router.state.location.pathname).toBe("/workspace/settings");
    expect(studio.headings()).toEqual(["Settings"]);
    expect(studio.back()).toBeNull();
    const openAgent = async () => {
      const row = studio.index()?.querySelector<HTMLAnchorElement>('a[href$="/agent"]');
      await act(async () => row?.click());
      await vi.waitFor(() => expect(studio.headings()).toEqual(["Agent"]));
      expect(studio.router.state.location.pathname).toBe("/workspace/settings/agent");
    };

    await openAgent();
    // The section nav stays hidden below the pivot, and the bar's heading replaces the title.
    const sectionNav = studio.main().querySelector('nav[aria-label="Settings sections"]');
    expect(sectionNav?.querySelector("button")).not.toBeNull();
    expect(sectionNav?.classList.contains("hidden")).toBe(true);
    expect(sectionNav?.classList.contains("min-[500px]:flex")).toBe(true);
    // The row added a history entry, so the browser's Back returns to the index.
    await act(async () => studio.router.history.back());
    await vi.waitFor(() => expect(studio.headings()).toEqual(["Settings"]));
    expect(studio.router.state.location.pathname).toBe("/workspace/settings");

    await openAgent();
    await act(async () => studio.back()?.click());
    await vi.waitFor(() =>
      expect(studio.router.state.location.pathname).toBe("/workspace/settings"),
    );
    expect(studio.headings()).toEqual(["Settings"]);
    expect(studio.index()?.querySelectorAll("a")).toHaveLength(sections.length);
  });

  it("returns to the index from a section opened directly", async () => {
    viewport(PHONE);
    const studio = await mountStudio("/workspace/settings/storage");
    expect(studio.headings()).toEqual(["Storage"]);
    expect(studio.back()?.getAttribute("href")).toBe("/workspace/settings");
    await act(async () => studio.back()?.click());
    await vi.waitFor(() => expect(studio.index()).not.toBeNull());
    expect(studio.router.state.location.pathname).toBe("/workspace/settings");
  });
});

describe("the desktop layout", () => {
  it("still opens Settings on You with its title and section nav, and no back bar", async () => {
    viewport(DESKTOP);
    const studio = await mountStudio("/workspace/settings");
    await vi.waitFor(() =>
      expect(studio.router.state.location.pathname).toBe("/workspace/settings/profile"),
    );
    expect(studio.router.history.length).toBe(1);
    expect(studio.headings()).toEqual(["Settings"]);
    const nav = studio.main().querySelector('nav[aria-label="Settings sections"]');
    expect(nav?.querySelectorAll("button")).toHaveLength(sections.length);
    expect(nav?.querySelector('[aria-current="page"]')?.textContent).toBe("You");
    expect(studio.back()).toBeNull();
    expect(studio.index()).toBeNull();
  });

  it("follows a window widened past the pivot from the index to You", async () => {
    const display = viewport(PHONE);
    const studio = await mountStudio("/workspace/settings");
    expect(studio.index()).not.toBeNull();
    await display.resize(DESKTOP);
    await vi.waitFor(() =>
      expect(studio.router.state.location.pathname).toBe("/workspace/settings/profile"),
    );
    // A replacement, so Back does not return to an index a desktop never shows.
    expect(studio.router.history.length).toBe(1);
    expect(studio.headings()).toEqual(["Settings"]);
  });

  it("keeps a section's state while the window crosses the pivot", async () => {
    const display = viewport(DESKTOP);
    const studio = await mountStudio("/workspace/settings/models");
    const filter = () =>
      studio.main().querySelector<HTMLInputElement>('input[aria-label="Filter models"]');
    await vi.waitFor(() => expect(filter()).not.toBeNull());
    const input = filter();
    await act(async () => {
      if (input) fireEvent.change(input, { target: { value: "one" } });
    });
    expect(filter()?.value).toBe("one");
    await display.resize(PHONE);
    expect(studio.headings()).toEqual(["Models"]);
    expect(filter()).toBe(input);
    expect(filter()?.value).toBe("one");
    await display.resize(DESKTOP);
    expect(studio.headings()).toEqual(["Settings"]);
    expect(filter()).toBe(input);
  });
});

describe("Settings without a native select", () => {
  it.each([
    ["a phone", PHONE],
    ["a desktop", DESKTOP],
  ])("has no select and one level-one heading on every page in %s window", async (_, width) => {
    viewport(width);
    const studio = await mountStudio("/workspace/settings");
    await vi.waitFor(() => expect(studio.headings()).toEqual(["Settings"]));
    expect(studio.main().querySelectorAll("select")).toHaveLength(0);
    for (const [slug, label] of sections) {
      await act(async () =>
        studio.router.navigate({
          params: { section: slug },
          search: { item: undefined },
          to: "/workspace/settings/$section",
        }),
      );
      await vi.waitFor(() =>
        expect(studio.headings(), slug).toEqual([width < 500 ? label : "Settings"]),
      );
      expect(studio.main().querySelectorAll("select"), slug).toHaveLength(0);
    }
  });
});
