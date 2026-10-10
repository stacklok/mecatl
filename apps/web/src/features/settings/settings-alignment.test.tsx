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
import { afterEach, describe, expect, it } from "vitest";
import { clearUserScopedStorage } from "@/lib/account-storage";
import { routeTree } from "@/routeTree.gen";
import { DraftGreeting, STARTER_PROMPTS } from "../chat/draft-greeting";
import { type SettingsSection, settingsGroups } from "./settings-sections";
import { SettingsWorkspace } from "./settings-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const runtimeFixture = {
  apiMajor: 1,
  capabilities: { mcp: true, posture: "auto", steer: true, storageHealth: true },
  connection: "online",
  deployment: "Team deployment",
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
  models: [],
  modelsReason: "",
  modelsSupported: true,
  providerEndpoint: "",
  providers: [
    {
      availableNotDefault: true,
      defaultModelAutoSelected: false,
      hint: "Add a key where the agent runs.",
      id: "openai",
      modelCount: 2,
      state: "unauthorized",
    },
  ],
  serverImplementation: "mecated",
};

function client({
  runtime = runtimeFixture,
  settings = settingsFixture,
  storage = { supported: false } as Record<string, unknown> | null,
  session = { mode: "none", status: "disabled" } as Record<string, unknown>,
}: {
  runtime?: GetRuntimeResponse | null;
  settings?: GetRuntimeSettingsResponse | null;
  storage?: Record<string, unknown> | null;
  session?: Record<string, unknown>;
} = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  if (runtime) queryClient.setQueryData(getRuntimeQueryKey(), runtime);
  if (settings) queryClient.setQueryData(getRuntimeSettingsQueryKey(), settings);
  if (storage) queryClient.setQueryData(getStorageHealthQueryKey(), storage);
  queryClient.setQueryData(getAuthSessionQueryKey(), session);
  return queryClient;
}

function failed(queryClient: QueryClient, queryKey: readonly unknown[]) {
  queryClient
    .getQueryCache()
    .find({ queryKey })
    ?.setState({ error: new Error("raw failure"), status: "error" });
  return queryClient;
}

async function render(section: SettingsSection, queryClient = client()) {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [`/workspace/settings/${section}`] }),
    routeTree,
  });
  await router.load();
  const markup = renderToStaticMarkup(
    <QueryClientProvider client={queryClient}>
      <RouterContextProvider router={router}>
        <SettingsWorkspace section={section} />
      </RouterContextProvider>
    </QueryClientProvider>,
  );
  return new DOMParser().parseFromString(markup, "text/html").body;
}

function roleOf(page: HTMLElement, text: string): string | null | undefined {
  return [...page.querySelectorAll("[role]")]
    .find((element) => element.textContent?.includes(text))
    ?.getAttribute("role");
}

afterEach(() => {
  clearUserScopedStorage();
  window.localStorage.clear();
});

