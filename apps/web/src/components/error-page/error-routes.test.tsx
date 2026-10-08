// @vitest-environment happy-dom
// SPDX-License-Identifier: Apache-2.0

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { getAuthSessionOptions, getRuntimeSettingsOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthRecoveryContext } from "../../features/auth/auth-recovery-context";
import { routeTree } from "../../routeTree.gen";
import { RootErrorBoundary, studioRouterOptions } from "./error-routes";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const privateError = "https://deployment.internal/run?token=SECRET_SENTINEL";
const initialApiConfig = apiClient.getConfig();
const chatApi = { broken: false };
let container: HTMLDivElement;
let reactRoot: Root;

async function mount(element: React.ReactNode) {
  container = document.createElement("div");
  document.body.append(container);
  reactRoot = createRoot(container);
  await act(async () => reactRoot.render(element));
}

afterEach(async () => {
  if (reactRoot) await act(async () => reactRoot.unmount());
  container?.remove();
  chatApi.broken = false;
  vi.restoreAllMocks();
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
});

async function mountProductionRoute(path: string) {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const pathname = new URL(
      input instanceof Request ? input.url : String(input),
      window.location.href,
    ).pathname;
    if (pathname === "/api/v1/runtime")
      return Response.json({
        capabilities: { image: false, posture: "managed" },
        connection: "online",
      });
    // A model list without `models` makes the real ChatWorkspace throw while
    // rendering; the sentinel rides along in the payload the failure came from.
    if (pathname === "/api/v1/settings/runtime")
      return Response.json(chatApi.broken ? { detail: privateError } : { models: [] });
    return Response.json({ items: [] });
  });
  apiClient.setConfig({ baseUrl: window.location.origin, fetch });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(getAuthSessionOptions().queryKey, { mode: "none", status: "disabled" });
  const router = createRouter({
    ...studioRouterOptions,
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  });
  await mount(
    <QueryClientProvider client={client}>
      <AuthRecoveryContext.Provider
        value={{
          banner: {
            authenticated: true,
            publicStatus: { connection: "reachable", signInRequired: false },
            publicStatusFailed: false,
            sessionCheckFailed: false,
          },
          loginUrl: "/api/v1/auth/login?return_to=%2Fworkspace%2Fchat",
          phase: "ready",
          popupIssue: null,
          retrySession: () => {},
          startPopupLogin: () => {},
        }}
      >
        <RouterProvider router={router} />
      </AuthRecoveryContext.Provider>
    </QueryClientProvider>,
  );
  return { client, router };
}

describe("production Studio routes", () => {
  it("opens a known route from the branded unknown URL using the real workspace shell", async () => {
    const { router } = await mountProductionRoute("/workspace/missing");
    expect(container.querySelector("h1")?.textContent).toBe("Page not found");
    expect(container.querySelectorAll("h1")).toHaveLength(1);
    expect(container.textContent).toContain("Mecatl Studio");
    const home = Array.from(
      container.querySelectorAll<HTMLAnchorElement>('a[href="/workspace/chat"]'),
    ).find((link) => link.textContent?.includes("Go to Chats"));
    expect(home).toBeDefined();

    await act(async () => home?.click());
    expect(router.state.location.pathname).toBe("/workspace/chat");
    expect(container.querySelector("h1")?.textContent).toBe("New chat");
    expect(container.querySelector("[data-shell-global-status]")).not.toBeNull();
    expect(container.querySelector('nav[aria-label="Main navigation"]')).not.toBeNull();
  });

  it("retries a real child route while keeping the workspace shell mounted", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    chatApi.broken = true;
    const { client } = await mountProductionRoute("/workspace/chat");
    await vi.waitFor(() =>
      expect(container.querySelector("h1")?.textContent).toBe("Something went wrong"),
    );

    const shell = container.querySelector("[data-shell-global-status]")?.parentElement;
    const navigation = container.querySelector('nav[aria-label="Main navigation"]');
    expect(shell).not.toBeNull();
    expect(navigation).not.toBeNull();
    expect(container.querySelector("h1")?.textContent).toBe("Something went wrong");
    expect(container.textContent).not.toContain(privateError);
    expect(container.textContent).not.toContain("Cannot read properties");
    expect(
      container.querySelector('a[aria-label="Stacklok — go to Chats"][href="/workspace/chat"]'),
    ).not.toBeNull();
    expect(
      Array.from(container.querySelectorAll<HTMLAnchorElement>('a[href="/workspace/chat"]')).some(
        (link) => link.textContent?.includes("Go to Chats"),
      ),
    ).toBe(true);

    chatApi.broken = false;
    client.resetQueries({ queryKey: getRuntimeSettingsOptions().queryKey });
    const retry = Array.from(container.querySelectorAll<HTMLButtonElement>("button")).find(
      (button) => button.textContent?.trim() === "Try again",
    );
    expect(retry).toBeDefined();
    await act(async () => retry?.click());
    await vi.waitFor(() => expect(container.querySelector("h1")?.textContent).toBe("New chat"));
    expect(shell?.isConnected).toBe(true);
    expect(container.querySelector('nav[aria-label="Main navigation"]')).toBe(navigation);
  });
});

