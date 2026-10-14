// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Provider names come from the BFF (#2260): each surface shows the display
 * name the BFF sends ("OpenAI", not a title-cased "Openai"), and the model
 * filter matches a provider by that name or by its ID.
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
import type { ComposerModelOption } from "./chat/chat-composer";
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

function provider(id: string, displayName: string) {
  return {
    availableNotDefault: false,
    defaultModelAutoSelected: false,
    displayName,
    hint: "",
    id,
    modelCount: 1,
    state: "ok",
  };
}

const openAiModel = {
  contextLimit: "400000",
  displayName: "GPT-5",
  id: "gpt-5",
  image: true,
  providerDisplayName: "OpenAI",
  providerId: "openai",
  reasoning: true,
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
  models: [openAiModel],
  modelsReason: "",
  modelsSupported: true,
  providerEndpoint: "",
  providers: [provider("openai", "OpenAI"), provider("openrouter", "OpenRouter")],
  serverImplementation: "mecated",
};

const openAiDetail: GetProviderSettingsResponse = {
  displayEndpoint: null,
  models: [openAiModel],
  provider: provider("openai", "OpenAI"),
};

/** Neutral model labels, so a filter can only match through the provider. */
const models: ComposerModelOption[] = [
  {
    id: "a",
    image: false,
    label: "Model A",
    providerId: "openai",
    providerLabel: "OpenAI",
    reasoning: false,
  },
  {
    id: "b",
    image: false,
    label: "Model B",
    providerId: "openai-codex",
    providerLabel: "OpenAI Codex",
    reasoning: false,
  },
  {
    id: "c",
    image: false,
    label: "Model C",
    providerId: "anthropic",
    providerLabel: "Anthropic",
    reasoning: false,
  },
];

function seededClient({ detail = true }: { detail?: boolean } = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(getRuntimeQueryKey(), runtimeFixture);
  client.setQueryData(getRuntimeSettingsQueryKey(), settingsFixture);
  client.setQueryData(getStorageHealthQueryKey(), { supported: false });
  client.setQueryData(getAuthSessionQueryKey(), { mode: "none", status: "disabled" });
  if (detail) {
    client.setQueryData(
      getProviderSettingsQueryKey({ query: { providerId: "openai" } }),
      openAiDetail,
    );
  }
  return client;
}

async function staticPage(path: string, element: ReactNode, client = seededClient()) {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  });
  await router.load();
  const markup = renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>{element}</RouterContextProvider>
    </QueryClientProvider>,
  );
  return new DOMParser().parseFromString(markup, "text/html").body;
}

function renderComposer() {
  render(
    <ChatComposer
      configuration={{ mode: "default", reasoningEffort: "default" }}
      models={models}
      onConfigurationChange={vi.fn()}
      onSend={vi.fn().mockResolvedValue(true)}
    />,
  );
}

function groupLabels(scope: HTMLElement = document.body) {
  return within(scope)
    .queryAllByText(/^(Anthropic|OpenAI|OpenAI Codex)$/)
    .map((label) => label.textContent);
}

function visibleModels(role: "button" | "menuitem", scope: HTMLElement = document.body) {
  return within(scope)
    .queryAllByRole(role, { name: /^Model [ABC]$/ })
    .map((item) => item.textContent);
}

