// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type {
  GetProviderSettingsResponse,
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import {
  getProviderSettingsQueryKey,
  getRuntimeQueryKey,
  getRuntimeSettingsQueryKey,
  getStorageHealthQueryKey,
  listLearningProposalsQueryKey,
  listUserMemoryQueryKey,
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it } from "vitest";
import { modelPreferenceId } from "../../lib/model-preferences";
import { routeTree } from "../../routeTree.gen";
import { ProviderDetail } from "./provider-detail";
import { SettingsWorkspace } from "./settings-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const providerId = "team/openai";
const model = {
  contextLimit: "128000",
  displayName: "Model One",
  id: "model-one",
  image: true,
  providerId,
  reasoning: true,
};
const provider = {
  availableNotDefault: true,
  defaultModelAutoSelected: false,
  hint: "Ready through the gateway",
  id: providerId,
  modelCount: 1,
  state: "available",
};

function settings(overrides: Partial<GetRuntimeSettingsResponse> = {}): GetRuntimeSettingsResponse {
  return {
    buildId: "v0.0.22",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "Providers are managed by the deployment.",
      routingConfiguration: false,
      routingConfigurationReason: "Routing is managed by the deployment.",
    },
    models: [model],
    modelsReason: "",
    modelsSupported: true,
    providerEndpoint: "selector-free-value-must-not-appear",
    providers: [provider],
    serverImplementation: "mecated",
    ...overrides,
  };
}

function runtime(overrides: Partial<GetRuntimeResponse> = {}): GetRuntimeResponse {
  return {
    apiMajor: 1,
    capabilities: {
      agents: true,
      audio: false,
      bash: true,
      debugMcp: false,
      image: true,
      learnedSkills: true,
      learningProposals: true,
      manualCompaction: true,
      manualDream: { userModel: { decide: true, generate: true } },
      mcp: true,
      mcpConnectorStatus: false,
      memory: true,
      modelSelection: true,
      posture: "Ask",
      reflection: true,
      scheduling: false,
      sessionDebug: false,
      skills: true,
      slashCommands: true,
      soul: false,
      steer: true,
      storageCleanup: false,
      storageHealth: true,
      storageMigration: false,
      teams: false,
      userModel: true,
      workspaceEnrollment: false,
      worktrees: false,
    },
    connection: "online",
    features: [],
    mock: false,
    source: "external",
    ...overrides,
  };
}

function seededClient(
  runtimeResponse: GetRuntimeResponse = runtime(),
  settingsResponse: GetRuntimeSettingsResponse = settings(),
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(getRuntimeQueryKey(), runtimeResponse);
  client.setQueryData(getRuntimeSettingsQueryKey(), settingsResponse);
  return client;
}

async function renderSection(
  section: Parameters<typeof SettingsWorkspace>[0]["section"],
  client: QueryClient,
) {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [`/workspace/settings/${section}`] }),
    routeTree,
  });
  await router.load();
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <SettingsWorkspace section={section} />
      </RouterContextProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => window.localStorage.clear());