describe("settings navigation in the prototype's grammar", () => {
  it("groups the sections with an icon each and the prototype's labels", async () => {
    expect(settingsGroups.map((group) => group.title)).toEqual([
      "Preferences",
      "Agent runtime",
      "Experimental",
      "Support",
    ]);
    for (const group of settingsGroups)
      for (const item of group.items) expect(typeof item.icon, item.value).toBe("object");

    const page = await render("appearance");
    const nav = page.querySelector('nav[aria-label="Settings sections"]');
    expect([...(nav?.querySelectorAll("p") ?? [])].map((title) => title.textContent)).toEqual([
      "Preferences",
      "Agent runtime",
      "Experimental",
      "Support",
    ]);
    const labels = [...(nav?.querySelectorAll("button") ?? [])].map((item) => item.textContent);
    expect(labels.slice(0, 2)).toEqual(["You", "Personalise"]);
    expect(nav?.querySelector('[aria-current="page"]')?.textContent).toBe("Personalise");
    expect(nav?.className).toContain("min-[500px]:flex");

    // The phone picker carries the current section's icon, decorative only.
    const picker = page.querySelector("select#settings-section")?.parentElement;
    const icons = picker?.querySelectorAll('svg[aria-hidden="true"]');
    expect(icons?.length).toBe(2);
    expect(icons?.[0]?.getAttribute("class")).toContain("lucide-palette");
    expect(
      page.querySelector("select#settings-section")?.closest(".min-\\[500px\\]\\:hidden"),
    ).not.toBeNull();
  });

  it("pivots at 500px instead of sm: or md: across the sections it owns", async () => {
    const owned = [
      "profile",
      "appearance",
      "agent",
      "permissions",
      "providers",
      "models",
      "mcp-tools",
      "storage",
      "diagnostics",
      "labs",
      "about",
    ] as const;
    for (const section of owned) {
      const page = await render(section);
      // Shared primitives (data-slot) keep their own upstream breakpoints.
      const classes = [...page.querySelectorAll("[class]:not([data-slot])")].flatMap((element) =>
        (element.getAttribute("class") ?? "").split(/\s+/u),
      );
      expect(
        classes.filter((name) => /^(sm|md):/u.test(name)),
        section,
      ).toEqual([]);
    }
  });
});

describe("settings live regions", () => {
  it("announces loading and notices politely and failures as alerts", async () => {
    for (const section of ["permissions", "providers", "about", "storage", "memory"] as const) {
      const loading = await render(section, client({ runtime: null }));
      expect(roleOf(loading, "Loading current runtime settings…"), section).toBe("status");
      const offline = await render(
        section,
        client({ runtime: { ...runtimeFixture, connection: "offline" } }),
      );
      expect(roleOf(offline, "Offline. Connect to the agent"), section).toBe("status");
      const failure = await render(section, failed(client(), getRuntimeQueryKey()));
      expect(roleOf(failure, "Current runtime settings could not be loaded"), section).toBe(
        "alert",
      );
    }
    const inventoryLoading = await render("models", client({ settings: null }));
    expect(roleOf(inventoryLoading, "Loading settings…")).toBe("status");
    const inventoryFailure = await render("models", failed(client(), getRuntimeSettingsQueryKey()));
    expect(roleOf(inventoryFailure, "Current settings could not be loaded")).toBe("alert");
    const incompatible = await render(
      "permissions",
      client({ runtime: { ...runtimeFixture, connection: "incompatible" } }),
    );
    expect(roleOf(incompatible, "incompatible")).toBe("alert");
  });

  it("announces the storage and sign-in states", async () => {
    const storageLoading = await render("storage", client({ storage: null }));
    expect(roleOf(storageLoading, "Loading storage details…")).toBe("status");
    const storageUnsupported = await render("storage");
    expect(roleOf(storageUnsupported, "This agent cannot report on its storage.")).toBe("status");
    const storageFailure = await render(
      "storage",
      failed(client({ storage: { supported: true } }), getStorageHealthQueryKey()),
    );
    expect(roleOf(storageFailure, "raw failure")).toBe("alert");
    expect(storageFailure.querySelector("section > h2")?.textContent).toBe("Storage");

    const signIn = client();
    signIn.removeQueries({ queryKey: getAuthSessionQueryKey() });
    const checking = await render("profile", signIn);
    expect(roleOf(checking, "Checking sign-in details…")).toBe("status");
  });
});

