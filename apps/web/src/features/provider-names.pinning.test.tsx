// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins how Studio names a provider whose ID has no display name of its own,
 * such as an operator-defined gateway (#2260). Each surface title-cases the ID
 * word by word, and the model filter matches that name. Provider names now come
 * from the BFF, which falls back to the same title-cased ID, so an unmapped
 * provider must look and filter exactly as before. Each fixture therefore also
 * carries the name the BFF sends for that ID.
 */

import type { SessionDetailResponse } from "@mecatl-studio/contracts";
import type {
  GetProviderSettingsResponse,
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import {
  getAuthSessionQueryKey,
  getProviderSettingsQueryKey,
  getRuntimeQueryKey,
  getRuntimeSettingsQueryKey,
  getStorageHealthQueryKey,
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../lib/account-storage";
import { routeTree } from "../routeTree.gen";
import { ChatComposer } from "./chat/chat-composer";
import { SessionUsageControls } from "./chat/chat-session-controls";
import { ProviderDetail } from "./settings/provider-detail";
import { SettingsWorkspace } from "./settings/settings-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

/** An operator-defined provider: no surface knows a name for this ID. */
const gatewayId = "team-gateway";
/** How every surface names it: the ID title-cased, which is also the BFF's fallback. */
const gatewayName = "Team Gateway";

// Each fixture is bound to a name before use, so the BFF's display-name fields
// type-check whether or not the generated contract declares them yet.
const gatewayProvider = {
  availableNotDefault: false,
  defaultModelAutoSelected: true,
  displayName: gatewayName,
  hint: "",
  id: gatewayId,
  modelCount: 1,
  state: "ok",
};
const gatewayModel = {
  contextLimit: "64000",
  displayName: "Gateway Model",
  id: "gateway-model",
  image: false,
  providerDisplayName: gatewayName,
  providerId: gatewayId,
  reasoning: false,
};
const gatewayOption = {
  id: "gateway-model",
  image: false,
  label: "Gateway Model",
  providerId: gatewayId,
  providerLabel: gatewayName,
  reasoning: false,
};
/** A second unmapped provider, so a filter has something to exclude. */
const acmeOption = {
  id: "acme-model",
  image: false,
  label: "Acme Model",
  providerId: "acme-llm",
  providerLabel: "Acme Llm",
  reasoning: false,
};
const gatewaySessionModel = {
  contextWindow: "64000",
  id: "gateway-model",
  providerDisplayName: gatewayName,
  providerId: gatewayId,
  reasoningEffort: "default" as const,
};

const runtimeFixture = {
  apiMajor: 1,
  capabilities: { modelSelection: true, posture: "auto", storageHealth: true },
  connection: "online",
  features: [],
  mock: false,
  source: "external",
} as unknown as GetRuntimeResponse;

const settingsFixture: GetRuntimeSettingsResponse = {
  buildId: "v0.0.48",
  management: {
    providerConfiguration: false,
    providerConfigurationReason: "Managed by the deployment.",
    routingConfiguration: false,
    routingConfigurationReason: "Managed by the deployment.",
  },
  models: [gatewayModel],
  modelsReason: "",
  modelsSupported: true,
  providerEndpoint: "",
  providers: [gatewayProvider],
  serverImplementation: "mecated",
};

const providerFixture: GetProviderSettingsResponse = {
  displayEndpoint: null,
  models: [gatewayModel],
  provider: gatewayProvider,
};

function seededClient() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(getRuntimeQueryKey(), runtimeFixture);
  client.setQueryData(getRuntimeSettingsQueryKey(), settingsFixture);
  client.setQueryData(getStorageHealthQueryKey(), { supported: false });
  client.setQueryData(getAuthSessionQueryKey(), { mode: "none", status: "disabled" });
  client.setQueryData(
    getProviderSettingsQueryKey({ query: { providerId: gatewayId } }),
    providerFixture,
  );
  return client;
}

async function staticPage(path: string, element: ReactNode) {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  });
  await router.load();
  const markup = renderToStaticMarkup(
    <QueryClientProvider client={seededClient()}>
      <RouterContextProvider router={router}>{element}</RouterContextProvider>
    </QueryClientProvider>,
  );
  return new DOMParser().parseFromString(markup, "text/html").body;
}

function renderComposer() {
  render(
    <ChatComposer
      configuration={{ mode: "default", reasoningEffort: "default" }}
      models={[gatewayOption, acmeOption]}
      onConfigurationChange={vi.fn()}
      onSend={vi.fn().mockResolvedValue(true)}
    />,
  );
}

