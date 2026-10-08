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
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { SettingsWorkspace } from "./settings-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const runtime = {
  apiMajor: 1,
  capabilities: { steer: true },
  connection: "online",
  deployment: "Production workspace",
  features: ["unsafe-should-not-copy"],
  mock: false,
  sdkVersion: "0.9.2",
  source: "external",
  studioBuildId: "v0.9.3",
} as GetRuntimeResponse;
const settings = {
  buildId: "v0.9.1-3-gabc",
  management: {},
  models: [],
  modelsReason: "",
  modelsSupported: false,
  providerEndpoint: "unsafe-should-not-copy",
  providers: [],
  serverImplementation: "mecak8s",
} as unknown as GetRuntimeSettingsResponse;

async function mountAbout(
  runtimeResponse: GetRuntimeResponse,
  settingsResponse: GetRuntimeSettingsResponse,
) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
    if (path === "/api/v1/runtime") return Response.json(runtimeResponse);
    if (path === "/api/v1/settings/runtime") return Response.json(settingsResponse);
    throw new Error(`Unexpected BFF read: ${path}`);
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(getRuntimeQueryKey(), runtimeResponse);
  client.setQueryData(getRuntimeSettingsQueryKey(), settingsResponse);
  client.setQueryData(getAuthSessionQueryKey(), {
    account: "alice@example.com",
    mode: "oidc",
    status: "authenticated",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/settings/about"] }),
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
          <SettingsWorkspace section="about" />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
  });
  return { host, root };
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("About", () => {
  it("shows version facts from authenticated BFF responses", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const { host, root } = await mountAbout(runtime, settings);
    expect(host.textContent).toContain("Studio build");
    expect(host.textContent).toContain("v0.9.3");
    expect(host.textContent).toContain("SDK version");
    expect(host.textContent).toContain("0.9.2");
    expect(host.textContent).toContain("Daemon build");
    expect(host.textContent).toContain("v0.9.1-3-gabc");
    expect(host.textContent).toContain("mecak8s");
    expect(host.textContent).toContain("external");
    expect(host.textContent).toContain("online");
    expect(host.textContent).toContain("Production workspace");
    expect(host.textContent).not.toContain("unsafe-should-not-copy");

    expect(host.querySelector('a[href="/workspace/shortcuts"]')).not.toBeNull();
    expect(host.querySelector('a[href*="github.com/stacklok/mecatl/issues"]')).not.toBeNull();
    expect(host.querySelector('a[href*="mecatl.dev/docs/"]')).not.toBeNull();
    expect(host.textContent).toContain("Sign out");

    const copy = [...host.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("Copy support summary"),
    );
    expect(copy).toBeDefined();
    await act(async () => copy?.click());
    expect(writeText).toHaveBeenCalledWith(
      [
        "Studio build: v0.9.3",
        "SDK version: 0.9.2",
        "Daemon build: v0.9.1-3-gabc",
        "Daemon implementation: mecak8s",
        "Runtime source: external",
        "Connection: online",
        "Deployment: Production workspace",
      ].join("\n"),
    );
    await act(async () => root.unmount());
    host.remove();
  });

  it("labels absent build and version facts as Not reported", async () => {
    const { host, root } = await mountAbout(
      { ...runtime, deployment: undefined, sdkVersion: undefined, studioBuildId: undefined },
      { ...settings, buildId: "", serverImplementation: "" },
    );
    expect(host.textContent?.match(/Not reported/g)?.length).toBeGreaterThanOrEqual(5);
    await act(async () => root.unmount());
    host.remove();
  });
});
