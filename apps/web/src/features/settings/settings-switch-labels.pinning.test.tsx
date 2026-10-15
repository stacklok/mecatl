// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type {
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey, getRuntimeSettingsQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "@/lib/account-storage";
import { routeTree } from "@/routeTree.gen";
import type { SettingsSection } from "./settings-sections";
import { SettingsWorkspace } from "./settings-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

/**
 * Pins the three labelled switches in Appearance (Starter prompts, Show tools)
 * and on a model card (Visible): the label is a `<label for>` the switch, the
 * switch keeps its accessible name, clicking the label's visible area toggles
 * the switch once, and the label text keeps its weight and line height.
 */

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
      providerDisplayName: "OpenAI",
      providerId: "openai",
      reasoning: true,
    },
  ],
  modelsReason: "",
  modelsSupported: true,
  providerEndpoint: "",
  providers: [],
  serverImplementation: "mecated",
};

const runtimeFixture = {
  capabilities: { steer: true },
  connection: "online",
  source: "local",
} as unknown as GetRuntimeResponse;

async function mount(section: SettingsSection) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(getRuntimeQueryKey(), runtimeFixture);
  client.setQueryData(getRuntimeSettingsQueryKey(), settingsFixture);
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
    if (path === "/api/v1/runtime") return Response.json(runtimeFixture);
    if (path === "/api/v1/settings/runtime") return Response.json(settingsFixture);
    throw new Error(`Unexpected BFF read: ${path}`);
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [`/workspace/settings/${section}`] }),
    routeTree,
  });
  await router.load();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <RouterContextProvider router={router}>
          <SettingsWorkspace section={section} />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
  });
  return {
    host,
    unmount: async () => {
      await act(async () => root.unmount());
      host.remove();
    },
  };
}

function labelled(host: HTMLElement, switchName: string) {
  const control = host.querySelector<HTMLButtonElement>(
    `[role="switch"][aria-label="${switchName}"]`,
  );
  expect(control, switchName).not.toBeNull();
  const label = host.querySelector<HTMLLabelElement>(`label[for="${control?.id}"]`);
  expect(label, switchName).not.toBeNull();
  return { control: control as HTMLButtonElement, label: label as HTMLLabelElement };
}

afterEach(() => {
  vi.restoreAllMocks();
  clearUserScopedStorage(window.localStorage);
  window.localStorage.clear();
});

describe("settings switch labels", () => {
  it("labels Starter prompts with a touch-sized label that toggles the switch once", async () => {
    const page = await mount("appearance");
    try {
      const { control, label } = labelled(page.host, "Starter prompts");
      expect(label.contains(control)).toBe(true);
      expect(label.htmlFor).toBe("starter-prompts");
      expect(label.textContent).toBe("");
      for (const name of ["flex", "min-h-11", "min-w-11", "items-center", "justify-end"]) {
        expect(label.classList.contains(name), name).toBe(true);
      }
      expect(control.getAttribute("aria-checked")).toBe("true");
      await act(async () => label.click());
      expect(control.getAttribute("aria-checked")).toBe("false");
      await act(async () => control.click());
      expect(control.getAttribute("aria-checked")).toBe("true");
    } finally {
      await page.unmount();
    }
  });

  it("labels Show tools with its eye icon and regular-weight text", async () => {
    const page = await mount("appearance");
    try {
      const { control, label } = labelled(page.host, "Show tool activity");
      expect(label.contains(control)).toBe(true);
      expect(label.textContent).toBe("Show tools");
      expect(label.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
      for (const name of ["min-h-11", "w-44", "justify-between", "gap-2", "text-sm"]) {
        expect(label.classList.contains(name), name).toBe(true);
      }
      expect(label.classList.contains("font-medium")).toBe(false);
      expect(label.classList.contains("leading-none")).toBe(false);
      const before = control.getAttribute("aria-checked");
      const text = [...label.querySelectorAll("span")].find((span) =>
        span.textContent?.includes("Show tools"),
      );
      await act(async () => text?.click());
      expect(control.getAttribute("aria-checked")).not.toBe(before);
    } finally {
      await page.unmount();
    }
  });

  it("labels a model card's Visible switch in small, regular-weight muted text", async () => {
    const page = await mount("models");
    try {
      const { control, label } = labelled(page.host, "Show Model One in model pickers");
      expect(label.contains(control)).toBe(true);
      expect(label.htmlFor).toBe("visible-model-openai-model-one");
      expect(label.textContent).toBe("Visible");
      for (const name of ["min-h-11", "gap-2", "text-xs", "text-muted-foreground"]) {
        expect(label.classList.contains(name), name).toBe(true);
      }
      for (const name of ["font-medium", "leading-none", "text-sm"]) {
        expect(label.classList.contains(name), name).toBe(false);
      }
      expect(control.getAttribute("aria-checked")).toBe("true");
      await act(async () => label.click());
      expect(control.getAttribute("aria-checked")).toBe("false");
      expect(window.localStorage.getItem("studio.chat.models.disabled")).toContain("model-one");
    } finally {
      await page.unmount();
    }
  });
});
