// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { getAuthSessionQueryKey, getPublicStatusQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
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
        const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
        client.setQueryData(getAuthSessionQueryKey(), { mode: "oidc", status: "anonymous" });
        client.setQueryData(getPublicStatusQueryKey(), {
          connection: "reachable",
          signInRequired: true,
        });
        const open = vi.spyOn(window, "open").mockReturnValue(null);
        const host = document.createElement("div");
        document.body.append(host);
        const root = createRoot(host);
        try {
          await act(async () => {
            root.render(
              <QueryClientProvider client={client}>
                <AuthGate>
                  <p>Private detail facts</p>
                </AuthGate>
              </QueryClientProvider>,
            );
          });
          const signIn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((button) =>
            button.textContent?.includes("Sign in to Mecatl"),
          );
          expect(signIn).toBeDefined();
          expect(host.textContent).not.toContain("Private detail facts");

          await act(async () => signIn?.click());
          expect(open).toHaveBeenCalledTimes(1);
          const popupUrl = new URL(String(open.mock.calls[0]?.[0]), window.location.origin);
          expect(popupUrl.pathname).toBe("/api/v1/auth/login");
          expect(popupUrl.searchParams.get("flow")).toBe("popup");
          expect(popupUrl.searchParams.get("return_to")).toBe(path);

          const manual = host.querySelector<HTMLAnchorElement>('a[href^="/api/v1/auth/login?"]');
          expect(manual).not.toBeNull();
          const manualUrl = new URL(manual?.href ?? "", window.location.origin);
          expect(manualUrl.searchParams.get("flow")).toBeNull();
          expect(manualUrl.searchParams.get("return_to")).toBe(path);
          expect(`${window.location.pathname}${window.location.search}`).toBe(path);
        } finally {
          await act(async () => root.unmount());
          host.remove();
          vi.restoreAllMocks();
        }
      }
    } finally {
      window.history.replaceState({}, "", original);
    }
  });
});
