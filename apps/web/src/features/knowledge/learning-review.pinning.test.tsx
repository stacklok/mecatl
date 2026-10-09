// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import type { ListLearningProposalsResponse } from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Toaster } from "@/components/ui/sonner";
import { routeTree } from "@/routeTree.gen";
import { LearningReview as Subject } from "./learning-review";

/**
 * Pins what Settings → Learning does today, through the rendered page and the
 * BFF requests it makes, so the prototype port can prove it changes the look
 * and the copy but not the semantics:
 *
 * - each decision and undo is confirmed first and carries the loaded version;
 * - a version conflict refreshes the queue and is never retried;
 * - the status filter is the exact token the list request sends;
 * - reflection offers only completed chats and explains an unsupported or
 *   failed run.
 *
 * Copy lives in `copy` and the session picker in `chooseSession`, so a copy or
 * control change edits those, never an assertion.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const copy = {
  approve: "Approve",
  approved: "Proposal approved.",
  cancel: "Cancel",
  confirm: "Confirm",
  conflict:
    "That proposal changed since it was loaded. The queue was refreshed; review it again before deciding.",
  emptyPending: "Nothing is waiting for review.",
  noFinishedChats: "No completed chats are available yet.",
  promotedFilter: "Promoted",
  receipt: "Reflection completed: 2 staged · 0 promoted · 0 conflicted.",
  reflect: "Reflect",
  reflectionOff: "Session reflection is not enabled on this Mecatl deployment.",
  reject: "Reject",
  rejected: "Proposal rejected.",
  rejectedFilter: "Rejected",
  sessionPicker: "Completed session",
  undo: "Undo promotion",
  undone: "Promotion undone.",
};

type Proposal = ListLearningProposalsResponse["items"][number];

function proposal(overrides: Partial<Proposal> = {}): Proposal {
  return {
    body: "",
    createdAt: "",
    decisions: [],
    description: "How the user likes answers",
    evidenceCount: 1,
    id: "proposal-1",
    key: "communication",
    kind: "user_model",
    learnedSkillId: "",
    projectScoped: false,
    promotionAvailable: true,
    promotionUnavailableReason: "",
    status: "staged",
    title: "Communication preference",
    triggers: [],
    updatedAt: "",
    value: "Keep answers concise.",
    version: "v1",
    ...overrides,
  };
}

interface Problem {
  code: string;
  detail: string;
  status: number;
}

const state = {
  decide: [] as Array<{ body: unknown; id: string }>,
  decideAnswer: (() => undefined) as () => Problem | undefined,
  listCalls: [] as URLSearchParams[],
  pages: {} as Record<string, Partial<ListLearningProposalsResponse>>,
  reflect: [] as string[],
  reflectAnswer: (() => undefined) as () => Problem | undefined,
  runtime: { reflection: true },
  sessions: [] as Array<{ id: string; state: string; title: string }>,
  undo: [] as Array<{ body: unknown; id: string }>,
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    headers: { "content-type": status < 400 ? "application/json" : "application/problem+json" },
    status,
  });
}

function problem({ code, detail, status }: Problem) {
  return json({ code, detail, instance: "", status, title: "", type: "about:blank" }, status);
}

const fakeFetch: typeof globalThis.fetch = async (input) => {
  const request = input instanceof Request ? input : new Request(String(input));
  const url = new URL(request.url);
  const path = url.pathname;
  if (path === "/api/v1/sessions" && request.method === "GET") {
    return json({ complete: true, items: state.sessions });
  }
  if (path === "/api/v1/learning-proposals" && request.method === "GET") {
    state.listCalls.push(url.searchParams);
    const status = url.searchParams.get("status") ?? "";
    return json({
      complete: true,
      items: [],
      nextCursor: "",
      reason: "",
      supported: true,
      ...state.pages[status],
    });
  }
  const decision = /^\/api\/v1\/learning-proposals\/([^/]+)\/decisions$/.exec(path);
  if (decision && request.method === "POST") {
    const body = (await request.clone().json()) as { decision: string; expectedVersion: string };
    state.decide.push({ body, id: decodeURIComponent(decision[1] ?? "") });
    const refusal = state.decideAnswer();
    if (refusal) return problem(refusal);
    return json(
      proposal({ status: body.decision === "approve" ? "promoted" : "rejected", version: "v2" }),
    );
  }
  const undo = /^\/api\/v1\/learning-proposals\/([^/]+)\/undo$/.exec(path);
  if (undo && request.method === "POST") {
    state.undo.push({ body: await request.clone().json(), id: decodeURIComponent(undo[1] ?? "") });
    return json(proposal({ status: "undone", version: "v4" }));
  }
  const reflection = /^\/api\/v1\/sessions\/([^/]+)\/reflection$/.exec(path);
  if (reflection && request.method === "POST") {
    state.reflect.push(decodeURIComponent(reflection[1] ?? ""));
    const refusal = state.reflectAnswer();
    if (refusal) return problem(refusal);
    return json({
      abstained: false,
      conflicted: 0,
      disposition: "completed",
      message: "",
      promoted: 0,
      queued: 0,
      reason: "",
      reflectionId: "reflection-1",
      staged: 2,
    });
  }
  return problem({
    code: "not_found",
    detail: `unexpected ${request.method} ${path}`,
    status: 404,
  });
};

const initialApiConfig = apiClient.getConfig();

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  // The generated query keys carry the client's base URL, so configure it first.
  apiClient.setConfig({ baseUrl: window.location.origin, fetch: fakeFetch });
  client.setQueryData(getRuntimeQueryKey(), {
    capabilities: { learningProposals: true, reflection: state.runtime.reflection },
    connection: "online",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/settings/learning"] }),
    routeTree,
  });
  return render(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <Subject />
        <Toaster />
      </RouterContextProvider>
    </QueryClientProvider>,
  );
}

/** The plain DOM reads Studio's tests use; there are no jest-dom matchers. */
const isDisabled = (element: Element) => (element as HTMLButtonElement).disabled;

