// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import type {
  GetLearningProposalResponse,
  ListLearningProposalsResponse,
} from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { routeTree } from "@/routeTree.gen";
import { LearningSettingsPage } from "./learning-settings-page";

/**
 * What the prototype port adds to Settings → Learning: cursor paging that keeps
 * the filter, Refresh restarting from the first page, the Deferred filter and
 * its procedure approvals, and Details, which re-reads one proposal to show
 * where it came from and hands the list a newer version before the next
 * decision.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Proposal = ListLearningProposalsResponse["items"][number];
type Evidence = GetLearningProposalResponse["evidence"][number];

function proposal(id: string, overrides: Partial<Proposal> = {}): Proposal {
  return {
    body: "",
    createdAt: "",
    decisions: [],
    description: "",
    evidenceCount: 1,
    id,
    key: `key/${id}`,
    kind: "user_model",
    learnedSkillId: "",
    projectScoped: false,
    promotionAvailable: true,
    promotionUnavailableReason: "",
    status: "staged",
    title: `Title of ${id}`,
    triggers: [],
    updatedAt: "",
    value: `Value of ${id}`,
    version: "v1",
    ...overrides,
  };
}

function evidence(overrides: Partial<Evidence> = {}): Evidence {
  return {
    availability: "available",
    available: true,
    digest: "sha256:aaa",
    eventSeq: "0",
    locator: "message",
    ordinal: 1,
    preview: "",
    sessionId: "session-1",
    toolCallId: "",
    ...overrides,
  };
}

type Page = Partial<ListLearningProposalsResponse>;

const state = {
  capabilities: { learningProposals: true, reflection: true },
  decide: [] as unknown[],
  details: {} as Record<string, () => GetLearningProposalResponse | Response>,
  detailCalls: [] as string[],
  listCalls: [] as URLSearchParams[],
  /** Pages keyed by `status|cursor`. */
  pages: {} as Record<string, Page | (() => Response)>,
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    headers: { "content-type": status < 400 ? "application/json" : "application/problem+json" },
    status,
  });
}

function problem(status: number, code: string, detail: string) {
  return json({ code, detail, instance: "", status, title: "", type: "about:blank" }, status);
}

const fakeFetch: typeof globalThis.fetch = async (input) => {
  const request = input instanceof Request ? input : new Request(String(input));
  const url = new URL(request.url);
  const path = url.pathname;
  if (path === "/api/v1/sessions") return json({ complete: true, items: [] });
  if (path === "/api/v1/learning-proposals") {
    state.listCalls.push(url.searchParams);
    const key = `${url.searchParams.get("status") ?? ""}|${url.searchParams.get("cursor") ?? ""}`;
    const page = state.pages[key];
    if (typeof page === "function") return page();
    return json({
      complete: true,
      items: [],
      nextCursor: "",
      reason: "",
      supported: true,
      ...page,
    });
  }
  const decision = /^\/api\/v1\/learning-proposals\/([^/]+)\/decisions$/.exec(path);
  if (decision && request.method === "POST") {
    state.decide.push({ body: await request.clone().json(), id: decision[1] });
    return json(proposal(decodeURIComponent(decision[1] ?? ""), { status: "promoted" }));
  }
  const detail = /^\/api\/v1\/learning-proposals\/([^/]+)$/.exec(path);
  if (detail && request.method === "GET") {
    const id = decodeURIComponent(detail[1] ?? "");
    state.detailCalls.push(id);
    const answer = state.details[id]?.();
    if (answer instanceof Response) return answer;
    return answer ? json(answer) : problem(404, "not_found", `No learning proposal with id ${id}`);
  }
  return problem(404, "not_found", `unexpected ${request.method} ${path}`);
};

const initialApiConfig = apiClient.getConfig();

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  // The generated query keys carry the client's base URL, so configure it first.
  apiClient.setConfig({ baseUrl: window.location.origin, fetch: fakeFetch });
  client.setQueryData(getRuntimeQueryKey(), {
    capabilities: state.capabilities,
    connection: "online",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/settings/learning"] }),
    routeTree,
  });
  return render(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <LearningSettingsPage />
      </RouterContextProvider>
    </QueryClientProvider>,
  );
}