function testRouter(path: string, chatPage: () => React.ReactNode, loader?: () => void) {
  const rootRoute = createRootRoute({ component: Outlet });
  const workspaceRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "workspace",
    component: () => (
      <div data-workspace-frame="true">
        <main>
          <Outlet />
        </main>
      </div>
    ),
  });
  const chatRoute = createRoute({
    getParentRoute: () => workspaceRoute,
    path: "chat",
    component: chatPage,
    loader,
  });
  const routeTree = rootRoute.addChildren([workspaceRoute.addChildren([chatRoute])]);
  return createRouter({
    ...studioRouterOptions,
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  });
}

describe("Studio error routes", () => {
  it("recovers from a route error without exposing its message", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    let broken = true;
    const router = testRouter("/workspace/chat", () => {
      if (broken) throw new Error(privateError);
      return <h1>Chat ready</h1>;
    });
    await mount(<RouterProvider router={router} />);

    const frame = container.querySelector("[data-workspace-frame]");
    const heading = container.querySelector("h1");
    expect(frame).not.toBeNull();
    expect(heading?.textContent).toBe("Something went wrong");
    expect(document.activeElement).toBe(heading);
    expect(container.querySelector("main")).not.toBeNull();
    expect(container.textContent).not.toContain(privateError);
    expect(container.textContent).not.toContain("SECRET_SENTINEL");
    expect(container.querySelector('a[href="/workspace/chat"]')?.textContent).toContain(
      "Go to Chats",
    );

    broken = false;
    const retry = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Try again",
    );
    expect(retry).toBeDefined();
    await act(async () => retry?.click());
    expect(container.textContent).toContain("Chat ready");
    expect(frame?.isConnected).toBe(true);
    expect(container.textContent).not.toContain(privateError);
  });

  it("gives an unknown route a branded home link", async () => {
    const router = testRouter("/workspace/missing", () => <h1>Chat ready</h1>);
    await mount(<RouterProvider router={router} />);
    const heading = container.querySelector("h1");
    expect(heading?.textContent).toBe("Page not found");
    expect(container.querySelectorAll("h1")).toHaveLength(1);
    expect(container.querySelector("main")).not.toBeNull();
    expect(document.activeElement).toBe(heading);
    expect(container.textContent).toContain("Mecatl Studio");
    const home = Array.from(container.querySelectorAll('a[href="/workspace/chat"]')).find((link) =>
      link.textContent?.includes("Go to Chats"),
    );
    expect(home?.textContent).toContain("Go to Chats");
    await act(async () => (home as HTMLAnchorElement).click());
    expect(container.textContent).toContain("Chat ready");
    expect(router.state.location.pathname).toBe("/workspace/chat");
  });

  it("loads a known route directly", async () => {
    const router = testRouter("/workspace/chat", () => <h1>Chat ready</h1>);
    await mount(<RouterProvider router={router} />);
    expect(container.querySelector("h1")?.textContent).toBe("Chat ready");
    expect(container.querySelector("[data-workspace-frame]")).not.toBeNull();
  });

  it("recovers from a loader error without exposing its message", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    let broken = true;
    const router = testRouter(
      "/workspace/chat",
      () => <h1>Chat ready</h1>,
      () => {
        if (broken) throw new Error(privateError);
      },
    );
    await mount(<RouterProvider router={router} />);
    const frame = container.querySelector("[data-workspace-frame]");
    expect(container.querySelector("h1")?.textContent).toBe("Something went wrong");
    expect(container.textContent).not.toContain(privateError);
    broken = false;
    const retry = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Try again",
    );
    await act(async () => retry?.click());
    expect(container.textContent).toContain("Chat ready");
    expect(frame?.isConnected).toBe(true);
  });

  it("keeps a root render failure generic and offers reload and home", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const reload = vi.fn();
    function CrashedRoot(): React.ReactNode {
      throw new Error(privateError);
    }
    await mount(
      <RootErrorBoundary reload={reload}>
        <CrashedRoot />
      </RootErrorBoundary>,
    );
    const heading = container.querySelector("h1");
    expect(heading?.textContent).toBe("Something went wrong");
    expect(container.querySelectorAll("h1")).toHaveLength(1);
    expect(container.querySelector("main")).not.toBeNull();
    expect(document.activeElement).toBe(heading);
    expect(container.textContent).not.toContain(privateError);
    expect(container.querySelector('a[href="/workspace/chat"]')?.textContent).toContain(
      "Go to Chats",
    );
    const retry = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Reload Studio",
    );
    expect(retry).toBeDefined();
    await act(async () => retry?.click());
    expect(reload).toHaveBeenCalledOnce();
  });
});
