// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client } from "@mecatl-studio/contracts/client";
import type { GetAuthSessionResponse } from "@mecatl-studio/contracts/generated";
import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { GlobalSearch } from "./global-search";

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const inventoryPaths = [
  "/api/v1/sessions",
  "/api/v1/schedules",
  "/api/v1/skills",
  "/api/v1/learned-skills",
  "/api/v1/user-memory",
];

let root: Root | undefined;
let container: HTMLDivElement | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  container?.remove();
  container = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

it("bypasses the browser HTTP cache for each authorized search inventory request", async () => {
  const requests: Request[] = [];
  const requestInits: Array<{ cache: RequestCache | undefined; pathname: string }> = [];
  const BrowserRequest = Request;
  vi.stubGlobal(
    "Request",
    class extends BrowserRequest {
      constructor(input: RequestInfo | URL, init?: RequestInit) {
        super(input, init);
        requestInits.push({ cache: init?.cache, pathname: new URL(this.url).pathname });
      }
    },
  );
  vi.stubGlobal("fetch", async (request: Request) => {
    requests.push(request);
    const pathname = new URL(request.url).pathname;
    const body =
      pathname === "/api/v1/auth/session"
        ? { account: "account-a", mode: "oidc", status: "authenticated" }
        : pathname === "/api/v1/sessions"
          ? { items: [] }
          : { items: [], supported: true };
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
      status: 200,
    });
  });
  client.setConfig({ baseUrl: "http://studio.test" });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  const authenticatedSession: GetAuthSessionResponse = {
    account: "account-a",
    mode: "oidc",
    status: "authenticated",
  };
  queryClient.setQueryData(getAuthSessionOptions().queryKey, authenticatedSession);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(
      <QueryClientProvider client={queryClient}>
        <ShortcutProvider>
          <GlobalSearch />
        </ShortcutProvider>
      </QueryClientProvider>,
    );
  });
  expect(requests).toHaveLength(0);

  await act(async () =>
    document.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
  );
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  const inventoryRequests = requests.filter((request) =>
    inventoryPaths.includes(new URL(request.url).pathname),
  );
  expect(inventoryRequests.map((request) => new URL(request.url).pathname).sort()).toEqual(
    [...inventoryPaths].sort(),
  );
  for (const request of inventoryRequests) {
    expect(request.method).toBe("GET");
  }
  expect(requestInits.filter(({ pathname }) => inventoryPaths.includes(pathname))).toEqual(
    expect.arrayContaining(inventoryPaths.map((pathname) => ({ cache: "no-store", pathname }))),
  );
});