describe("provider names from the BFF", () => {
  it("names each Settings provider row with the BFF's display name", async () => {
    const page = await staticPage(
      "/workspace/settings/providers",
      <SettingsWorkspace section="providers" />,
    );
    const name = (id: string) =>
      page
        .querySelector(`a[href="/workspace/provider?providerId=${id}"]`)
        ?.querySelector(".font-medium")?.textContent;
    expect(name("openai")).toBe("OpenAI");
    expect(name("openrouter")).toBe("OpenRouter");
    expect(page.textContent).not.toContain("Openai");
    expect(page.textContent).not.toContain("Openrouter");
  });

  it("titles the provider page with the BFF's name and keeps the raw ID as a fact", async () => {
    const page = await staticPage(
      "/workspace/provider?providerId=openai",
      <ProviderDetail providerId="openai" />,
    );
    const heading = page.querySelector("h1");
    expect(heading?.textContent).toBe("OpenAI");
    // A name wraps between words; only an unbroken ID needs to break anywhere.
    expect(heading?.classList.contains("break-words")).toBe(true);
    expect(heading?.classList.contains("break-all")).toBe(false);
    const facts = new Map(
      [...page.querySelectorAll("dt")].map((term) => [
        term.textContent,
        term.nextElementSibling?.textContent,
      ]),
    );
    expect(facts.get("Provider ID")).toBe("openai");
  });

  it("titles the provider page with the requested ID until the BFF describes it", async () => {
    const page = await staticPage(
      "/workspace/provider?providerId=openai",
      <ProviderDetail providerId="openai" />,
      seededClient({ detail: false }),
    );
    expect(page.querySelector("h1")?.textContent).toBe("openai");
    expect(page.textContent).toContain("Loading provider details");
  });

  it("groups the desktop model menu by provider name and filters by name or ID", async () => {
    const user = userEvent.setup();
    renderComposer();
    await user.click(screen.getByRole("button", { name: /^Model Default/ }));
    await user.hover(screen.getByRole("menuitem", { name: /^Model/ }));
    await screen.findByRole("menuitem", { name: "Model A" });
    // Groups keep Studio's order, by provider ID.
    expect(groupLabels()).toEqual(["Anthropic", "OpenAI Codex", "OpenAI"]);

    // The submenu focuses its filter; a click would leave the submenu and close it.
    const filter = screen.getByRole("textbox", { name: "Filter models" });
    expect(document.activeElement).toBe(filter);
    await user.type(filter, "OpenAI", { skipClick: true });
    expect(visibleModels("menuitem")).toEqual(["Model B", "Model A"]);
    expect(groupLabels()).toEqual(["OpenAI Codex", "OpenAI"]);
    // The name has a space where the ID has a hyphen, so each query reaches one of them.
    await user.clear(filter);
    await user.type(filter, "OpenAI Codex", { skipClick: true });
    expect(visibleModels("menuitem")).toEqual(["Model B"]);
    await user.clear(filter);
    await user.type(filter, "openai-codex", { skipClick: true });
    expect(visibleModels("menuitem")).toEqual(["Model B"]);
    await user.clear(filter);
    await user.type(filter, "anthropic", { skipClick: true });
    expect(visibleModels("menuitem")).toEqual(["Model C"]);
  });

  it("groups the phone model sheet by provider name and searches by name or ID", async () => {
    const user = userEvent.setup();
    renderComposer();
    await user.click(screen.getByRole("button", { name: "Composer options" }));
    const sheet = screen.getByRole("dialog", { name: "Composer options" });
    await user.click(within(sheet).getByRole("button", { name: /^Model/ }));
    expect(groupLabels(sheet)).toEqual(["Anthropic", "OpenAI Codex", "OpenAI"]);

    const search = within(sheet).getByRole("textbox", { name: "Search models" });
    await user.type(search, "OpenAI Codex");
    expect(visibleModels("button", sheet)).toEqual(["Model B"]);
    expect(groupLabels(sheet)).toEqual(["OpenAI Codex"]);
    await user.clear(search);
    await user.type(search, "openai-codex");
    expect(visibleModels("button", sheet)).toEqual(["Model B"]);
    await user.clear(search);
    await user.type(search, "openai");
    expect(visibleModels("button", sheet)).toEqual(["Model B", "Model A"]);
  });

  it("labels a live chat's context usage with the BFF's provider name", () => {
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
      model: {
        contextWindow: "400000",
        id: "gpt-5",
        providerDisplayName: "OpenAI",
        providerId: "openai",
        reasoningEffort: "default",
      },
      state: "idle",
      usage: {
        cacheReadTokens: "0",
        cacheWriteTokens: "0",
        inputTokens: "40000",
        outputTokens: "0",
        reasoningTokens: "0",
      },
    };
    const props = {
      compacting: false,
      detail,
      disabled: false,
      forking: false,
      modePending: false,
      onCompact: vi.fn(),
    };
    const pills = render(<SessionUsageControls {...props} variant="pills" />);
    expect(pills.container.querySelector('[title^="gpt-5"]')?.getAttribute("title")).toMatch(
      /^gpt-5 · OpenAI · /,
    );
    cleanup();

    render(<SessionUsageControls {...props} variant="sheet" />);
    expect(screen.getByText("gpt-5 · OpenAI")).toBeTruthy();
  });
});
