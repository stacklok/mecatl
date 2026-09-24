// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { getAuthSessionQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { stringSearchParams } from "../../lib/search-params";
import { SettingsSectionPage } from "../../routes/workspace.settings_.$section";
import { routeTree } from "../../routeTree.gen";
import { AuthGate } from "../auth/auth-gate";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

async function load(path: string) {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
    ...stringSearchParams,
  });
  await router.load();
  return router;
}

describe("settings route interactions", () => {
  it("desktop section buttons navigate through the route handler", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      Response.json({ capabilities: {}, connection: "online" }),
    );
    const router = await load("/workspace/settings/appearance");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => {
        root.render(
          <QueryClientProvider client={client}>
            <RouterContextProvider router={router}>
              <SettingsSectionPage />
            </RouterContextProvider>
          </QueryClientProvider>,
        );
      });
      const button = [
        ...host.querySelectorAll<HTMLButtonElement>('nav[aria-label="Settings sections"] button'),
      ].find((item) => item.textContent === "Permissions");
      expect(button).toBeDefined();
      await act(async () => button?.click());
      expect(router.state.location.pathname).toBe("/workspace/settings/permissions");
      expect(
        host.querySelector('nav[aria-label="Settings sections"] button[aria-current="page"]')
          ?.textContent,
      ).toBe("Permissions");
    } finally {
      await act(async () => root.unmount());
      host.remove();
      vi.restoreAllMocks();
    }
  });

  it("keeps slash-containing detail URLs through the anonymous auth gate", async () => {
    const original = window.location.href;
    try {
      for (const path of [
        "/workspace/memory?item=team%2Fvoice",
        "/workspace/provider?providerId=team%2Fopenai",
      ]) {
        const router = await load(path);
        window.history.replaceState({}, "", router.state.location.href);
        const client = new QueryClient();
        client.setQueryData(getAuthSessionQueryKey(), { mode: "oidc", status: "anonymous" });
        const markup = renderToStaticMarkup(
          <QueryClientProvider client={client}>
            <AuthGate>
              <p>Private detail facts</p>
            </AuthGate>
          </QueryClientProvider>,
        );
        const document = new DOMParser().parseFromString(markup, "text/html");
        const signIn = document.querySelector<HTMLAnchorElement>('a[href^="/api/v1/auth/login?"]');
        expect(signIn).not.toBeNull();
        expect(
          new URL(signIn?.href ?? "", window.location.origin).searchParams.get("return_to"),
        ).toBe(path);
        expect(markup).not.toContain("Private detail facts");
      }
    } finally {
      window.history.replaceState({}, "", original);
    }
  });
});
