// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type {
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey, getRuntimeSettingsQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { ShortcutReference } from "../shortcuts/shortcut-reference";
import { AvatarPicker } from "./avatar-picker";
import { MemoryFactDetail } from "./memory-settings";
import { ProviderDetail } from "./provider-detail";
import { SettingsWorkspace } from "./settings-workspace";

function render(path: string, element: React.ReactNode, client = new QueryClient()): HTMLElement {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  });
  const host = document.createElement("div");
  host.innerHTML = renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>{element}</RouterContextProvider>
    </QueryClientProvider>,
  );
  return host;
}

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
  providers: [],
  serverImplementation: "mecated",
};

describe("settings responsive accessibility", () => {
  it("identifies the selected section in both navigation modes and sizes each target for touch", () => {
    const page = render("/workspace/settings/models", <SettingsWorkspace section="models" />);
    const selector = page.querySelector<HTMLSelectElement>("#settings-section");
    const navigation = page.querySelector<HTMLElement>('nav[aria-label="Settings sections"]');
    const buttons = navigation?.querySelectorAll<HTMLButtonElement>("button");
    const returnLink = [...page.querySelectorAll<HTMLAnchorElement>("a")].find((link) =>
      /← Chats/u.test(link.textContent ?? ""),
    );
    expect(returnLink?.getAttribute("href")).toBe("/workspace/chat");
    expect(returnLink?.classList.contains("min-h-11")).toBe(true);
    expect(returnLink?.className).toContain("focus-visible:");
    expect(selector?.querySelector('option[value="models"]')?.hasAttribute("selected")).toBe(true);
    expect(selector?.classList.contains("min-h-11")).toBe(true);
    expect(selector?.className).toContain("focus-visible:");
    expect(buttons?.length).toBe(13);
    expect(navigation?.querySelectorAll('button[aria-current="page"]')).toHaveLength(1);
    expect(navigation?.querySelector('button[aria-current="page"]')?.textContent).toBe("Models");
    for (const button of buttons ?? []) {
      expect(button.classList.contains("min-h-11"), button.textContent ?? "").toBe(true);
      expect(button.className, button.textContent ?? "").toContain("focus-visible:");
    }
  });

  it("offers a named, focusable 44px return link on both detail pages and shortcuts", () => {
    const details = [
      [
        "/workspace/provider?providerId=team%2Fopenai",
        <ProviderDetail key="provider" providerId="team/openai" />,
        /providers/i,
      ],
      [
        "/workspace/memory?item=team%2Fvoice",
        <MemoryFactDetail key="memory" memoryKey="team/voice" />,
        /memory/i,
      ],
      ["/workspace/shortcuts", <ShortcutReference key="shortcuts" />, /settings/i],
    ] as const;
    for (const [path, component, name] of details) {
      const page = render(path, component);
      const returnLink = [...page.querySelectorAll<HTMLAnchorElement>("a")].find((link) =>
        name.test(link.textContent ?? ""),
      );
      expect(returnLink, path).toBeDefined();
      expect(returnLink?.classList.contains("min-h-11"), path).toBe(true);
      expect(returnLink?.className, path).toContain("focus-visible:");
    }
  });

  it("keeps ordinary managed fact prose on word boundaries in narrow cards", () => {
    const client = new QueryClient();
    client.setQueryData(getRuntimeQueryKey(), {
      capabilities: {},
      connection: "online",
    } as GetRuntimeResponse);
    client.setQueryData(getRuntimeSettingsQueryKey(), settingsFixture);
    const page = render(
      "/workspace/settings/models",
      <SettingsWorkspace section="models" />,
      client,
    );
    const defaultModel = [...page.querySelectorAll("dt")].find((item) =>
      item.textContent?.includes("Default model"),
    );
    const prose = defaultModel?.nextElementSibling;
    expect(prose?.textContent).toContain("Managed by deployment");
    expect(prose?.classList.contains("break-words")).toBe(true);
    expect(prose?.classList.contains("break-all")).toBe(false);
  });

  it("gives About actions and the model visibility label a 44px touch area", () => {
    const client = new QueryClient();
    client.setQueryData(getRuntimeQueryKey(), {
      capabilities: {},
      connection: "online",
      source: "local",
    } as GetRuntimeResponse);
    client.setQueryData(getRuntimeSettingsQueryKey(), {
      ...settingsFixture,
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
    } satisfies GetRuntimeSettingsResponse);

    const about = render(
      "/workspace/settings/about",
      <SettingsWorkspace section="about" />,
      client,
    );
    for (const name of [
      "Copy support summary",
      "Documentation",
      "Report a problem",
      "Keyboard shortcuts",
    ]) {
      const action = [
        ...about.querySelectorAll<HTMLAnchorElement | HTMLButtonElement>("a, button"),
      ].find((element) => element.textContent?.includes(name));
      expect(action, name).toBeDefined();
      expect(action?.classList.contains("min-h-11"), name).toBe(true);
    }

    const models = render(
      "/workspace/settings/models",
      <SettingsWorkspace section="models" />,
      client,
    );
    const label = models.querySelector<HTMLLabelElement>('label[for^="visible-model-"]');
    const control = models.querySelector<HTMLButtonElement>('button[data-slot="switch"]');
    expect(label?.textContent).toContain("Visible");
    expect(label?.htmlFor).toBe(control?.id);
    expect(label?.classList.contains("min-h-11")).toBe(true);
    expect(control?.className).toContain("focus-visible:");
  });

  it("sizes personal preferences and Learning controls for touch", () => {
    const client = new QueryClient();
    client.setQueryData(getRuntimeQueryKey(), {
      capabilities: { reflection: true },
      connection: "online",
    } as GetRuntimeResponse);
    for (const section of ["profile", "agent"] as const) {
      const page = render(
        `/workspace/settings/${section}`,
        <SettingsWorkspace section={section} />,
        client,
      );
      const input = page.querySelector<HTMLInputElement>(
        section === "profile"
          ? 'input[aria-label="Your display name"]'
          : 'input[aria-label="Agent name"]',
      );
      const upload = [...page.querySelectorAll<HTMLButtonElement>("button")].find((button) =>
        button.textContent?.includes("Upload picture"),
      );
      expect(input?.classList.contains("min-h-11"), section).toBe(true);
      expect(upload?.classList.contains("min-h-11"), section).toBe(true);
    }

    const appearance = render(
      "/workspace/settings/appearance",
      <SettingsWorkspace section="appearance" />,
      client,
    );
    for (const name of ["Theme", "Decrease interface scale", "Increase interface scale"]) {
      const selector =
        name === "Theme" ? 'button[aria-label^="Theme: "]' : `button[aria-label="${name}"]`;
      const button = appearance.querySelector<HTMLButtonElement>(selector);
      expect(button?.classList.contains("min-h-11"), name).toBe(true);
    }
    expect(
      appearance.querySelector('label[for="show-tool-activity"]')?.classList.contains("min-h-11"),
    ).toBe(true);

    const learning = render(
      "/workspace/settings/learning",
      <SettingsWorkspace section="learning" />,
      client,
    );
    for (const name of ["Pending", "Promoted", "Rejected"]) {
      const button = [...learning.querySelectorAll<HTMLButtonElement>("button")].find(
        (item) => item.textContent === name,
      );
      expect(button?.classList.contains("min-h-11"), name).toBe(true);
    }
    expect(
      learning
        .querySelector('select[aria-label="Completed session"]')
        ?.classList.contains("min-h-11"),
    ).toBe(true);
  });

  it("exposes a 44px picture removal target without hover on mobile", () => {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(
      <AvatarPicker
        alt="Ava"
        avatarUrl="data:image/png;base64,aA=="
        fallback={<span />}
        onChange={() => {}}
      />,
    );
    const remove = host.querySelector<HTMLButtonElement>('button[aria-label="Remove Ava picture"]');
    expect(remove).not.toBeNull();
    expect(remove?.classList.contains("size-11")).toBe(true);
    expect(remove?.classList.contains("opacity-100")).toBe(true);
    expect(remove?.className).toContain("focus-visible:");
  });
});
