// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { MemoryDetailResponse, UserMemoryResponse } from "@mecatl-studio/contracts";
import { client as apiClient } from "@mecatl-studio/contracts/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { MemoryFactDetail } from "./memory-fact-detail";

/**
 * The memory fact detail page: the fact's value leads, daemon-derived
 * provenance rows follow, the source session links to its chat, and the bounded
 * history renders newest-first or is reported as unavailable from this
 * store/driver. Read-only. Ported from the design prototype's test.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const state = {
  connection: "online" as string,
  detail: undefined as unknown,
  detailError: undefined as { detail: string; status: number } | undefined,
  detailPending: false,
  list: undefined as unknown,
  listPending: false,
};

const initialApiConfig = apiClient.getConfig();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    headers: { "content-type": status < 400 ? "application/json" : "application/problem+json" },
    status,
  });
}

function problem(status: number, detail: string) {
  return json({ code: "", detail, instance: "", status, title: "", type: "about:blank" }, status);
}

/** Fakes the BFF reads the page makes; never resolves a read marked pending. */
const fakeFetch: typeof globalThis.fetch = async (input) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (url.pathname === "/api/v1/runtime")
    return json({ capabilities: {}, connection: state.connection });
  if (url.pathname === "/api/v1/user-memory") {
    if (state.listPending) return new Promise<Response>(() => {});
    return json(state.list);
  }
  if (url.pathname.startsWith("/api/v1/user-memory/")) {
    if (state.detailPending) return new Promise<Response>(() => {});
    if (state.detailError) return problem(state.detailError.status, state.detailError.detail);
    return json(state.detail);
  }
  return problem(404, `unexpected ${url.pathname}`);
};

const UPDATED_AT = "2026-01-10T00:00:00.000Z";
const PRIOR_AT = "2025-12-01T00:00:00.000Z";

function index(
  items: UserMemoryResponse["items"] = [
    { description: "The operator prefers tabs.", key: "prefers-tabs" },
  ],
): UserMemoryResponse {
  return { items, reason: "", sha256: "", sizeBytes: "0", supported: true };
}

function detail(overrides: Partial<MemoryDetailResponse["current"]> = {}): MemoryDetailResponse {
  return {
    current: {
      description: "The operator prefers tabs.",
      key: "prefers-tabs",
      origin: "reflection",
      sourceSessionId: "",
      status: "active",
      updatedAt: UPDATED_AT,
      value: "Tabs, width 4",
      version: "3",
      writer: "agent",
      ...overrides,
    },
    history: [
      {
        description: "",
        key: "prefers-tabs",
        origin: "",
        sourceSessionId: "",
        status: "superseded",
        updatedAt: "2025-12-20T00:00:00.000Z",
        value: "",
        version: "2",
        writer: "",
      },
      {
        description: "",
        key: "prefers-tabs",
        origin: "",
        sourceSessionId: "",
        status: "superseded",
        updatedAt: PRIOR_AT,
        value: "",
        version: "1",
        writer: "",
      },
    ],
    historyAvailable: true,
  };
}

async function renderDetail(memoryId = "prefers-tabs") {
  apiClient.setConfig({ baseUrl: window.location.origin, fetch: fakeFetch });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: [`/workspace/memory?item=${encodeURIComponent(memoryId)}`],
    }),
    routeTree,
  });
  await act(async () => {
    render(
      <QueryClientProvider client={client}>
        <RouterContextProvider router={router}>
          <MemoryFactDetail memoryId={memoryId} />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
  });
}

/** The value cell of the Details row labelled `label`. */
function factValue(label: string): HTMLElement {
  const value = screen.getByText(label, { selector: "span" }).nextElementSibling;
  if (!(value instanceof HTMLElement)) throw new Error(`fact "${label}" has no value cell`);
  return value;
}

beforeEach(() => {
  state.connection = "online";
  state.listPending = false;
  state.list = index();
  state.detailError = undefined;
  state.detailPending = false;
  state.detail = detail();
});

afterEach(() => {
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
  cleanup();
});