describe("settings facts", () => {
  it("shows a provider's BFF details as text and links to its exact ID", async () => {
    const client = seededClient();
    const list = await renderSection("providers", client);
    expect(list).toContain('href="/workspace/provider?providerId=team%2Fopenai"');
    expect(list).toContain("Ready");
    expect(list).not.toContain("selector-free-value-must-not-appear");

    const detail: GetProviderSettingsResponse = {
      displayEndpoint: "https://provider.example/v1",
      models: [model],
      provider,
    };
    client.setQueryData(getProviderSettingsQueryKey({ query: { providerId } }), detail);
    const router = createRouter({
      history: createMemoryHistory({
        initialEntries: ["/workspace/provider?providerId=team%2Fopenai"],
      }),
      routeTree,
    });
    await router.load();
    const page = renderToStaticMarkup(
      <QueryClientProvider client={client}>
        <RouterContextProvider router={router}>
          <ProviderDetail providerId={providerId} />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
    expect(page).toContain("Ready through the gateway");
    expect(page).toContain("1 model");
    expect(page).toContain("Model One");
    expect(page).toContain("https://provider.example/v1");
    expect(page).not.toContain('href="https://provider.example/v1"');
    expect(page).toContain('href="/workspace/settings/providers"');

    client.setQueryData(getProviderSettingsQueryKey({ query: { providerId } }), {
      ...detail,
      displayEndpoint: null,
    });
    const missingEndpoint = renderToStaticMarkup(
      <QueryClientProvider client={client}>
        <RouterContextProvider router={router}>
          <ProviderDetail providerId={providerId} />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
    expect(missingEndpoint).toContain("Not available");

    const unknownId = "unknown/provider";
    const unknownKey = getProviderSettingsQueryKey({ query: { providerId: unknownId } });
    client.setQueryData(unknownKey, detail);
    client
      .getQueryCache()
      .find({ queryKey: unknownKey })
      ?.setState({
        error: Object.assign(new Error("not found"), { code: "provider_not_found", status: 404 }),
        status: "error",
      });
    const unknown = renderToStaticMarkup(
      <QueryClientProvider client={client}>
        <RouterContextProvider router={router}>
          <ProviderDetail providerId={unknownId} />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
    expect(unknown).toContain("Provider not found");
    expect(unknown).not.toContain("https://provider.example/v1");
  });

  it("keeps model visibility personal and defaults managed", async () => {
    const client = seededClient();
    const page = await renderSection("models", client);
    for (const fact of ["model-one", "Model One", "team/openai", "128,000", "Images", "Reasoning"])
      expect(page).toContain(fact);
    expect(page).toContain("Default model");
    expect(page).toContain("Routing");
    expect(page).toContain("deployment");
    expect(page).not.toMatch(/<select[^>]*aria-label="Default model"/);
    expect(page).not.toContain("Save model");
    const noCapabilities = await renderSection(
      "models",
      seededClient(runtime(), settings({ models: [{ ...model, image: false, reasoning: false }] })),
    );
    expect(noCapabilities).toMatch(/Images<\/dt><dd>No<\/dd>/);
    expect(noCapabilities).toMatch(/Reasoning<\/dt><dd>No<\/dd>/);

    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/settings/models"] }),
      routeTree,
    });
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <RouterContextProvider router={router}>
            <SettingsWorkspace section="models" />
          </RouterContextProvider>
        </QueryClientProvider>,
      );
    });
    const visible = host.querySelector<HTMLButtonElement>(
      '[role="switch"][aria-label="Show Model One in model pickers"]',
    );
    expect(visible).not.toBeNull();
    await act(async () => visible?.click());
    expect(window.localStorage.getItem("studio.chat.models.disabled")).toBe(
      JSON.stringify([modelPreferenceId(model)]),
    );
    expect(window.localStorage.length).toBe(1);
    await act(async () => root.unmount());
    host.remove();
  });

  it("separates personal agent identity from managed behavior", async () => {
    const client = seededClient();
    const profile = await renderSection("profile", client);
    expect(profile).toContain("browser profile preferences");
    expect(profile).toContain("Your display name");
    const appearance = await renderSection("appearance", client);
    expect(appearance).toContain("browser appearance");
    const agent = await renderSection("agent", client);
    expect(agent).toContain("Agent name");
    expect(agent).toContain("Picture");
    expect(agent).toContain("browser");
    expect(agent).toContain("deployment");
    expect(agent).not.toContain("Change agent behavior");

    client.setQueryData(listLearningProposalsQueryKey({ query: { status: "staged" } }), {
      complete: true,
      items: [
        {
          body: "",
          createdAt: "",
          decisions: [],
          description: "A preference",
          evidenceCount: 0,
          id: "proposal-1",
          key: "pref",
          kind: "memory",
          learnedSkillId: "",
          projectScoped: false,
          promotionAvailable: true,
          promotionUnavailableReason: "",
          status: "staged",
          title: "A preference",
          triggers: [],
          updatedAt: "",
          value: "Stay concise",
          version: "1",
        },
      ],
      reason: "",
      supported: true,
    });
    const learning = await renderSection("learning", client);
    expect(learning).toContain("Learning configuration is managed by this deployment");
    expect(learning).toContain("Approve");
    expect(learning).toContain("Reject");
  });

  it("keeps memory consolidation on the settings list", async () => {
    const client = seededClient();
    client.setQueryData(listUserMemoryQueryKey(), {
      items: [{ description: "A preference", key: "team/voice" }],
      reason: "",
      sha256: "abc",
      sizeBytes: "42",
      supported: true,
    });
    const page = await renderSection("memory", client);
    expect(page).toContain("Consolidate memory");
    expect(page).toContain("Generate plan");
    expect(page).toContain('href="/workspace/memory?item=team%2Fvoice"');
    expect(page).toContain("deployment");
  });

  it("explains managed sections without write controls", async () => {
    const client = seededClient();
    client.setQueryData(getStorageHealthQueryKey(), {
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
    });
    const expectations = [
      ["permissions", "Ask", "deployment", "runtime"],
      ["mcp-tools", "MCP", "deployment", "runtime"],
      ["storage", "3", "deployment", "storage"],
      ["diagnostics", "mecated", "deployment", "runtime"],
      ["labs", "Labs", "deployment", "runtime"],
    ] as const;
    for (const [section, fact, owner, source] of expectations) {
      const page = await renderSection(section, client);
      expect(page).toContain(fact);
      expect(page).toContain(owner);
      expect(page).toContain(source);
      expect(page).not.toMatch(
        /<(?:input|select|button)[^>]*(?:posture|MCP setup|Clean up|logs|usage|demo)/i,
      );
    }
  });

  it("distinguishes offline unsupported and failed settings states", async () => {
    const offline = await renderSection(
      "providers",
      seededClient(runtime({ connection: "offline" })),
    );
    expect(offline).toContain("Offline");
    expect(offline).not.toContain("Ready through the gateway");
    expect(offline).not.toContain("team/openai");

    const unsupported = await renderSection(
      "models",
      seededClient(
        runtime(),
        settings({
          models: [],
          modelsReason: "Model selection is disabled.",
          modelsSupported: false,
          providers: [],
        }),
      ),
    );
    expect(unsupported).toContain("Model selection is disabled.");
    expect(unsupported).not.toContain('role="switch"');

    const empty = await renderSection(
      "models",
      seededClient(runtime(), settings({ models: [], providers: [] })),
    );
    expect(empty).toContain("No models");
    expect(empty).not.toContain("Model selection is disabled.");

    const loading = await renderSection(
      "providers",
      new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    );
    expect(loading).toContain("Loading");

    const failureClient = seededClient();
    failureClient
      .getQueryCache()
      .find({ queryKey: getRuntimeSettingsQueryKey() })
      ?.setState({
        error: new Error("BFF read failed"),
        status: "error",
      });
    const failed = await renderSection("providers", failureClient);
    expect(failed).toContain("could not be loaded");
    expect(failed).not.toContain("BFF read failed");
    expect(failed).not.toContain("team/openai");

    const originalOnline = Object.getOwnPropertyDescriptor(window.navigator, "onLine");
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/settings/providers"] }),
      routeTree,
    });
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      Object.defineProperty(window.navigator, "onLine", { configurable: true, value: false });
      await act(async () => {
        root.render(
          <QueryClientProvider client={seededClient()}>
            <RouterContextProvider router={router}>
              <SettingsWorkspace section="providers" />
            </RouterContextProvider>
          </QueryClientProvider>,
        );
      });
      expect(host.textContent).toContain("Offline");
      expect(host.textContent).not.toContain("Ready through the gateway");
    } finally {
      await act(async () => root.unmount());
      host.remove();
      if (originalOnline) Object.defineProperty(window.navigator, "onLine", originalOnline);
      else Reflect.deleteProperty(window.navigator, "onLine");
    }
  });
});
