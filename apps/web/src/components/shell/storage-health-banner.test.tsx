// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  AuthRecoveryContext,
  type AuthRecoveryContextValue,
} from "@/features/auth/auth-recovery-context";
import { StorageHealthBanner } from "./storage-health-banner";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const healthy = {
  activeJob: false,
  available: true,
  childCount: "0",
  corruptCount: "0",
  currentBytes: "1024",
  lastFailure: false,
  mainCount: "3",
  reclaimableBytes: null,
  scheduledCount: "0",
  sessionCount: "3",
  supported: true,
  unknownCount: "0",
};

function runtime(storageHealth: boolean, connection = "online") {
  return { capabilities: { storageHealth }, connection };
}

const initialApiConfig = apiClient.getConfig();
let root: Root | undefined;
let container: HTMLDivElement | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  container?.remove();
  container = undefined;
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
});

async function render(
  responses: { health?: unknown; runtime: unknown },
  phase: AuthRecoveryContextValue["phase"] = "ready",
) {
  const requests: string[] = [];
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const request = input instanceof Request ? input : new Request(input);
    const pathname = new URL(request.url).pathname;
    requests.push(pathname);
    if (pathname === "/api/v1/runtime") return Response.json(responses.runtime);
    if (pathname === "/api/v1/storage/health") return Response.json(responses.health);
    throw new Error(`Unexpected request: ${pathname}`);
  });
  apiClient.setConfig({ baseUrl: window.location.origin, fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(
      <QueryClientProvider client={queryClient}>
        <AuthRecoveryContext.Provider
          value={{
            banner: {
              authenticated: phase === "ready",
              publicStatusFailed: false,
              sessionCheckFailed: false,
            },
            loginUrl: "/api/v1/auth/login",
            phase,
            popupIssue: null,
            retrySession: () => {},
            startPopupLogin: () => {},
          }}
        >
          <StorageHealthBanner />
        </AuthRecoveryContext.Provider>
      </QueryClientProvider>,
    );
  });
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  return requests;
}

describe("StorageHealthBanner", () => {
  it.each([
    [{ ...healthy, corruptCount: "2" }, "2 saved items can no longer be opened."],
    [{ ...healthy, available: false }, "The agent cannot read its saved chats right now."],
    [{ ...healthy, lastFailure: true }, "A recent automatic clean-up did not finish."],
  ])("explains a degraded store", async (health, detail) => {
    await render({ health, runtime: runtime(true) });
    await vi.waitFor(() => expect(container?.querySelector('[role="status"]')).not.toBeNull());
    const notice = container?.querySelector('[role="status"]');
    expect(notice?.textContent).toContain(
      "Session storage is degraded. Some chats may be missing.",
    );
    expect(notice?.textContent).toContain(detail);
  });

  it("stays hidden for a healthy or unsupported store", async () => {
    let requests = await render({ health: healthy, runtime: runtime(true) });
    expect(requests).toContain("/api/v1/storage/health");
    expect(container?.textContent).toBe("");
    await act(async () => root?.unmount());
    container?.remove();
    requests = await render({
      health: { ...healthy, corruptCount: "2", supported: false },
      runtime: runtime(true),
    });
    expect(requests).toContain("/api/v1/storage/health");
    expect(container?.textContent).toBe("");
  });

  it("asks for storage health only when an online agent reports it", async () => {
    let requests = await render({ health: healthy, runtime: runtime(false) });
    expect(requests).toEqual(["/api/v1/runtime"]);
    expect(container?.textContent).toBe("");
    await act(async () => root?.unmount());
    container?.remove();
    requests = await render({ health: healthy, runtime: runtime(true, "reconnecting") });
    expect(requests).toEqual(["/api/v1/runtime"]);
  });

  it.each(["checking", "sign-in", "verification-unavailable"] as const)(
    "makes no request while the identity is %s",
    async (phase) => {
      const requests = await render(
        { health: { ...healthy, corruptCount: "2" }, runtime: runtime(true) },
        phase,
      );
      expect(requests).toEqual([]);
      expect(container?.textContent).toBe("");
    },
  );
});