function usageControls(variant: "pills" | "sheet") {
  const detail: SessionDetailResponse = {
    capabilities: {
      audio: false,
      builtInShell: false,
      executionFiles: false,
      image: false,
      manualCompaction: true,
      modelSelection: true,
    },
    id: "chat-1",
    kind: "main",
    mode: "default",
    model: gatewaySessionModel,
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "16000",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  };
  return render(
    <SessionUsageControls
      compacting={false}
      detail={detail}
      disabled={false}
      forking={false}
      modePending={false}
      onCompact={vi.fn()}
      variant={variant}
    />,
  );
}

describe("an unmapped provider keeps its title-cased name", () => {
  it("names the Settings provider row by the title-cased ID", async () => {
    const page = await staticPage(
      "/workspace/settings/providers",
      <SettingsWorkspace section="providers" />,
    );
    const row = page.querySelector(`a[href="/workspace/provider?providerId=${gatewayId}"]`);
    expect(row?.querySelector(".font-medium")?.textContent).toBe(gatewayName);
    expect(row?.textContent).toContain("Ready · 1 model");
  });

  it("keeps the provider page's raw ID fact and its model list", async () => {
    const page = await staticPage(
      `/workspace/provider?providerId=${gatewayId}`,
      <ProviderDetail providerId={gatewayId} />,
    );
    const facts = new Map(
      [...page.querySelectorAll("dt")].map((term) => [
        term.textContent,
        term.nextElementSibling?.textContent,
      ]),
    );
    expect(facts.get("Provider ID")).toBe(gatewayId);
    expect(page.querySelector("h2")?.textContent).toBe("Provider status");
    expect([...page.querySelectorAll("li p")].map((line) => line.textContent)).toEqual([
      "Gateway Model",
      "gateway-model",
    ]);
    expect(page.querySelector('a[href="/workspace/settings/providers"]')?.textContent).toContain(
      "Providers",
    );
  });

  it("groups and filters the desktop model menu by the title-cased ID", async () => {
    const user = userEvent.setup();
    renderComposer();
    await user.click(screen.getByRole("button", { name: /^Model Default/ }));
    await user.hover(screen.getByRole("menuitem", { name: /^Model/ }));
    expect(await screen.findByText(gatewayName)).toBeTruthy();
    expect(screen.getByText("Acme Llm")).toBeTruthy();

    // The submenu focuses its filter; a click would leave the submenu and close it.
    const filter = screen.getByRole("textbox", { name: "Filter models" });
    expect(document.activeElement).toBe(filter);
    await user.type(filter, "team gateway", { skipClick: true });
    expect(screen.getByRole("menuitem", { name: "Gateway Model" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: "Acme Model" })).toBeNull();
    await user.clear(filter);
    await user.type(filter, "llm", { skipClick: true });
    expect(screen.getByRole("menuitem", { name: "Acme Model" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: "Gateway Model" })).toBeNull();
    expect(screen.queryByText(gatewayName)).toBeNull();
    await user.clear(filter);
    await user.type(filter, "zzz", { skipClick: true });
    expect(screen.getByText("No models match")).toBeTruthy();
  });

  it("groups and searches the phone model sheet by the title-cased ID", async () => {
    const user = userEvent.setup();
    renderComposer();
    await user.click(screen.getByRole("button", { name: "Composer options" }));
    const sheet = screen.getByRole("dialog", { name: "Composer options" });
    await user.click(within(sheet).getByRole("button", { name: /^Model/ }));
    expect(within(sheet).getByText(gatewayName)).toBeTruthy();
    expect(within(sheet).getByText("Acme Llm")).toBeTruthy();

    const search = within(sheet).getByRole("textbox", { name: "Search models" });
    await user.type(search, "team gateway");
    expect(within(sheet).getByRole("button", { name: "Gateway Model" })).toBeTruthy();
    expect(within(sheet).queryByRole("button", { name: "Acme Model" })).toBeNull();
    expect(within(sheet).queryByText("Acme Llm")).toBeNull();
  });

  it("labels a live chat's context usage with the model ID and the title-cased ID", () => {
    const pills = usageControls("pills");
    expect(
      pills.container.querySelector('[title^="gateway-model"]')?.getAttribute("title"),
    ).toMatch(/^gateway-model · Team Gateway · /);
    cleanup();

    usageControls("sheet");
    expect(screen.getByText("gateway-model · Team Gateway")).toBeTruthy();
  });
});