describe("MemoryFactDetail", () => {
  it("leads with the fact value and shows daemon-derived provenance rows", async () => {
    await renderDetail();

    const value = await screen.findByText("Tabs, width 4");
    expect(value.tagName).toBe("PRE");
    expect(
      within(screen.getByRole("region", { name: "Value" })).getByText("The operator prefers tabs."),
    ).toBeTruthy();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("prefers-tabs");

    expect(factValue("Key").textContent).toBe("prefers-tabs");
    expect(factValue("Status").textContent).toBe("active");
    expect(factValue("Version").textContent).toBe("3");
    expect(factValue("Writer").textContent).toBe("agent");
    expect(factValue("Origin").textContent).toBe("reflection");

    const updated = within(factValue("Updated")).getByText(/ago$/);
    expect(updated.getAttribute("title")).toBe(UPDATED_AT);
  });

  it("shows the value as loading while the detail read is in flight", async () => {
    state.detailPending = true;
    await renderDetail();
    const value = await screen.findByRole("region", { name: "Value" });
    expect(value.textContent).toContain("Loading value…");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("prefers-tabs");
  });

  it("links the source session to its chat", async () => {
    state.detail = detail({ sourceSessionId: "session-fixture-1" });
    await renderDetail();
    await screen.findByText("Tabs, width 4");

    expect(screen.getByRole("link", { name: "session-fixture-1" }).getAttribute("href")).toBe(
      "/workspace/chat?sessionId=session-fixture-1",
    );
  });

  it("lists the bounded revisions newest-first as version · status · date", async () => {
    await renderDetail();
    await screen.findByText("Tabs, width 4");

    const history = screen.getByRole("region", { name: "History" });
    expect(history.textContent).toContain("2 bounded revisions");
    const rows = within(history).getAllByRole("listitem");
    expect(rows.map((row) => row.textContent)).toEqual([
      "2 · superseded · 2025-12-20",
      "1 · superseded · 2025-12-01",
    ]);
  });

  it("reports history the store/driver could not supply, never as 'no revisions'", async () => {
    state.detail = { ...detail(), history: [], historyAvailable: false };
    await renderDetail();
    await screen.findByText("Tabs, width 4");

    const history = screen.getByRole("region", { name: "History" });
    expect(history.textContent).toContain("History unavailable from this store/driver.");
    expect(history.textContent).not.toContain("bounded revision");
  });

  it("says the value is unavailable when the read fails", async () => {
    state.detailError = { detail: "store offline", status: 503 };
    await renderDetail();
    expect(await screen.findByText("Value unavailable: store offline")).toBeTruthy();
    expect(factValue("Key").textContent).toBe("prefers-tabs");
  });

  it("stays read-only: the footer names the agent's memory tools, no editor", async () => {
    await renderDetail();
    await screen.findByText("Tabs, width 4");
    expect(
      screen.getByText("Read-only — ask the agent to use ForgetUserMemory or UndoUserMemory."),
    ).toBeTruthy();
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("treats a still-loading index as loading, never as a missing fact", async () => {
    state.listPending = true;
    await renderDetail();
    expect(await screen.findByText("Loading…")).toBeTruthy();
    expect(screen.queryByText("Memory not found")).toBeNull();
  });

  it("reports a fact the settled index lacks as missing, in-page and recoverable", async () => {
    state.list = index([]);
    await renderDetail();
    expect(await screen.findByText("Memory not found")).toBeTruthy();
    expect(screen.getByText(/the agent may have forgotten or renamed it\./)).toBeTruthy();
    const back = screen.getByRole("link", { name: "Back to Memory" });
    expect(back.getAttribute("href")).toBe("/workspace/settings/memory");
  });

  it("reports a fact the detail read answers 404 for as missing", async () => {
    state.detailError = { detail: "No memory entry named prefers-tabs", status: 404 };
    await renderDetail();
    expect(await screen.findByText("Memory not found")).toBeTruthy();
    expect(screen.queryByText(/Value unavailable/)).toBeNull();
  });

  it("names an unreachable agent instead of claiming the fact is gone", async () => {
    state.connection = "offline";
    await renderDetail();
    expect(await screen.findByText("Memory not found")).toBeTruthy();
    expect(
      screen.getByText("The agent can't be reached, so this fact can't be looked up right now."),
    ).toBeTruthy();
  });

  it("says memory is off, with the daemon's own reason, when the store is disabled", async () => {
    state.list = {
      items: [],
      reason: "User memory is not enabled on this Mecatl deployment.",
      sha256: "",
      sizeBytes: "0",
      supported: false,
    };
    await renderDetail();
    expect(await screen.findByText("Memory is turned off for this agent.")).toBeTruthy();
    expect(screen.getByText("User memory is not enabled on this Mecatl deployment.")).toBeTruthy();
  });

  it("the back pill returns to Settings → Memory", async () => {
    await renderDetail();
    await screen.findByText("Tabs, width 4");
    // The chevron is aria-hidden, so the accessible name is just "Back".
    const back = screen.getByRole("link", { name: "Back" });
    expect(back.getAttribute("href")).toBe("/workspace/settings/memory");
  });
});