describe("settings section pages", () => {
  it("shows the posture as a read-only Safety level", async () => {
    const auto = await render("permissions");
    expect(auto.textContent).toContain("Safety level");
    expect(auto.textContent).toContain("Auto");
    expect(auto.querySelector('[data-slot="badge"]')?.className).toContain("text-warning");
    for (const [posture, label] of [
      ["yolo", "Yolo"],
      ["trusted", "Trusted"],
      ["strict", "Strict"],
      ["Ask", "Ask"],
      ["", "Not reported"],
    ] as const) {
      const page = await render(
        "permissions",
        client({
          runtime: {
            ...runtimeFixture,
            capabilities: { ...runtimeFixture.capabilities, posture },
          },
        }),
      );
      expect(page.querySelector('[data-slot="badge"]')?.textContent, posture).toBe(label);
    }
  });

  it("lists providers as rows with their state and a link to the details", async () => {
    const page = await render("providers");
    const row = page.querySelector<HTMLAnchorElement>(
      'a[href="/workspace/provider?providerId=openai"]',
    );
    expect(row?.textContent).toContain("Openai");
    expect(row?.textContent).toContain("Sign-in needed · 2 models");
    expect(row?.textContent).toContain("Add a key where the agent runs.");
    expect(row?.textContent).toContain("View provider details");
    expect(row?.className).toContain("min-h-11");
    expect(row?.className).toContain("focus-visible:");
  });

  it("splits About into a Studio card and an agent card", async () => {
    const page = await render("about");
    const cards = [...page.querySelectorAll("section")];
    expect(cards.map((card) => card.querySelector("h2")?.textContent)).toEqual([
      "About Mecatl Studio",
      "About the agent",
    ]);
    const [studio, agent] = cards;
    expect(studio?.textContent).toContain("v0.9.3");
    expect(studio?.textContent).toContain("0.9.2");
    expect(studio?.querySelector('a[href="/workspace/shortcuts"]')).not.toBeNull();
    expect(studio?.textContent).not.toContain("v0.0.40");
    expect(agent?.textContent).toContain("v0.0.40");
    expect(agent?.textContent).toContain("mecated");
    expect(agent?.textContent).toContain("Team deployment");
    expect(agent?.textContent).toContain("Copy support summary");
    expect(agent?.textContent).not.toContain("v0.9.3");

    const signedIn = await render(
      "about",
      client({ session: { account: "a", mode: "oidc", status: "authenticated" } }),
    );
    const signInCard = [...signedIn.querySelectorAll("section")].at(-1);
    expect(signInCard?.querySelector("h2")?.textContent).toBe("Sign-in");
    expect(signInCard?.textContent).toContain("Sign out");
  });

  it("shares the daemon facts between Diagnostics and About", async () => {
    const page = await render("diagnostics");
    const facts = [...page.querySelectorAll("dt")].map((term) => term.textContent);
    expect(facts).toEqual([
      "Daemon build",
      "Daemon implementation",
      "Runtime source",
      "Connection",
    ]);
    expect(page.textContent).toContain("Logs and usage are managed by this deployment");
  });
});

describe("starter prompts preference", () => {
  it("is on by default and hides the chat's starter prompts when turned off", async () => {
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/settings/appearance"] }),
      routeTree,
    });
    await router.load();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => {
        root.render(
          <QueryClientProvider client={client()}>
            <RouterContextProvider router={router}>
              <SettingsWorkspace section="appearance" />
            </RouterContextProvider>
          </QueryClientProvider>,
        );
      });
      const toggle = host.querySelector<HTMLButtonElement>(
        '[role="switch"][aria-label="Starter prompts"]',
      );
      expect(toggle?.getAttribute("aria-checked")).toBe("true");
      expect(host.querySelector('label[for="starter-prompts"]')?.className).toContain("min-h-11");
      expect(window.localStorage.getItem("studio.profile.starter-prompts")).toBeNull();
      await act(async () => toggle?.click());
      expect(toggle?.getAttribute("aria-checked")).toBe("false");
      expect(window.localStorage.getItem("studio.profile.starter-prompts")).toBe("hidden");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }

    const greeting = renderToStaticMarkup(<DraftGreeting onPickSeed={() => {}} />);
    expect(greeting).toContain("What can I help you with?");
    for (const prompt of STARTER_PROMPTS) expect(greeting).not.toContain(prompt);

    // Account scoped: a sign-out or account change brings the prompts back.
    clearUserScopedStorage();
    const reset = renderToStaticMarkup(<DraftGreeting onPickSeed={() => {}} />);
    for (const prompt of STARTER_PROMPTS) expect(reset).toContain(prompt);
  });
});