async function row(title: string) {
  const heading = await screen.findByText(title);
  const item = heading.closest("li");
  if (!item) throw new Error(`no row for ${title}`);
  return within(item);
}

async function chooseSession(user: ReturnType<typeof userEvent.setup>, title: string) {
  const picker = await screen.findByLabelText(copy.sessionPicker);
  await user.selectOptions(picker, title);
}

async function sessionChoices() {
  const picker = (await screen.findByLabelText(copy.sessionPicker)) as HTMLSelectElement;
  await waitFor(() => expect(picker.options.length).toBeGreaterThan(1));
  return [...picker.options]
    .filter((option) => option.value !== "")
    .map((option) => option.textContent);
}

const listStatuses = () => state.listCalls.map((query) => query.get("status"));

beforeEach(() => {
  state.decide = [];
  state.decideAnswer = () => undefined;
  state.listCalls = [];
  state.pages = { staged: { items: [proposal()] } };
  state.reflect = [];
  state.reflectAnswer = () => undefined;
  state.runtime = { reflection: true };
  state.sessions = [];
  state.undo = [];
});

afterEach(() => {
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
  cleanup();
});

describe("learning review (pinned behaviour)", () => {
  it("lists pending proposals first and sends each filter's exact status token", async () => {
    const user = userEvent.setup();
    state.pages.promoted = { items: [proposal({ id: "proposal-2", status: "promoted" })] };
    renderPage();
    await row("Communication preference");
    expect(screen.getByText("Keep answers concise.")).toBeTruthy();
    expect(listStatuses()).toEqual(["staged"]);

    await user.click(screen.getByRole("button", { name: copy.promotedFilter }));
    await row("Communication preference");
    await user.click(screen.getByRole("button", { name: copy.rejectedFilter }));
    await waitFor(() => expect(listStatuses()).toEqual(["staged", "promoted", "rejected"]));
  });

  it("says when nothing is pending", async () => {
    state.pages.staged = { items: [] };
    renderPage();
    expect(await screen.findByText(copy.emptyPending)).toBeTruthy();
  });

  it("shows the daemon's reason when the queue is unsupported", async () => {
    state.pages.staged = {
      items: [],
      reason: "Learning proposals are not enabled on this Mecatl deployment.",
      supported: false,
    };
    renderPage();
    expect(
      await screen.findByText("Learning proposals are not enabled on this Mecatl deployment."),
    ).toBeTruthy();
  });

  it("confirms an approval before sending it with the loaded version, then refreshes", async () => {
    const user = userEvent.setup();
    renderPage();
    const item = await row("Communication preference");

    await user.click(item.getByRole("button", { name: copy.approve }));
    let dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Communication preference");
    await user.click(within(dialog).getByRole("button", { name: copy.cancel }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(state.decide).toEqual([]);

    await user.click(item.getByRole("button", { name: copy.approve }));
    dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: copy.confirm }));
    expect(await screen.findByText(copy.approved)).toBeTruthy();
    expect(state.decide).toEqual([
      { body: { decision: "approve", expectedVersion: "v1", reason: "" }, id: "proposal-1" },
    ]);
    await waitFor(() => expect(listStatuses()).toEqual(["staged", "staged"]));
  });

  it("confirms a rejection before sending it with the loaded version", async () => {
    const user = userEvent.setup();
    renderPage();
    const item = await row("Communication preference");
    await user.click(item.getByRole("button", { name: copy.reject }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Communication preference");
    await user.click(within(dialog).getByRole("button", { name: copy.confirm }));
    expect(await screen.findByText(copy.rejected)).toBeTruthy();
    expect(state.decide).toEqual([
      { body: { decision: "reject", expectedVersion: "v1", reason: "" }, id: "proposal-1" },
    ]);
  });

  it("confirms an undo before sending it with the loaded version", async () => {
    const user = userEvent.setup();
    state.pages.promoted = { items: [proposal({ status: "promoted", version: "v3" })] };
    renderPage();
    await row("Communication preference");
    await user.click(screen.getByRole("button", { name: copy.promotedFilter }));
    await waitFor(() => expect(listStatuses()).toContain("promoted"));
    const item = await row("Communication preference");
    expect(item.queryByRole("button", { name: copy.approve })).toBeNull();
    expect(item.queryByRole("button", { name: copy.reject })).toBeNull();

    await user.click(await item.findByRole("button", { name: copy.undo }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Communication preference");
    await user.click(within(dialog).getByRole("button", { name: copy.confirm }));
    expect(await screen.findByText(copy.undone)).toBeTruthy();
    expect(state.undo).toEqual([{ body: { expectedVersion: "v3" }, id: "proposal-1" }]);
  });

  it("disables Approve, not Reject, when the deployment cannot promote", async () => {
    state.pages.staged = {
      items: [
        proposal({ promotionAvailable: false, promotionUnavailableReason: "No memory target." }),
      ],
    };
    renderPage();
    const item = await row("Communication preference");
    expect(isDisabled(item.getByRole("button", { name: copy.approve }))).toBe(true);
    expect(isDisabled(item.getByRole("button", { name: copy.reject }))).toBe(false);
  });

  it("refreshes the queue after a version conflict and never retries the decision", async () => {
    const user = userEvent.setup();
    state.decideAnswer = () => ({
      code: "proposal_conflict",
      detail: "proposal version changed",
      status: 409,
    });
    renderPage();
    const item = await row("Communication preference");
    await user.click(item.getByRole("button", { name: copy.approve }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: copy.confirm }));
    expect(await screen.findByText(copy.conflict)).toBeTruthy();
    await waitFor(() => expect(listStatuses()).toEqual(["staged", "staged"]));
    expect(state.decide).toHaveLength(1);
    expect(screen.queryByText(copy.approved)).toBeNull();
  });

  it("shows a refused decision's detail", async () => {
    const user = userEvent.setup();
    state.decideAnswer = () => ({
      code: "failed_precondition",
      detail: "proposal evidence is unavailable or changed",
      status: 409,
    });
    renderPage();
    const item = await row("Communication preference");
    await user.click(item.getByRole("button", { name: copy.approve }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: copy.confirm }));
    expect(await screen.findByText("proposal evidence is unavailable or changed")).toBeTruthy();
    expect(screen.queryByText(copy.approved)).toBeNull();
  });

  it("reflects on a chosen completed chat, shows the receipt, and refreshes the queue", async () => {
    const user = userEvent.setup();
    state.sessions = [
      { id: "s-running", state: "running", title: "Still going" },
      { id: "s-done", state: "completed", title: "Finished chat" },
    ];
    renderPage();
    await row("Communication preference");
    expect(await sessionChoices()).toEqual(["Finished chat"]);
    expect(isDisabled(screen.getByRole("button", { name: copy.reflect }))).toBe(true);

    await chooseSession(user, "Finished chat");
    await user.click(screen.getByRole("button", { name: copy.reflect }));
    expect(await screen.findByText(copy.receipt)).toBeTruthy();
    expect(state.reflect).toEqual(["s-done"]);
    await waitFor(() => expect(listStatuses()).toEqual(["staged", "staged"]));
  });

  it("offers at most twenty completed chats", async () => {
    state.sessions = Array.from({ length: 25 }, (_, index) => ({
      id: `s-${index}`,
      state: "completed",
      title: `Chat ${index}`,
    }));
    renderPage();
    expect(await sessionChoices()).toHaveLength(20);
  });

  it("shows a failed reflection's detail", async () => {
    const user = userEvent.setup();
    state.sessions = [{ id: "s-done", state: "completed", title: "Finished chat" }];
    state.reflectAnswer = () => ({
      code: "failed_precondition",
      detail: "No eligible evidence was available for reflection.",
      status: 409,
    });
    renderPage();
    await sessionChoices();
    await chooseSession(user, "Finished chat");
    await user.click(screen.getByRole("button", { name: copy.reflect }));
    expect(
      await screen.findByText("No eligible evidence was available for reflection."),
    ).toBeTruthy();
  });

  it("says when there are no completed chats to reflect on", async () => {
    state.sessions = [{ id: "s-running", state: "running", title: "Still going" }];
    renderPage();
    expect(await screen.findByText(copy.noFinishedChats)).toBeTruthy();
  });

  it("explains that reflection is unsupported instead of offering it", async () => {
    state.runtime = { reflection: false };
    renderPage();
    expect(await screen.findByText(copy.reflectionOff)).toBeTruthy();
    expect(screen.queryByRole("button", { name: copy.reflect })).toBeNull();
  });
});