const isDisabled = (element: Element) => (element as HTMLButtonElement).disabled;

async function row(title: string) {
  const label = await screen.findByText(title);
  const item = label.closest("li");
  if (!item) throw new Error(`no row for ${title}`);
  return within(item);
}

const titles = () =>
  [...document.querySelectorAll("[data-proposal-id]")].map((item) =>
    item.getAttribute("data-proposal-id"),
  );

const calls = () =>
  state.listCalls.map((query) => ({
    cursor: query.get("cursor"),
    limit: query.get("limit"),
    status: query.get("status"),
  }));

beforeEach(() => {
  state.capabilities = { learningProposals: true, reflection: true };
  state.decide = [];
  state.details = {};
  state.detailCalls = [];
  state.listCalls = [];
  state.pages = {};
});

afterEach(() => {
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
  cleanup();
});

describe("learning queue paging", () => {
  it("appends the next page with the daemon's cursor and keeps the filter", async () => {
    const user = userEvent.setup();
    state.pages = {
      "deferred_unsupported|": { items: [proposal("d1", { status: "deferred_unsupported" })] },
      "staged|": { complete: false, items: [proposal("p1"), proposal("p2")], nextCursor: "p2" },
      "staged|p2": { items: [proposal("p2"), proposal("p3")] },
    };
    renderPage();
    await row("Title of p1");
    await user.click(screen.getByRole("button", { name: "Load more" }));
    await row("Title of p3");
    // A proposal the moving queue already showed is not listed twice.
    expect(titles()).toEqual(["p1", "p2", "p3"]);
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
    expect(calls()).toEqual([
      { cursor: "", limit: "50", status: "staged" },
      { cursor: "p2", limit: "50", status: "staged" },
    ]);

    await user.click(screen.getByRole("button", { name: "Deferred" }));
    await row("Title of d1");
    expect(screen.getByRole("button", { name: "Deferred" }).getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(calls().at(-1)).toEqual({ cursor: "", limit: "50", status: "deferred_unsupported" });
  });

  it("keeps the rows it has when the next page fails", async () => {
    const user = userEvent.setup();
    state.pages = {
      "staged|": { complete: false, items: [proposal("p1")], nextCursor: "p1" },
      "staged|p1": () => problem(400, "cursor_expired", "The cursor expired."),
    };
    renderPage();
    await row("Title of p1");
    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect((await screen.findByRole("alert")).textContent).toBe("The cursor expired.");
    expect(titles()).toEqual(["p1"]);
  });

  it("restarts from the first page on Refresh", async () => {
    const user = userEvent.setup();
    state.pages = {
      "staged|": { complete: false, items: [proposal("p1")], nextCursor: "p1" },
      "staged|p1": { items: [proposal("p2")] },
    };
    renderPage();
    await row("Title of p1");
    await user.click(screen.getByRole("button", { name: "Load more" }));
    await row("Title of p2");

    await user.click(screen.getByRole("button", { name: "Refresh suggestions" }));
    await waitFor(() => expect(calls()).toHaveLength(3));
    await row("Title of p1");
    expect(titles()).toEqual(["p1"]);
    expect(calls().at(-1)).toEqual({ cursor: "", limit: "50", status: "staged" });
  });
});

describe("learning queue rows", () => {
  it("offers Approve, not Reject, for a deferred procedure and none for a deferred fact", async () => {
    const user = userEvent.setup();
    state.pages = {
      "deferred_unsupported|": {
        items: [
          proposal("skill", { kind: "procedure", status: "deferred_unsupported" }),
          proposal("fact", { status: "deferred_unsupported" }),
        ],
      },
    };
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Deferred" }));
    const skill = await row("Title of skill");
    expect(skill.queryByRole("button", { name: "Reject" })).toBeNull();
    expect(skill.getByText("Deferred")).toBeTruthy();
    const fact = await row("Title of fact");
    expect(fact.queryByRole("button", { name: "Approve" })).toBeNull();

    await user.click(skill.getByRole("button", { name: "Approve" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("draft a learned skill");
    await user.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(state.decide).toEqual([
        { body: { decision: "approve", expectedVersion: "v1", reason: "" }, id: "skill" },
      ]),
    );
  });

  it("says why Approve is unavailable", async () => {
    state.pages = {
      "staged|": {
        items: [
          proposal("p1", {
            promotionAvailable: false,
            promotionUnavailableReason: "project promotion requires a trusted root",
          }),
        ],
      },
    };
    renderPage();
    const item = await row("Title of p1");
    expect(isDisabled(item.getByRole("button", { name: "Approve" }))).toBe(true);
    expect(item.getByText("project promotion requires a trusted root")).toBeTruthy();
  });

  it("loads the sources once, on demand, and shows the excerpt as text", async () => {
    const user = userEvent.setup();
    state.pages = { "staged|": { items: [proposal("p1", { learnedSkillId: "skill-7" })] } };
    state.details.p1 = () => ({
      ...proposal("p1", { learnedSkillId: "skill-7" }),
      evidence: [
        evidence({ preview: '<img src=x onerror="alert(1)"> keep it short' }),
        evidence({
          availability: "evidence changed",
          available: false,
          digest: "sha256:bbb",
          sessionId: "",
        }),
      ],
    });
    renderPage();
    const item = await row("Title of p1");
    expect(state.detailCalls).toEqual([]);

    const details = item.getByRole("button", { name: "Details" });
    expect(details.getAttribute("aria-expanded")).toBe("false");
    await user.click(details);
    expect(details.getAttribute("aria-expanded")).toBe("true");
    const region = document.getElementById(details.getAttribute("aria-controls") ?? "");
    if (!region) throw new Error("Details controls no region");
    const sources = within(region);
    expect((await sources.findByText('<img src=x onerror="alert(1)"> keep it short')).tagName).toBe(
      "PRE",
    );
    expect(region.querySelector("img")).toBeNull();
    expect(sources.getByRole("link", { name: "View chat" }).getAttribute("href")).toBe(
      "/workspace/chat?sessionId=session-1",
    );
    expect(sources.getByText("Available")).toBeTruthy();
    expect(sources.getByText("Unavailable (evidence changed)")).toBeTruthy();
    expect(sources.getByText("From a chat")).toBeTruthy();
    expect(sources.getByRole("link", { name: "View learned skill" }).getAttribute("href")).toBe(
      "/workspace/skills/learned/skill-7",
    );

    await user.click(details);
    await user.click(details);
    expect(state.detailCalls).toEqual(["p1"]);
  });

  it("takes a newer version from Details before the next decision", async () => {
    const user = userEvent.setup();
    state.pages = { "staged|": { items: [proposal("p1")] } };
    state.details.p1 = () => ({ ...proposal("p1", { version: "v2" }), evidence: [evidence()] });
    renderPage();
    const item = await row("Title of p1");
    await user.click(item.getByRole("button", { name: "Details" }));
    expect(
      await item.findByText(
        "This suggestion changed since the list was loaded. Review it again before deciding.",
      ),
    ).toBeTruthy();

    await user.click(item.getByRole("button", { name: "Approve" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(state.decide).toEqual([
        { body: { decision: "approve", expectedVersion: "v2", reason: "" }, id: "p1" },
      ]),
    );
  });

  it("explains a failed re-read", async () => {
    const user = userEvent.setup();
    state.pages = { "staged|": { items: [proposal("p1")] } };
    state.details.p1 = () =>
      problem(409, "failed_precondition", "proposal manifest is unavailable");
    renderPage();
    const item = await row("Title of p1");
    await user.click(item.getByRole("button", { name: "Details" }));
    expect((await item.findByRole("alert")).textContent).toBe(
      "Could not load where this came from: proposal manifest is unavailable",
    );
  });
});

describe("learning capabilities", () => {
  it("shows Learning is off when the agent offers neither proposals nor reflection", async () => {
    state.capabilities = { learningProposals: false, reflection: false };
    renderPage();
    expect(await screen.findByText("Learning is off for this agent.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Pending" })).toBeNull();
    expect(state.listCalls).toEqual([]);
  });

  it("explains a missing queue while still offering reflection", async () => {
    state.capabilities = { learningProposals: false, reflection: true };
    renderPage();
    expect(
      await screen.findByText("Reviewing suggestions is not available right now."),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Find suggestions" })).toBeTruthy();
    expect(state.listCalls).toEqual([]);
  });
});
