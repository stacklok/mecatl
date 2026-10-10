// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type {
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import {
  getAuthSessionQueryKey,
  getRuntimeQueryKey,
  getRuntimeSettingsQueryKey,
  getStorageHealthQueryKey,
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { stringSearchParams } from "../../lib/search-params";
import { routeTree } from "../../routeTree.gen";
import { DraftGreeting, STARTER_PROMPTS } from "../chat/draft-greeting";
import { isSettingsSection, type SettingsSection, settingsGroups } from "./settings-sections";
import { SettingsWorkspace } from "./settings-workspace";

/**
 * Pins the Settings behaviour the prototype alignment (#2206) must keep:
 * Studio's slugs, its section order, its legacy redirects, both navigation
 * modes, the runtime state wording that masks cached facts, the read-only
 * contract, and the chat greeting's starter prompts with nothing stored.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const slugs = [
  "profile",
  "appearance",
  "agent",
  "permissions",
  "providers",
  "models",
  "mcp-tools",
  "storage",
  "memory",
  "learning",
  "diagnostics",
  "labs",
  "about",
] as const;

const runtimeFixture = {
  apiMajor: 1,
  capabilities: { mcp: true, posture: "Ask", steer: true, storageHealth: true },
  connection: "online",
  deployment: "Pinned deployment",
  features: [],
  mock: false,
  sdkVersion: "0.9.2",
  source: "external",
  studioBuildId: "v0.9.3",
} as unknown as GetRuntimeResponse;

const settingsFixture: GetRuntimeSettingsResponse = {
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
      image: true,
      providerId: "team/openai",
      reasoning: true,
    },
  ],
  modelsReason: "",
  modelsSupported: true,
  providerEndpoint: "",
  providers: [
    {
      availableNotDefault: true,
      defaultModelAutoSelected: false,
      hint: "Ready through the gateway",
      id: "team/openai",
      modelCount: 1,
      state: "available",
    },
  ],
  serverImplementation: "mecated",
};

const storageFixture = {
  activeJob: false,
  available: true,
  childCount: "1",
  corruptCount: "0",
  currentBytes: "2048",
  lastFailure: false,
  mainCount: "2",
  reclaimableBytes: "1024",
  scheduledCount: "0",
  sessionCount: "3",
  supported: true,
  unknownCount: "0",
};

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

function client({
  runtime = runtimeFixture,
  settings = settingsFixture,
}: {
  runtime?: GetRuntimeResponse | null;
  settings?: GetRuntimeSettingsResponse | null;
} = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  if (runtime) queryClient.setQueryData(getRuntimeQueryKey(), runtime);
  if (settings) queryClient.setQueryData(getRuntimeSettingsQueryKey(), settings);
  queryClient.setQueryData(getStorageHealthQueryKey(), storageFixture);
  queryClient.setQueryData(getAuthSessionQueryKey(), { mode: "none", status: "disabled" });
  return queryClient;
}

function failed(queryClient: QueryClient, queryKey: readonly unknown[]) {
  queryClient
    .getQueryCache()
    .find({ queryKey })
    ?.setState({ error: new Error("raw BFF failure"), status: "error" });
  return queryClient;
}

async function render(section: SettingsSection, queryClient: QueryClient) {
  const router = await load(`/workspace/settings/${section}`);
  const markup = renderToStaticMarkup(
    <QueryClientProvider client={queryClient}>
      <RouterContextProvider router={router}>
        <SettingsWorkspace section={section} />
      </RouterContextProvider>
    </QueryClientProvider>,
  );
  return new DOMParser().parseFromString(markup, "text/html").body;
}

afterEach(() => {
  window.localStorage.clear();
  vi.restoreAllMocks();
});

describe("settings alignment pins", () => {
  it("keeps Studio's slugs, their order, and the legacy redirects", async () => {
    expect(settingsGroups.flatMap((group) => group.items.map((item) => item.value))).toEqual([
      ...slugs,
    ]);
    for (const slug of slugs) {
      expect(isSettingsSection(slug), slug).toBe(true);
      const router = await load(`/workspace/settings/${slug}`);
      expect(router.state.location.href, slug).toBe(`/workspace/settings/${slug}`);
      expect(router.state.matches.at(-1)?.routeId, slug).toBe("/workspace/settings_/$section");
    }
    expect(await landsOn("/workspace/settings")).toBe("/workspace/settings/profile");
    // The prototype's own slugs are not Studio routes: they fall back like any unknown slug.
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
    expect(await landsOn("/workspace/provider")).toBe("/workspace/settings/providers");
  });

  it("keeps both navigation modes over the same thirteen sections", async () => {
    const page = await render("storage", client());
    const nav = page.querySelector('nav[aria-label="Settings sections"]');
    const select = page.querySelector<HTMLSelectElement>("select#settings-section");
    expect(nav?.querySelectorAll("button")).toHaveLength(slugs.length);
    expect(nav?.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
    expect(nav?.querySelector('[aria-current="page"]')?.textContent).toBe("Storage");
    expect([...(select?.querySelectorAll("option") ?? [])].map((option) => option.value)).toEqual([
      ...slugs,
    ]);
    expect(select?.querySelector("option[selected]")?.getAttribute("value")).toBe("storage");
    expect(page.querySelector('label[for="settings-section"]')?.textContent).toBe(
      "Settings section",
    );

    const onSectionChange = vi.fn();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const router = await load("/workspace/settings/storage");
    try {
      await act(async () => {
        root.render(
          <QueryClientProvider client={client()}>
            <RouterContextProvider router={router}>
              <SettingsWorkspace onSectionChange={onSectionChange} section="storage" />
            </RouterContextProvider>
          </QueryClientProvider>,
        );
      });
      const mobile = host.querySelector<HTMLSelectElement>("select#settings-section");
      await act(async () => {
        if (mobile) mobile.value = "about";
        mobile?.dispatchEvent(new Event("change", { bubbles: true }));
      });
      expect(onSectionChange).toHaveBeenLastCalledWith("about");
      const permissions = [
        ...host.querySelectorAll<HTMLButtonElement>('nav[aria-label="Settings sections"] button'),
      ].find((button) => button.textContent === "Permissions");
      await act(async () => permissions?.click());
      expect(onSectionChange).toHaveBeenLastCalledWith("permissions");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("masks deployment facts behind the same runtime state wording", async () => {
    const runtimeSections = [
      "agent",
      "permissions",
      "providers",
      "models",
      "mcp-tools",
      "storage",
      "memory",
      "learning",
      "diagnostics",
      "labs",
      "about",
    ] as const;
    for (const section of runtimeSections) {
      const offline = await render(
        section,
        client({ runtime: { ...runtimeFixture, connection: "offline" } }),
      );
      expect(offline.textContent, section).toContain(
        "Offline. Connect to the agent to read current deployment settings.",
      );
      const loading = await render(section, client({ runtime: null }));
      expect(loading.textContent, section).toContain("Loading current runtime settings…");
      const failure = await render(section, failed(client(), getRuntimeQueryKey()));
      expect(failure.textContent, section).toContain(
        "Current runtime settings could not be loaded. Check the connection and try again.",
      );
      for (const page of [offline, loading, failure]) {
        expect(page.textContent, section).not.toContain("raw BFF failure");
        expect(page.textContent, section).not.toContain("Ready through the gateway");
        expect(page.textContent, section).not.toContain("v0.0.40");
      }
    }
    for (const section of ["providers", "models", "about", "diagnostics"] as const) {
      const inventoryLoading = await render(section, client({ settings: null }));
      expect(inventoryLoading.textContent, section).toContain("Loading settings…");
      const inventoryFailure = await render(
        section,
        failed(client(), getRuntimeSettingsQueryKey()),
      );
      expect(inventoryFailure.textContent, section).toContain(
        "Current settings could not be loaded. Check the connection and try again.",
      );
      expect(inventoryFailure.textContent, section).not.toContain("raw BFF failure");
    }
  });

  it("stays read-only for every deployment-managed section", async () => {
    const allowedInputs: Partial<Record<SettingsSection, string[]>> = {
      agent: ["Agent name"],
      models: ["Filter models"],
      profile: ["Your display name"],
    };
    for (const section of slugs) {
      const page = await render(section, client());
      // Hidden file pickers and switch inputs are personal, browser-local preferences.
      const inputs = [
        ...page.querySelectorAll("input:not([type=file]):not([type=checkbox]), textarea"),
      ];
      expect(
        inputs.map((input) => input.getAttribute("aria-label")),
        section,
      ).toEqual(allowedInputs[section] ?? []);
      expect(
        [...page.querySelectorAll("select")].map((select) => select.id),
        section,
      ).toEqual(["settings-section"]);
      const writes = [...page.querySelectorAll("button, a")].filter((control) =>
        /\bsave\b|\bapply\b|clean up|change posture|connect gateway|sign in to the gateway/i.test(
          `${control.getAttribute("aria-label") ?? ""} ${control.textContent ?? ""}`,
        ),
      );
      expect(writes, section).toHaveLength(0);
    }
  });

  it("keeps the deployment facts each section shows today", async () => {
    const expectations: Partial<Record<SettingsSection, string[]>> = {
      about: ["v0.9.3", "0.9.2", "v0.0.40", "mecated", "external", "online", "Pinned deployment"],
      diagnostics: ["v0.0.40", "mecated", "external", "online"],
      models: ["Model One", "model-one", "team/openai", "128,000", "Default model", "Routing"],
      permissions: ["Ask"],
      providers: ["Ready", "1 model"],
      storage: ["Healthy", "Saved chats and runs", "3", "2.0 KB", "1.0 KB"],
    };
    for (const [section, facts] of Object.entries(expectations)) {
      const page = await render(section as SettingsSection, client());
      for (const fact of facts) expect(page.textContent, `${section}: ${fact}`).toContain(fact);
    }
    const providers = await render("providers", client());
    expect(
      providers.querySelector('a[href="/workspace/provider?providerId=team%2Fopenai"]'),
    ).not.toBeNull();
    const about = await render("about", client());
    expect(about.querySelector('a[href="/workspace/shortcuts"]')).not.toBeNull();
    expect(
      about.querySelector('a[href="https://github.com/stacklok/mecatl/issues"]'),
    ).not.toBeNull();
    expect(
      about.querySelector('a[href="https://mecatl.dev/docs/building/deployment/studio"]'),
    ).not.toBeNull();
  });

  it("offers the starter prompts on a new chat when nothing is stored", async () => {
    const onPickSeed = vi.fn();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<DraftGreeting onPickSeed={onPickSeed} />));
      expect(host.querySelector("h2, h1")?.textContent).toBe("What can I help you with?");
      const chips = [...host.querySelectorAll<HTMLButtonElement>("button")];
      expect(chips.map((chip) => chip.textContent)).toEqual([...STARTER_PROMPTS]);
      await act(async () => chips[1]?.click());
      expect(onPickSeed).toHaveBeenCalledWith(STARTER_PROMPTS[1]);
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
});
