// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { history, undo } from "@codemirror/commands";
import { ensureSyntaxTree } from "@codemirror/language";
import { EditorSelection, EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { client } from "@mecatl-studio/contracts/client";
import type { GetAuthSessionResponse } from "@mecatl-studio/contracts/generated";
import {
  getAuthSessionOptions,
  getPublicStatusOptions,
  getRuntimeOptions,
  getRuntimeSettingsOptions,
} from "@mecatl-studio/contracts/query";
import { getCM } from "@replit/codemirror-vim";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { clearUserScopedStorage, reconcileAccount } from "../../lib/account-storage";
import { setRequestRecoveryState } from "../../lib/api-client";
import { AuthGate } from "../auth/auth-gate";
import type { WriterSnapshot, WriterTransport } from "./writer-core";
import { WriterRecovery } from "./writer-recovery";
import { formatMarkdown, WriterWorkspace } from "./writer-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
const queries: QueryClient[] = [];
const requests: Array<{
  path: string;
  body: Parameters<WriterTransport["observe"]>[0] & { message?: string };
}> = [];
let answer:
  | { status: "observe"; text: string; quote?: string; quotes?: string[] }
  | { status: "silent" } = {
  status: "observe",
  text: "What evidence supports this assumption?",
};
let discussionAnswer:
  | { mode: "reply"; text: string }
  | { mode: "proposal"; text: string; candidate: string } = {
  mode: "reply",
  text: "Keep the author's own wording.",
};
let discussionResponse: (() => Promise<Response>) | undefined;
let runtimeFailure = false;
let discussionFailure = false;
let runtimeRequests = 0;
const fetchBff = async (request: Request) => {
  const path = new URL(request.url).pathname;
  if (path === "/api/v1/runtime") {
    runtimeRequests++;
    if (runtimeFailure) return Response.json({ error: "unavailable" }, { status: 503 });
    return Response.json({ experimentalWriter: true });
  }
  if (path === "/api/v1/settings/runtime") {
    return Response.json({
      modelsSupported: true,
      models: [{ id: "one", providerId: "provider", displayName: "One", image: false }],
    });
  }
  const body = await request.clone().json();
  requests.push({ path, body });
  if (path === "/api/v1/writer/observe") return Response.json(answer);
  if (path === "/api/v1/writer/discuss") {
    if (discussionFailure) return Response.json({ error: "unavailable" }, { status: 503 });
    return discussionResponse ? discussionResponse() : Response.json(discussionAnswer);
  }
  throw new Error(`Unexpected ${path}`);
};

function mount(
  enabled?: boolean,
  strict = false,
  auth?: { account?: string; showWriter?: boolean },
) {
  setRequestRecoveryState({ identityEpoch: 0, phase: "ready", workspaceMounted: true });
  vi.stubGlobal("fetch", fetchBff);
  client.setConfig({ baseUrl: "http://studio.test" });
  const query = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  queries.push(query);
  query.setQueryData<unknown>(getRuntimeOptions().queryKey, { experimentalWriter: enabled });
  query.setQueryData<unknown>(getRuntimeSettingsOptions().queryKey, {
    modelsSupported: true,
    models: [{ id: "one", providerId: "provider", displayName: "One", image: false }],
  });
  if (auth) {
    query.setQueryData<GetAuthSessionResponse>(
      getAuthSessionOptions().queryKey,
      auth.account
        ? { mode: "oidc", status: "authenticated", account: auth.account }
        : { mode: "none", status: "disabled" },
    );
    query.setQueryData(getPublicStatusOptions().queryKey, {
      connection: "reachable",
      signInRequired: false,
    });
  }
  const component = (showWriter: boolean) => (
    <QueryClientProvider client={query}>
      {auth ? (
        <AuthGate>{showWriter ? <WriterWorkspace /> : <span>Verified workspace</span>}</AuthGate>
      ) : (
        <WriterWorkspace />
      )}
    </QueryClientProvider>
  );
  const initial = component(auth?.showWriter !== false);
  const mounted = render(strict ? <StrictMode>{initial}</StrictMode> : initial);
  return { ...mounted, query, showWriter: () => mounted.rerender(component(true)) };
}
afterEach(() => {
  cleanup();
  for (const query of queries.splice(0)) query.clear();
  runtimeFailure = false;
  discussionFailure = false;
  runtimeRequests = 0;
  requests.length = 0;
  vi.restoreAllMocks();
  clearUserScopedStorage();
  Reflect.deleteProperty(navigator, "locks");
  answer = { status: "observe", text: "What evidence supports this assumption?" };
  discussionAnswer = { mode: "reply", text: "Keep the author's own wording." };
  discussionResponse = undefined;
  vi.unstubAllGlobals();
});

async function includeSelectedPassage() {
  await userEvent.click(screen.getByRole("button", { name: "Add context" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Use selected passage" }));
}

async function sendWriter(message: string) {
  await userEvent.type(screen.getByRole("textbox", { name: "Ask Writer" }), message);
  await userEvent.keyboard("{Enter}");
}

async function chooseDocument(file: File) {
  fireEvent.change(screen.getByLabelText("Choose document to open"), { target: { files: [file] } });
  await act(async () => Promise.resolve());
}

it("opens a fresh document without requesting analysis, resets editor history and all prior context", async () => {
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "Old draft" }, selection: { anchor: 0, head: 3 } }),
  );
  await includeSelectedPassage();
  fireEvent.click(screen.getByRole("button", { name: "Add brief" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Writing brief/ }), {
    target: { value: "Old brief" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply brief" }));
  fireEvent.change(screen.getByLabelText("Attach reference files"), {
    target: { files: [new File(["private"], "private.txt")] },
  });
  await screen.findByRole("button", { name: "Remove private.txt" });
  await sendWriter("Old discussion");
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  await chooseDocument(new File(["# New draft"], "new.md", { type: "text/markdown" }));
  expect(view.state.doc.toString()).toBe("# New draft");
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("# New draft");
  expect(screen.queryByText("Old brief")).toBeNull();
  expect(screen.queryByText("Old discussion")).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove private.txt" })).toBeNull();
  expect(screen.queryByLabelText("Remove selected passage")).toBeNull();
  expect(requests).toHaveLength(1);
  await sendWriter("New discussion");
  const next = requests.at(-1)?.body;
  expect(next).toMatchObject({ document: { content: "# New draft" }, discussion: [] });
  expect(JSON.stringify(next)).not.toContain("Old");
  expect(next).not.toHaveProperty("references");
  act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: "!" } }));
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("# New draft");
});

it("keeps Vim mode across a document boundary and rejects edits made during confirmation", async () => {
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() => view.dispatch({ changes: { from: 0, insert: "Old" } }));
  await userEvent.click(screen.getByRole("button", { name: "Document key bindings" }));
  await userEvent.click(screen.getByRole("menuitemradio", { name: "Vim" }));
  vi.stubGlobal(
    "confirm",
    vi.fn(() => {
      act(() => view.dispatch({ changes: { from: 3, insert: "!" } }));
      return true;
    }),
  );
  await chooseDocument(new File(["New"], "new.txt"));
  expect(view.state.doc.toString()).toBe("Old!");
  expect(screen.getByRole("alert").textContent).toContain("Draft changed");
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  await chooseDocument(new File(["New"], "new.txt"));
  expect(view.state.doc.toString()).toBe("New");
  expect(view.state.selection.main.anchor).toBe(0);
  expect(getCM(view)?.state.vim).toBeDefined();
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("New");
});

it("validates before confirmation and preserves the draft on errors, cancellation and stale reads", async () => {
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() => view.dispatch({ changes: { from: 0, insert: "Keep me" } }));
  const confirm = vi.fn().mockReturnValue(false);
  vi.stubGlobal("confirm", confirm);
  await chooseDocument(new File([new Uint8Array([0xff])], "bad.md"));
  expect(screen.getByRole("alert").textContent).toContain("valid UTF-8");
  expect(confirm).not.toHaveBeenCalled();
  await chooseDocument(new File(["new"], "good.txt"));
  expect(confirm).toHaveBeenCalledOnce();
  expect(view.state.doc.toString()).toBe("Keep me");
  let release!: (bytes: ArrayBuffer) => void;
  const slow = new File(["new"], "slow.md");
  vi.spyOn(slow, "arrayBuffer").mockReturnValue(
    new Promise((resolve) => {
      release = resolve;
    }),
  );
  fireEvent.change(screen.getByLabelText("Choose document to open"), { target: { files: [slow] } });
  act(() => view.dispatch({ changes: { from: 7, insert: "!" } }));
  await act(async () => {
    release(new TextEncoder().encode("new").buffer);
    await Promise.resolve();
  });
  expect(view.state.doc.toString()).toBe("Keep me!");
  expect(screen.getByRole("alert").textContent).toContain("Draft changed");
  expect(confirm).toHaveBeenCalledOnce();
  expect(requests).toHaveLength(0);
});

it("rejects invalid files through Open document without confirming or changing existing context", async () => {
  mount(true);
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  const view = writerView();
  act(() => view.dispatch({ changes: { from: 0, insert: "Keep the draft." } }));
  fireEvent.click(screen.getByRole("button", { name: "Add brief" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Writing brief/ }), {
    target: { value: "Keep brief" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply brief" }));
  fireEvent.change(screen.getByLabelText("Attach reference files"), {
    target: { files: [new File(["Keep reference"], "keep.txt")] },
  });
  await screen.findByRole("button", { name: "Remove keep.txt" });
  await sendWriter("Keep discussion");
  await screen.findByText("Keep the author's own wording.");
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("What evidence supports this assumption?");
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Decision for this draft/ }), {
    target: { value: "Keep decision" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save decision" }));
  const confirm = vi.fn();
  vi.stubGlobal("confirm", confirm);
  const before = requests.length;
  const rejected = [
    [new File(["new"], "wrong.pdf", { type: "text/plain" }), /Choose/],
    [new File(["new\u0085"], "controls.txt"), /control/],
    [new File(["é".repeat(200_001)], "large.md"), /bytes/],
    [new File(["a".repeat(100_001)], "long.txt"), /characters/],
  ] as const;
  for (const [file, reason] of rejected) {
    await chooseDocument(file);
    expect(screen.getByRole("alert").textContent).toMatch(reason);
    expect(confirm).not.toHaveBeenCalled();
    expect(requests).toHaveLength(before);
    expect(view.state.doc.toString()).toBe("Keep the draft.");
    expect(screen.getByText("Keep brief")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove keep.txt" })).toBeTruthy();
    expect(screen.getAllByText("What evidence supports this assumption?").length).toBeGreaterThan(
      0,
    );
    expect(
      screen.getByRole<HTMLInputElement>("textbox", { name: /Decision for this draft/ }).value,
    ).toBe("Keep decision");
    fireEvent.click(screen.getByRole("button", { name: "← Back to conversation" }));
    expect(screen.getByText("Keep discussion")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  }
  confirm.mockReturnValue(true);
  await chooseDocument(new File(["\ufeff"], "empty.md"));
  expect(confirm).toHaveBeenCalledOnce();
  expect(view.state.doc.toString()).toBe("");
  expect(requests).toHaveLength(before);
});

it("isolates overlapping document reads and pending references across an import", async () => {
  const mounted = mount(true);
  const view = writerView();
  let release!: (bytes: ArrayBuffer) => void;
  const slow = new File(["first"], "first.md");
  vi.spyOn(slow, "arrayBuffer").mockReturnValue(
    new Promise((resolve) => {
      release = resolve;
    }),
  );
  fireEvent.change(screen.getByLabelText("Choose document to open"), { target: { files: [slow] } });
  await chooseDocument(new File(["second"], "second.md"));
  await act(async () => {
    release(new TextEncoder().encode("first").buffer);
    await Promise.resolve();
  });
  expect(view.state.doc.toString()).toBe("second");
  let releaseReference!: (bytes: ArrayBuffer) => void;
  const reference = new File(["old reference"], "old.txt");
  vi.spyOn(reference, "arrayBuffer").mockReturnValue(
    new Promise((resolve) => {
      releaseReference = resolve;
    }),
  );
  fireEvent.change(screen.getByLabelText("Attach reference files"), {
    target: { files: [reference] },
  });
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  await chooseDocument(new File(["third"], "third.md"));
  await act(async () => {
    releaseReference(new TextEncoder().encode("old reference").buffer);
    await Promise.resolve();
  });
  expect(screen.queryByRole("button", { name: "Remove old.txt" })).toBeNull();
  mounted.unmount();
  expect(requests).toHaveLength(0);
});

it("drops an unfinished document read after account switch", async () => {
  const tab = mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  let release!: (bytes: ArrayBuffer) => void;
  const slow = new File(["Alice's draft"], "alice.md");
  vi.spyOn(slow, "arrayBuffer").mockReturnValue(
    new Promise((resolve) => {
      release = resolve;
    }),
  );
  fireEvent.change(screen.getByLabelText("Choose document to open"), { target: { files: [slow] } });
  const session: GetAuthSessionResponse = {
    mode: "oidc",
    status: "authenticated",
    account: "bob",
  };
  act(() => tab.query.setQueryData(getAuthSessionOptions().queryKey, session));
  await waitFor(() => expect(window.localStorage.getItem("studio.account")).toBe("bob"));
  await screen.findByRole("textbox", { name: "Writer document" });
  await act(async () => {
    release(new TextEncoder().encode("Alice's draft").buffer);
    await Promise.resolve();
  });
  expect(writerView().state.doc.toString()).toBe("");
  expect(requests).toHaveLength(0);
});

it.each(["observe", "discuss"] as const)(
  "keeps an unsettled old %s request behind the import barrier",
  async (kind) => {
    let finish!: (result: Response) => void;
    if (kind === "discuss") {
      discussionResponse = () =>
        new Promise((resolve) => {
          finish = resolve;
        });
    }
    mount(true);
    if (kind === "observe") {
      const original = globalThis.fetch;
      vi.stubGlobal("fetch", (request: Request) => {
        if (new URL(request.url).pathname.endsWith("/writer/observe"))
          return original(request).then(
            () =>
              new Promise<Response>((resolve) => {
                finish = resolve;
              }),
          );
        return original(request);
      });
      fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
    } else {
      await sendWriter("Old question");
    }
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    expect(requests).toHaveLength(1);
    await chooseDocument(new File(["Fresh draft."], "fresh.txt"));
    expect(writerView().state.doc.toString()).toBe("Fresh draft.");
    expect(requests).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Read this now" })).toHaveProperty("disabled", true);
    if (kind === "observe") await sendWriter("Fresh question");
    expect(requests).toHaveLength(1);
    await act(async () => {
      finish(
        kind === "observe"
          ? Response.json({ status: "observe", text: "Old observation" })
          : Response.json({ mode: "reply", text: "Old answer" }),
      );
    });
    expect(screen.queryByText("Old observation")).toBeNull();
    expect(screen.queryByText("Old answer")).toBeNull();
    expect(screen.queryByText("Old question")).toBeNull();
    if (kind === "discuss") {
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Read this now" })).toHaveProperty(
          "disabled",
          false,
        ),
      );
      fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
    }
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[1]?.body.document.content).toBe("Fresh draft.");
    expect(JSON.stringify(requests[1]?.body)).not.toContain("Old");
    await new Promise((resolve) => setTimeout(resolve, 1600));
    expect(requests).toHaveLength(2);
  },
);

it("previews only from conversation, rejects stale replies and brief changes", async () => {
  let finish!: (result: Response) => void;
  discussionResponse = () =>
    new Promise<Response>((resolve) => {
      finish = resolve;
    });
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "First" }, selection: { anchor: 0, head: 5 } }),
  );
  await includeSelectedPassage();
  await sendWriter("Please rewrite the selected passage");
  await waitFor(() =>
    expect(requests.filter((item) => item.path.endsWith("/discuss"))).toHaveLength(1),
  );
  expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
  act(() => view.dispatch({ changes: { from: 5, insert: " changed" } }));
  finish(Response.json({ mode: "proposal", text: "Better", candidate: "New" }));
  await waitFor(() => expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull());
  discussionResponse = undefined;
  discussionAnswer = { mode: "proposal", text: "Better", candidate: "New" };
  act(() => view.dispatch({ selection: { anchor: 0, head: 5 } }));
  await includeSelectedPassage();
  await sendWriter("Please rewrite this");
  await screen.findByRole("region", { name: "Writer preview" });
  fireEvent.click(screen.getByRole("button", { name: "Add brief" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Writing brief/ }), {
    target: { value: "Audience: editors" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply brief" }));
  expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
  expect(view.state.doc.toString()).toBe("First changed");
});

it("discusses a selected passage without mutation, refines and applies in one undoable transaction", async () => {
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() =>
    view.dispatch({
      changes: { from: 0, insert: "Same. Same." },
      selection: { anchor: 6, head: 10 },
    }),
  );
  await includeSelectedPassage();
  await sendWriter("What is unclear here?");
  expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
  discussionAnswer = { mode: "proposal", text: "Clearer", candidate: "Better" };
  await sendWriter("Please revise this passage");
  await screen.findByRole("region", { name: "Writer preview" });
  expect(requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body).toMatchObject({
    passage: { from: 6, to: 10, text: "Same" },
  });
  expect(view.state.doc.toString()).toBe("Same. Same.");
  fireEvent.change(screen.getByRole("textbox", { name: "After (editable)" }), {
    target: { value: "Edited" },
  });
  discussionAnswer = { mode: "proposal", text: "Refined", candidate: "Final" };
  await sendWriter("Refine the candidate");
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLTextAreaElement>("textbox", { name: "After (editable)" }).value,
    ).toBe("Final"),
  );
  expect(requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body).toMatchObject({
    previousCandidate: "Edited",
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply" }));
  expect(view.state.doc.toString()).toBe("Same. Final.");
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("Same. Same.");
  fireEvent.click(screen.getByRole("button", { name: "Redo" }));
  expect(view.state.doc.toString()).toBe("Same. Final.");
});

it("keeps an author's preview edits when a late refinement arrives, and permits a later reply", async () => {
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "Original" }, selection: { anchor: 0, head: 8 } }),
  );
  discussionAnswer = { mode: "proposal", text: "Preview", candidate: "Initial" };
  await includeSelectedPassage();
  await sendWriter("Rewrite this passage");
  await screen.findByRole("region", { name: "Writer preview" });
  let finish!: (value: Response) => void;
  discussionResponse = () =>
    new Promise<Response>((resolve) => {
      finish = resolve;
    });
  await sendWriter("Refine the candidate");
  await waitFor(() =>
    expect(requests.filter((item) => item.path.endsWith("/discuss"))).toHaveLength(2),
  );
  fireEvent.change(screen.getByRole("textbox", { name: "After (editable)" }), {
    target: { value: "Author revision" },
  });
  finish(Response.json({ mode: "proposal", text: "Late", candidate: "Stale" }));
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLTextAreaElement>("textbox", { name: "After (editable)" }).value,
    ).toBe("Author revision"),
  );
  expect(screen.queryByText("Late")).toBeNull();
  discussionResponse = undefined;
  discussionAnswer = { mode: "reply", text: "Consider the evidence." };
  await sendWriter("What evidence would help?");
  await screen.findByText("Consider the evidence.");
  expect(screen.getByRole<HTMLTextAreaElement>("textbox", { name: "After (editable)" }).value).toBe(
    "Author revision",
  );
  fireEvent.click(screen.getByRole("button", { name: "Apply" }));
  expect(view.state.doc.toString()).toBe("Author revision");
});

it("offers empty-document starting points through general conversation only", async () => {
  mount(true);
  const view = writerView();
  discussionAnswer = {
    mode: "proposal",
    text: "An outline",
    candidate: "# Outline\n\n[Evidence needed]",
  };
  await sendWriter("Outline this idea, retaining unsupported evidence as questions");
  await screen.findByRole("region", { name: "Writer preview" });
  expect(requests.find((item) => item.path.endsWith("/discuss"))?.body).toMatchObject({
    document: { content: "" },
  });
  expect(view.state.doc.toString()).toBe("");
  fireEvent.click(screen.getByRole("button", { name: "Discard" }));
  expect(view.state.doc.toString()).toBe("");
  await sendWriter("Make a rough draft from this idea");
  await screen.findByRole("region", { name: "Writer preview" });
  fireEvent.click(screen.getByRole("button", { name: "Use this starting point" }));
  expect(view.state.doc.toString()).toBe("# Outline\n\n[Evidence needed]");
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("");
});

it("attaches only explicit bounded snapshots, sends exact references on a later request, and invalidates a sourced preview on removal", async () => {
  mount(true);
  const picker = screen.getByLabelText("Attach reference files");
  fireEvent.change(picker, {
    target: { files: [new File(["<unsafe> & data"], "notes.md", { type: "text/markdown" })] },
  });
  await screen.findByRole("button", { name: "Remove notes.md" });
  expect(requests).toHaveLength(0);
  discussionAnswer = { mode: "proposal", text: "Outline", candidate: "# Draft" };
  await sendWriter("Outline from attached notes");
  await screen.findByRole("region", { name: "Writer preview" });
  expect(requests.find((entry) => entry.path.endsWith("/discuss"))?.body.references).toEqual([
    { name: "notes.md", content: "<unsafe> & data" },
  ]);
  expect(screen.getByText("notes.md")).toBeTruthy();
  expect(screen.queryByText("<unsafe> & data")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Remove notes.md" }));
  expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
  await sendWriter("Another question");
  expect(requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body).not.toHaveProperty(
    "references",
  );
  fireEvent.change(picker, {
    target: { files: [new File([new Uint8Array([0xff])], "binary.txt", { type: "text/plain" })] },
  });
  expect(await screen.findByText(/Reference must be valid UTF-8 text/)).toBeTruthy();
});

it.each(["reference read", "discussion response"] as const)(
  "keeps %s from a previous verified account out of the new workspace",
  async (pending) => {
    const tab = mount(true, false, { account: "alice" });
    await screen.findByRole("textbox", { name: "Writer document" });
    fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
    const picker = screen.getByLabelText("Attach reference files");
    const priorContent = "Alice private evidence";
    let finishRead: ((value: ArrayBuffer) => void) | undefined;
    let finishDiscussion: ((value: Response) => void) | undefined;
    if (pending === "reference read") {
      const slow = new File([priorContent], "alice.txt", { type: "text/plain" });
      vi.spyOn(slow, "arrayBuffer").mockReturnValue(
        new Promise((resolve) => {
          finishRead = resolve;
        }),
      );
      fireEvent.change(picker, { target: { files: [slow] } });
      expect(
        (screen.getByRole("button", { name: "Read this now" }) as HTMLButtonElement).disabled,
      ).toBe(true);
    } else {
      fireEvent.change(picker, {
        target: { files: [new File([priorContent], "alice.txt", { type: "text/plain" })] },
      });
      await screen.findByRole("button", { name: "Remove alice.txt" });
      discussionResponse = () =>
        new Promise<Response>((resolve) => {
          finishDiscussion = resolve;
        });
      await sendWriter("Outline from my notes");
      await waitFor(() => expect(finishDiscussion).toBeTypeOf("function"));
      expect(requests.findLast((item) => item.path.endsWith("/discuss"))?.body.references).toEqual([
        { name: "alice.txt", content: priorContent },
      ]);
    }
    act(() => {
      const session: GetAuthSessionResponse = {
        mode: "oidc",
        status: "authenticated",
        account: "bob",
      };
      tab.query.setQueryData(getAuthSessionOptions().queryKey, session);
    });
    await waitFor(() => expect(window.localStorage.getItem("studio.account")).toBe("bob"));
    await screen.findByRole("textbox", { name: "Writer document" });
    const sentBeforeRelease = requests.length;
    await act(async () => {
      finishRead?.(new TextEncoder().encode(priorContent).buffer);
      finishDiscussion?.(
        Response.json({ mode: "proposal", text: priorContent, candidate: priorContent }),
      );
      await Promise.resolve();
    });
    expect(screen.queryByRole("button", { name: "Remove alice.txt" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
    expect(document.body.textContent).not.toContain(priorContent);
    expect(requests).toHaveLength(sentBeforeRelease);
    expect(writerView().state.doc.toString()).toBe("");
    await sendWriter("What evidence is available?");
    const next = requests.findLast((item) => item.path.endsWith("/discuss"));
    expect(next?.body).not.toHaveProperty("references");
    expect(JSON.stringify(next?.body)).not.toContain(priorContent);
    tab.query.clear();
  },
);

it("drops reads in progress after removal or unmount without sending content", async () => {
  const mounted = mount(true);
  const picker = screen.getByLabelText("Attach reference files");
  fireEvent.change(picker, {
    target: { files: [new File(["kept"], "kept.txt", { type: "text/plain" })] },
  });
  await screen.findByRole("button", { name: "Remove kept.txt" });
  let finish!: (value: ArrayBuffer) => void;
  const slow = new File(["secret"], "slow.txt", { type: "text/plain" });
  vi.spyOn(slow, "arrayBuffer").mockReturnValue(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  fireEvent.change(picker, { target: { files: [slow] } });
  expect(
    (screen.getByRole("button", { name: "Read this now" }) as HTMLButtonElement).disabled,
  ).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Remove kept.txt" }));
  finish(new TextEncoder().encode("secret").buffer);
  await act(async () => Promise.resolve());
  expect(screen.queryByRole("button", { name: "Remove slow.txt" })).toBeNull();
  expect(requests).toHaveLength(0);
  const slowUnmount = new File(["secret"], "unmount.txt", { type: "text/plain" });
  let finishUnmount!: (value: ArrayBuffer) => void;
  vi.spyOn(slowUnmount, "arrayBuffer").mockReturnValue(
    new Promise((resolve) => {
      finishUnmount = resolve;
    }),
  );
  fireEvent.change(picker, { target: { files: [slowUnmount] } });
  mounted.unmount();
  finishUnmount(new TextEncoder().encode("secret").buffer);
  await act(async () => Promise.resolve());
  expect(requests).toHaveLength(0);
});

it("shows an empty Markdown surface with theme-aware styles and formats selected text as one undoable edit", () => {
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  expect(screen.getByText("What would you like to write?")).toBeTruthy();
  expect(editor.closest(".cm-editor")?.parentElement?.className).toContain("bg-background");
  const styles = [...document.querySelectorAll("style")]
    .map((style) => style.textContent)
    .join(" ");
  expect(styles).toContain("var(--primary)");
  expect(styles).toContain("var(--accent)");
  expect(styles).toContain("cm-focused");
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "Draft" }, selection: { anchor: 0, head: 5 } }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Format bold" }));
  expect(view.state.doc.toString()).toBe("**Draft**");
  expect(view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to)).toBe(
    "Draft",
  );
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("Draft");
  act(() => view.dispatch({ selection: { anchor: 0, head: 5 } }));
  fireEvent.keyDown(editor, { key: "i", ctrlKey: true });
  expect(view.state.doc.toString()).toBe("*Draft*");
  fireEvent.click(screen.getByRole("button", { name: "Format heading" }));
  expect(view.state.doc.toString()).toBe("# *Draft*");
  expect(ensureSyntaxTree(view.state, view.state.doc.length, 1000)?.toString()).toContain(
    "ATXHeading1",
  );
  expect(editor.querySelector(".cm-line span")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("*Draft*");
  act(() => view.dispatch({ selection: { anchor: view.state.doc.length } }));
  fireEvent.click(screen.getByRole("button", { name: "Format bold" }));
  expect(view.state.doc.toString()).toBe("*Draft*****");
  expect(view.state.selection.main.from).toBe(view.state.doc.length - 2);
});

it("offers visible brief and automatic-feedback controls", () => {
  mount(true);
  const automaticFeedback = screen.getByRole("switch", { name: "Automatic feedback" });
  expect(automaticFeedback.getAttribute("data-state")).toBe("checked");
  fireEvent.click(automaticFeedback);
  expect(automaticFeedback.getAttribute("data-state")).toBe("unchecked");
  expect(screen.getByText("Off: checks run only when you select Read this now.")).toBeTruthy();

  const addBrief = screen.getByRole("button", { name: "Add brief" });
  expect(addBrief.className).toContain("border");
  addBrief.focus();
  expect(document.activeElement).toBe(addBrief);
  fireEvent.click(addBrief);
  expect(screen.getByRole("textbox", { name: /Writing brief/ })).toBeTruthy();
});

it("keeps the Writer composer concise and supports keyboard context and thread navigation", async () => {
  mount(true);
  expect(screen.queryByRole("button", { name: "Ask Writer" })).toBeNull();
  expect(screen.queryByRole("heading", { name: /general discussion/i })).toBeNull();
  expect(screen.getByRole("heading", { name: "Document" })).toBeTruthy();
  const conversation = screen.getByRole("complementary", { name: "Writer conversation" });
  expect(within(conversation).getByRole("heading", { name: "Writer" })).toBeTruthy();
  expect(screen.queryByText("Start writing to receive occasional observations")).toBeNull();
  expect(screen.queryByText("Start with a question or an outline.")).toBeNull();
  expect(screen.getAllByText("Observations will appear here as you write.")).toHaveLength(1);
  expect(screen.getByRole("textbox", { name: "Ask Writer" }).getAttribute("placeholder")).toBe(
    "Ask a question or describe a change…",
  );

  const view = writerView();
  act(() =>
    view.dispatch({
      changes: { from: 0, insert: "Selected passage" },
      selection: { anchor: 0 },
    }),
  );
  const context = screen.getByRole("button", { name: "Add context" });
  context.focus();
  await userEvent.keyboard("{Enter}");
  expect(screen.getByText(/File contents are sent with Writer requests/)).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Attach reference files" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: "Use selected passage" })).toBeNull();
  await userEvent.keyboard("{Escape}");

  act(() => view.dispatch({ selection: { anchor: 0, head: 8 } }));
  await userEvent.click(context);
  expect(screen.getByRole("menuitem", { name: "Use selected passage" })).toBeTruthy();
  await userEvent.click(screen.getByRole("menuitem", { name: "Use selected passage" }));
  expect(screen.getByText("Passage: Selected")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "Remove selected passage" }));
  expect(screen.queryByText("Passage: Selected")).toBeNull();

  act(() => view.dispatch({ selection: { anchor: 8 } }));
  await userEvent.click(context);
  expect(screen.queryByRole("menuitem", { name: "Use selected passage" })).toBeNull();
  await userEvent.keyboard("{Escape}");

  answer = { status: "observe", text: "Thread context" };
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("Thread context");
  await userEvent.click(screen.getByRole("button", { name: "Open thread" }));
  const requestCount = requests.length;
  await userEvent.click(screen.getByRole("button", { name: "← Back to conversation" }));
  expect(requests).toHaveLength(requestCount);
  expect(screen.queryByRole("button", { name: "← Back to conversation" })).toBeNull();
});

it("switches document key bindings in place without losing selection, history, or reference snapshots", async () => {
  mount(true);
  const view = writerView();
  expect(getCM(view)).toBeNull();
  await userEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "Draft" }, selection: { anchor: 5, head: 0 } }),
  );
  const picker = screen.getByLabelText("Attach reference files");
  fireEvent.change(picker, {
    target: { files: [new File(["Evidence"], "evidence.md", { type: "text/markdown" })] },
  });
  await screen.findByRole("button", { name: "Remove evidence.md" });
  const requestCount = requests.length;
  const settings = screen.getByRole("button", { name: "Document key bindings" });
  await userEvent.click(settings);
  await userEvent.click(screen.getByRole("menuitemradio", { name: "Vim" }));
  expect(writerView()).toBe(view);
  expect(getCM(view)?.state.vim).toBeTruthy();
  expect(view.state.doc.toString()).toBe("Draft");
  expect(view.state.selection.main).toMatchObject({ anchor: 5, head: 0 });
  expect(requests).toHaveLength(requestCount);
  expect(screen.getByRole("button", { name: "Remove evidence.md" })).toBeTruthy();
  act(() =>
    view.dispatch({
      selection: EditorSelection.create([EditorSelection.range(0, 1), EditorSelection.range(3, 4)]),
    }),
  );
  expect(view.state.selection.ranges).toHaveLength(2);
  await userEvent.click(settings);
  await userEvent.click(screen.getByRole("menuitemradio", { name: "Standard" }));
  expect(getCM(view)).toBeNull();
  expect(view.state.selection.ranges.map(({ from, to }) => ({ from, to }))).toEqual([
    { from: 0, to: 1 },
    { from: 3, to: 4 },
  ]);
  expect(screen.queryByRole("status", { name: /Document Vim mode/ })).toBeNull();
  expect(requests).toHaveLength(requestCount);
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await waitFor(() => expect(requests).toHaveLength(requestCount + 1));
  expect(requests.at(-1)?.body).toMatchObject({
    document: { content: "Draft", revision: 1 },
    references: [{ name: "evidence.md", content: "Evidence" }],
  });
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("");
  fireEvent.click(screen.getByRole("button", { name: "Redo" }));
  expect(view.state.doc.toString()).toBe("Draft");
});

it("captures reversed contiguous Vim selections but never captures a visual block", async () => {
  mount(true);
  const view = writerView();
  act(() => view.dispatch({ changes: { from: 0, insert: "first line\nsecond line" } }));
  const settings = screen.getByRole("button", { name: "Document key bindings" });
  await userEvent.click(settings);
  await userEvent.click(screen.getByRole("menuitemradio", { name: "Vim" }));
  const cm = getCM(view);
  if (!cm?.state.vim) throw new Error("Vim adapter not mounted");
  act(() => view.dispatch({ selection: { anchor: 10, head: 0 } }));
  await includeSelectedPassage();
  expect(screen.getByText("Passage: first line")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "Remove selected passage" }));
  cm.state.vim.visualBlock = true;
  await userEvent.click(screen.getByRole("button", { name: "Add context" }));
  expect(screen.queryByRole("menuitem", { name: "Use selected passage" })).toBeNull();
  expect(screen.getByText(/Select one contiguous passage/)).toBeTruthy();
});

it.each([
  ["heading", "# Draft"],
  ["bold", "**Draft**"],
  ["italic", "*Draft*"],
])("toggles %s without nesting markers and preserves selection and undo", (kind, formatted) => {
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "Draft" }, selection: { anchor: 0, head: 5 } }),
  );
  const button = screen.getByRole("button", { name: `Format ${kind}` });
  fireEvent.click(button);
  expect(view.state.doc.toString()).toBe(formatted);
  fireEvent.click(button);
  expect(view.state.doc.toString()).toBe("Draft");
  expect(view.state.selection.main.from).toBe(0);
  expect(view.state.selection.main.to).toBe(5);
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe(formatted);
  fireEvent.click(screen.getByRole("button", { name: "Redo" }));
  expect(view.state.doc.toString()).toBe("Draft");
});

it("maps a collapsed column-zero heading caret across toggles and undo", () => {
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  const heading = screen.getByRole("button", { name: "Format heading" });

  act(() => view.dispatch({ changes: { from: 0, insert: "Draft" }, selection: { anchor: 0 } }));
  fireEvent.click(heading);
  expect(view.state.doc.toString()).toBe("# Draft");
  expect(view.state.selection.main).toMatchObject({ anchor: 2, head: 2 });
  fireEvent.click(heading);
  expect(view.state.doc.toString()).toBe("Draft");
  expect(view.state.selection.main).toMatchObject({ anchor: 0, head: 0 });
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("# Draft");
  expect(view.state.selection.main).toMatchObject({ anchor: 2, head: 2 });
});

it("maps a backwards full-line heading selection across toggles and undo", () => {
  let state = EditorState.create({
    doc: "Draft",
    selection: EditorSelection.single(5, 0),
    extensions: [history()],
  });
  const view = {
    get state() {
      return state;
    },
    get hasFocus() {
      return true;
    },
    dispatch(spec: Parameters<EditorState["update"]>[0]) {
      state = state.update(spec).state;
    },
  } as unknown as EditorView;

  formatMarkdown(view, "heading");
  expect(view.state.doc.toString()).toBe("# Draft");
  expect(view.state.selection.main).toMatchObject({ anchor: 7, head: 2 });
  formatMarkdown(view, "heading");
  expect(view.state.doc.toString()).toBe("Draft");
  expect(view.state.selection.main).toMatchObject({ anchor: 5, head: 0 });
  expect(undo(view)).toBe(true);
  expect(view.state.doc.toString()).toBe("# Draft");
  expect(view.state.selection.main).toMatchObject({ anchor: 7, head: 2 });
});

it("uses the selected inventory model for observation and discussion", async () => {
  mount(true);
  const picker = screen.getByRole("button", { name: "Writer model" });
  expect(picker.textContent).toContain("Deployment default");
  fireEvent.pointerDown(picker, { button: 0, ctrlKey: false });
  fireEvent.change(screen.getByRole("textbox", { name: "Filter models" }), {
    target: { value: "one" },
  });
  fireEvent.click(screen.getByRole("menuitem", { name: "One" }));
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() => view.dispatch({ changes: { from: 0, insert: "Draft" } }));
  await screen.findByText("What evidence supports this assumption?", {}, { timeout: 3500 });
  expect(requests[0]?.body.model).toEqual({ id: "one", providerId: "provider" });
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Ask Writer" }), "Why?");
  await userEvent.keyboard("{Enter}");
  await waitFor(() =>
    expect(requests[1]?.body.model).toEqual({ id: "one", providerId: "provider" }),
  );
});

it("fails closed without the runtime flag", () => {
  mount(false);
  expect(screen.getByText("Writer is unavailable.")).toBeTruthy();
  expect(screen.queryByRole("textbox", { name: "Writer document" })).toBeNull();
});

it("retains the editor and draft when runtime invalidation fails or the flag is disabled", async () => {
  const { query } = mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "Unsaved draft" }, userEvent: "input.type" }),
  );
  runtimeFailure = true;
  await act(async () => {
    await query.invalidateQueries({ queryKey: getRuntimeOptions().queryKey });
  });
  await screen.findByText(/Requests are suspended/);
  expect(screen.getByRole("textbox", { name: "Writer document" })).toBe(editor);
  expect(EditorView.findFromDOM(editor as HTMLElement)).toBe(view);
  expect(editor.textContent).toBe("Unsaved draft");
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 1600));
  });
  expect(requests).toHaveLength(0);
  expect(runtimeRequests).toBe(1);
  act(() =>
    query.setQueryData<unknown>(getRuntimeOptions().queryKey, { experimentalWriter: false }),
  );
  await waitFor(() => expect(screen.getByText(/Requests are suspended/)).toBeTruthy());
  expect(editor.textContent).toBe("Unsaved draft");
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("");
});

it("keeps the editor usable after StrictMode effect remount", async () => {
  mount(true, true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() =>
    view.dispatch({ changes: { from: 0, insert: "Strict draft" }, userEvent: "input.type" }),
  );
  expect(editor.textContent).toBe("Strict draft");
  await waitFor(() => expect(requests[0]?.body.document.content).toBe("Strict draft"), {
    timeout: 3500,
  });
});

it("sends author edits via the generated SDK, never inserts model text, and supports undo", async () => {
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() => view.dispatch({ changes: { from: 0, insert: "Opening" }, userEvent: "input.type" }));
  expect(editor.textContent).toContain("Opening");
  await waitFor(() => expect(requests.some(({ path }) => path.endsWith("/observe"))).toBe(true), {
    timeout: 3500,
  });
  expect(requests.map((r) => r.body.document?.content)).toEqual(["Opening"]);
  expect(await screen.findByText("What evidence supports this assumption?")).toBeTruthy();
  expect(editor.textContent).toBe("Opening");
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Ask Writer" }));
  act(() => view.dispatch({ changes: { from: 7, insert: " updated" }, userEvent: "input.type" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Ask Writer" }), "How?");
  await userEvent.keyboard("{Enter}");
  await waitFor(() => expect(requests.some(({ path }) => path.endsWith("/discuss"))).toBe(true));
  expect(requests.find(({ path }) => path.endsWith("/discuss"))?.body).toMatchObject({
    document: { content: "Opening updated" },
    observations: [
      {
        revision: 1,
        status: "open",
        text: "What evidence supports this assumption?",
        selected: true,
      },
    ],
    message: "How?",
  });
  expect(await screen.findByText("Keep the author's own wording.")).toBeTruthy();
  expect(screen.getByRole("region", { name: "Discussion" }).textContent).toContain(
    "What evidence supports this assumption?",
  );
  expect(editor.textContent).toBe("Opening updated");
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(editor.textContent).toBe("Opening");
});

it("dismisses an observation without changing the document", async () => {
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() =>
    view.dispatch({
      changes: { from: 0, insert: "Draft" },
      userEvent: "input.type",
    }),
  );
  await screen.findByText("What evidence supports this assumption?", {}, { timeout: 3500 });
  fireEvent.click(screen.getByRole("button", { name: "Not relevant" }));
  expect(screen.getByText("Observations will appear here as you write.")).toBeTruthy();
  expect(editor.textContent).toBe("Draft");
});

it("recovers observation after a failed discussion is dismissed without losing the draft", async () => {
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() => view.dispatch({ changes: { from: 0, insert: "Draft" } }));
  await screen.findByText("What evidence supports this assumption?", {}, { timeout: 3500 });
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  discussionFailure = true;
  await userEvent.type(screen.getByRole("textbox", { name: "Ask Writer" }), "Why?");
  await userEvent.keyboard("{Enter}");
  await screen.findByText(/Discussion failed/);
  fireEvent.click(screen.getByRole("button", { name: "Not relevant" }));
  expect(screen.getByRole("region", { name: "Discussion" })).toBeTruthy();
  act(() => view.dispatch({ changes: { from: 5, insert: " revised" } }));
  answer = { status: "silent" };
  fireEvent.click(screen.getByRole("button", { name: "Retry analysis" }));
  await waitFor(
    () => expect(requests.filter(({ path }) => path.endsWith("/observe"))).toHaveLength(2),
    { timeout: 3500 },
  );
  expect(editor.textContent).toBe("Draft revised");
  expect(screen.queryByRole("alert")).toBeNull();
});

it("uses a brief, explicit pause/read, revisitable decisions, and cautious quote reveal", async () => {
  answer = { status: "observe", text: "Why the expense?", quote: "Costs" } as typeof answer;
  mount(true);
  fireEvent.click(screen.getByRole("button", { name: "Add brief" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Writing brief/ }), {
    target: { value: "Audience: operators" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply brief" }));
  expect(requests).toHaveLength(0);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() => view.dispatch({ changes: { from: 0, insert: "Costs are unknown." } }));
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("Why the expense?");
  expect(requests[0]?.body).toMatchObject({ brief: "Audience: operators" });
  act(() => view.dispatch({ selection: { anchor: 4 } }));
  fireEvent.click(screen.getByRole("button", { name: "Reveal passage" }));
  expect(view.state.selection.main.anchor).toBe(4);
  expect(editor.querySelector(".cm-writer-passage")?.textContent).toBe("Costs");
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  const decision = screen.getByRole("textbox", { name: /Decision for this draft/ });
  await userEvent.type(decision, "Costs outside scope");
  await userEvent.keyboard("{Enter}");
  expect(screen.getByText("Decision saved.")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Addressed" }));
  expect(screen.getByText("History · 1 closed threads").closest("details")?.open).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "← Back to conversation" }));
  fireEvent.click(screen.getByText("History · 1 closed threads"));
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  const closedDecision = screen.getByRole<HTMLInputElement>("textbox", {
    name: "Decision for this draft",
  });
  expect(closedDecision.value).toBe("Costs outside scope");
  await userEvent.clear(closedDecision);
  await userEvent.type(closedDecision, "Saved with the button");
  fireEvent.click(screen.getByRole("button", { name: "Save decision" }));
  expect(screen.getByText("Decision saved.")).toBeTruthy();
  expect(closedDecision.value).toBe("Saved with the button");
  await userEvent.clear(closedDecision);
  await userEvent.type(closedDecision, "Keep costs for the next draft");
  await userEvent.keyboard("{Enter}");
  expect(screen.getByText("Decision saved.")).toBeTruthy();
  expect(closedDecision.value).toBe("Keep costs for the next draft");
  fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await waitFor(() =>
    expect(requests.at(-1)?.body).toMatchObject({
      observations: [
        expect.objectContaining({
          status: "addressed",
          decision: "Keep costs for the next draft",
        }),
      ],
    }),
  );
  expect(screen.getByText("Addressed")).toBeTruthy();
  expect(view.state.doc.toString()).toBe("Costs are unknown.");
  fireEvent.click(screen.getByRole("button", { name: "Clear decision" }));
  expect(screen.getByText("Decision cleared.")).toBeTruthy();
  expect(
    (screen.getByRole("textbox", { name: /Decision for this draft/ }) as HTMLInputElement).value,
  ).toBe("");
  expect(screen.getByText("Addressed")).toBeTruthy();
  act(() => view.dispatch({ changes: { from: 0, to: 5, insert: "Price" } }));
  fireEvent.click(screen.getByRole("button", { name: "Reveal passage" }));
  expect(screen.getByText(/Earlier draft \/ changed/)).toBeTruthy();
});

it("keeps unsaved decisions with their selected threads through unrelated renders", async () => {
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() => view.dispatch({ changes: { from: 0, insert: "Draft" } }));
  answer = { status: "observe", text: "First concern" };
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  const first = await screen.findByText("First concern");
  answer = { status: "observe", text: "Second concern" };
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  const second = await screen.findByText("Second concern");
  const firstArticle = first.closest("article");
  const secondArticle = second.closest("article");
  if (!firstArticle || !secondArticle) throw new Error("Expected Writer observations");

  fireEvent.click(within(firstArticle).getByRole("button", { name: "Open thread" }));
  const decision = screen.getByRole<HTMLInputElement>("textbox", {
    name: "Decision for this draft",
  });
  await userEvent.type(decision, "First draft decision");
  act(() => view.dispatch({ changes: { from: 5, insert: " update" } }));
  expect(decision.value).toBe("First draft decision");

  fireEvent.click(within(secondArticle).getByRole("button", { name: "Open thread" }));
  const secondDecision = screen.getByRole<HTMLInputElement>("textbox", {
    name: "Decision for this draft",
  });
  expect(secondDecision.value).toBe("");
  await userEvent.type(secondDecision, "Second draft decision");
  fireEvent.click(within(firstArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("First draft decision");
  fireEvent.click(within(secondArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("Second draft decision");

  let releaseObservation!: () => void;
  const observationGate = new Promise<void>((resolve) => {
    releaseObservation = resolve;
  });
  const originalFetch = globalThis.fetch;
  vi.stubGlobal("fetch", async (request: Request) => {
    if (new URL(request.url).pathname.endsWith("/writer/observe")) await observationGate;
    return originalFetch(request);
  });
  const observationCount = requests.length;
  fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("Analyzing…");
  fireEvent.click(within(firstArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("First draft decision");
  fireEvent.click(within(secondArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("Second draft decision");
  fireEvent.click(within(firstArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("First draft decision");
  releaseObservation();
  await waitFor(() => expect(requests.length).toBeGreaterThan(observationCount));
  await waitFor(() => expect(screen.queryByText("Analyzing…")).toBeNull());
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("First draft decision");
  fireEvent.click(within(secondArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("Second draft decision");

  let releaseDiscussion!: () => void;
  const discussionGate = new Promise<void>((resolve) => {
    releaseDiscussion = resolve;
  });
  discussionResponse = async () => {
    await discussionGate;
    return Response.json(discussionAnswer);
  };
  await userEvent.type(screen.getByRole("textbox", { name: "Ask Writer" }), "Review drafts");
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Discussing…");
  releaseDiscussion();
  await screen.findByText("Keep the author's own wording.");
  fireEvent.click(within(firstArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("First draft decision");
  fireEvent.click(within(secondArticle).getByRole("button", { name: "Open thread" }));
  expect(
    screen.getByRole<HTMLInputElement>("textbox", { name: "Decision for this draft" }).value,
  ).toBe("Second draft decision");
});

it("reveals reversed passages in a mounted editor, requesting scroll only on click without changing selection or undo", async () => {
  answer = { status: "observe", text: "How do these sections relate?", quotes: ["Later", "First"] };
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  const text = `First\n${"intervening line\n".repeat(80)}Later`;
  act(() => view.dispatch({ changes: { from: 0, insert: text }, selection: { anchor: 3 } }));
  await screen.findByText("How do these sections relate?", {}, { timeout: 3500 });
  expect(editor.querySelectorAll(".cm-writer-passage")).toHaveLength(0);
  const dispatch = vi.spyOn(view, "dispatch");
  fireEvent.click(screen.getByRole("button", { name: "Reveal passage" }));
  const effects = dispatch.mock.calls.flatMap(([spec]) => spec?.effects ?? []);
  expect(effects).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        value: expect.objectContaining({
          range: expect.objectContaining({ anchor: text.indexOf("Later") }),
        }),
      }),
    ]),
  );
  expect(view.state.selection.main.anchor).toBe(3);
  expect(view.state.doc.toString()).toBe(text);
  expect(editor.querySelectorAll(".cm-writer-passage")).toHaveLength(2);
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(view.state.doc.toString()).toBe("");
});

it("keeps Ask Writer available while an automatic observation is running", async () => {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  mount(true);
  const original = globalThis.fetch;
  vi.stubGlobal("fetch", async (request: Request) => {
    if (new URL(request.url).pathname.endsWith("/writer/observe")) await gate;
    return original(request);
  });
  const view = EditorView.findFromDOM(
    screen.getByRole("textbox", { name: "Writer document" }) as HTMLElement,
  );
  if (!view) throw new Error("CodeMirror did not mount");
  act(() => view.dispatch({ changes: { from: 0, insert: "Draft during analysis" } }));
  await screen.findByText("Analyzing…", {}, { timeout: 3500 });
  expect(screen.getByRole("textbox", { name: "Ask Writer" })).not.toHaveProperty("disabled", true);
  await userEvent.type(screen.getByRole("textbox", { name: "Ask Writer" }), "Why?");
  await userEvent.keyboard("{Enter}");
  release();
  await waitFor(() => expect(requests.some(({ path }) => path.endsWith("/discuss"))).toBe(true));
});

function installLocks(gate: Promise<void> = Promise.resolve()) {
  let chain = gate;
  const locks = {
    request: vi.fn((_key: string, _options: unknown, callback: () => boolean) => {
      const result = chain.then(callback);
      chain = result.then(
        () => {},
        () => {},
      );
      return result;
    }),
  } as unknown as LockManager;
  Object.defineProperty(navigator, "locks", { configurable: true, value: locks });
  return locks;
}

function writerView() {
  const view = EditorView.findFromDOM(
    screen.getByRole("textbox", { name: "Writer document" }) as HTMLElement,
  );
  if (!view) throw new Error("CodeMirror did not mount");
  return view;
}

it("recovers adopted text but never previews or attached content", async () => {
  installLocks();
  const first = mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  fireEvent.click(screen.getByRole("button", { name: "Save locally (opt in)" }));
  await screen.findByText("Saved in this browser");
  fireEvent.change(screen.getByLabelText("Attach reference files"), {
    target: { files: [new File(["private notes"], "notes.txt", { type: "text/plain" })] },
  });
  await screen.findByRole("button", { name: "Remove notes.txt" });
  discussionAnswer = {
    mode: "proposal",
    text: "Outline",
    candidate: "# Outline\n\n[Evidence needed]",
  };
  await sendWriter("Outline this idea");
  await screen.findByRole("region", { name: "Writer preview" });
  expect(window.localStorage.getItem("studio.writer.recovery")).not.toContain("private notes");
  expect(window.localStorage.getItem("studio.writer.recovery")).not.toContain("Evidence needed");
  fireEvent.click(screen.getByRole("button", { name: "Use this starting point" }));
  await waitFor(() =>
    expect(window.localStorage.getItem("studio.writer.recovery")).toContain("Evidence needed"),
  );
  first.unmount();
  first.query.clear();
  mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  expect(writerView().state.doc.toString()).toBe("# Outline\n\n[Evidence needed]");
  expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove notes.txt" })).toBeNull();
});

it("sends the clicked observation's bounded thread without changing either observation", async () => {
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() => view.dispatch({ changes: { from: 0, insert: "B passage." } }));

  answer = { status: "observe", text: "B concern", quote: "B passage" };
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("B concern");
  const bArticle = screen.getByText("B concern").closest("article");
  if (!bArticle) throw new Error("Expected B observation");
  fireEvent.click(within(bArticle).getByRole("button", { name: "Open thread" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Ask Writer" }), "B discussion");
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Keep the author's own wording.");

  for (let index = 1; index <= 14; index++) {
    answer = { status: "observe", text: `Concern ${index}` };
    await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
    await screen.findByText(`Concern ${index}`);
  }
  const openThreads = screen.getAllByRole("button", { name: "Open thread" });
  const lastThread = openThreads.at(-1);
  if (!lastThread) throw new Error("Expected latest observation thread");
  fireEvent.click(lastThread);
  fireEvent.click(within(bArticle).getByRole("button", { name: "Open thread" }));
  discussionAnswer = { mode: "proposal", text: "Clearer", candidate: "Better" };
  await sendWriter("Revise B");
  await screen.findByRole("region", { name: "Writer preview" });

  const proposal = requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body;
  expect(proposal?.observations).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ text: "B concern", selected: true, status: "open" }),
    ]),
  );
  expect(proposal?.discussion).toEqual([
    { role: "user", text: "B discussion" },
    { role: "assistant", text: "Keep the author's own wording." },
  ]);
  expect(proposal?.observations).toHaveLength(12);
  expect(within(bArticle).getByText("Open")).toBeTruthy();
  expect(within(bArticle).getByRole("button", { name: "Addressed" })).toBeTruthy();
});

it("refuses multi-quote observation anchors until the author explicitly selects one passage", async () => {
  answer = {
    status: "observe",
    text: "These passages need comparison.",
    quote: "First",
    quotes: ["First", "Later"],
  };
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() => view.dispatch({ changes: { from: 0, insert: "First passage. Later passage." } }));
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("These passages need comparison.");
  const article = screen.getByText("These passages need comparison.").closest("article");
  if (!article) throw new Error("Expected multi-quote observation");
  fireEvent.click(within(article).getByRole("button", { name: "Open thread" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Decision for this draft/ }), {
    target: { value: "Keep both passages" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save decision" }));
  discussionAnswer = { mode: "reply", text: "Please select one passage in the editor." };
  await sendWriter("Can you help revise this?");
  expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
  expect(requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body).not.toHaveProperty(
    "passage",
  );
  expect(view.state.doc.toString()).toBe("First passage. Later passage.");
  expect(within(article).getByText("Open")).toBeTruthy();
  expect(
    (screen.getByRole("textbox", { name: /Decision for this draft/ }) as HTMLInputElement).value,
  ).toBe("Keep both passages");
  const from = view.state.doc.toString().indexOf("Later");
  act(() => view.dispatch({ selection: { anchor: from, head: from + "Later".length } }));
  await includeSelectedPassage();
  discussionAnswer = { mode: "proposal", text: "Better", candidate: "Revised" };
  await sendWriter("Please revise the selected passage");
  await screen.findByRole("region", { name: "Writer preview" });
  expect(requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body).toMatchObject({
    passage: { from, to: from + "Later".length, text: "Later" },
  });
});

it("refuses ambiguous observation anchors without changing status or decisions", async () => {
  answer = { status: "observe", text: "Clarify this?", quote: "Same" };
  mount(true);
  const view = writerView();
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() => view.dispatch({ changes: { from: 0, insert: "Same. Same." } }));
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("Clarify this?");
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  await sendWriter("Revise?");
  expect(requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body).not.toHaveProperty(
    "passage",
  );
  expect(screen.queryByRole("region", { name: "Writer preview" })).toBeNull();
  expect(view.state.doc.toString()).toBe("Same. Same.");
  act(() => view.dispatch({ changes: { from: 6, to: 11, insert: "Other" } }));
  discussionAnswer = { mode: "proposal", text: "Clearer", candidate: "Better" };
  await sendWriter("Revise now?");
  await screen.findByRole("region", { name: "Writer preview" });
  expect(requests.findLast((entry) => entry.path.endsWith("/discuss"))?.body).toMatchObject({
    passage: { from: 0, to: 4, text: "Same" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply" }));
  expect(view.state.doc.toString()).toBe("Better. Other");
  expect(screen.getAllByText("Clarify this?").length).toBeGreaterThan(0);
  expect(screen.getByRole("button", { name: "Addressed" })).toBeTruthy();
});

it("saves only the imported document after opt-in and restores it without old context", async () => {
  installLocks();
  const first = mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  act(() => writerView().dispatch({ changes: { from: 0, insert: "Old draft" } }));
  fireEvent.click(screen.getByRole("button", { name: "Add brief" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Writing brief/ }), {
    target: { value: "Old brief" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply brief" }));
  fireEvent.click(screen.getByRole("button", { name: "Save locally (opt in)" }));
  await screen.findByText("Saved in this browser");
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  await chooseDocument(new File(["New draft"], "new.md"));
  await waitFor(() =>
    expect(
      JSON.parse(window.localStorage.getItem("studio.writer.recovery") ?? "{}").snapshot?.document
        .content,
    ).toBe("New draft"),
  );
  const saved = window.localStorage.getItem("studio.writer.recovery") ?? "";
  expect(saved).not.toContain("Old draft");
  expect(saved).not.toContain("Old brief");
  first.unmount();
  first.query.clear();
  mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  expect(writerView().state.doc.toString()).toBe("New draft");
  expect(screen.queryByText("Old brief")).toBeNull();
});

it("serializes a pending old-document save with an imported document", async () => {
  let release!: () => void;
  installLocks(
    new Promise<void>((resolve) => {
      release = resolve;
    }),
  );
  const first = mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  const view = writerView();
  act(() => view.dispatch({ changes: { from: 0, insert: "Old draft" } }));
  fireEvent.click(screen.getByRole("button", { name: "Save locally (opt in)" }));
  await screen.findByText("Saving…");
  expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  await chooseDocument(new File(["New draft"], "new.md"));
  expect(view.state.doc.toString()).toBe("New draft");
  expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
  await act(async () => release());
  await waitFor(() =>
    expect(
      JSON.parse(window.localStorage.getItem("studio.writer.recovery") ?? "{}").snapshot?.document
        .content,
    ).toBe("New draft"),
  );
  await screen.findByText("Saved in this browser");
  first.unmount();
  first.query.clear();
  mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  expect(writerView().state.doc.toString()).toBe("New draft");
});

it("keeps a local draft only after opt-in, restores it, and forgets only on confirmation", async () => {
  installLocks();
  const first = mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() => view.dispatch({ changes: { from: 0, insert: "Saved draft" } }));
  fireEvent.click(screen.getByRole("switch", { name: "Automatic feedback" }));
  fireEvent.click(screen.getByRole("button", { name: "Add brief" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Writing brief/ }), {
    target: { value: "Restored audience" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply brief" }));
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("What evidence supports this assumption?");
  await userEvent.type(
    screen.getByRole("textbox", { name: "Ask Writer" }),
    "General saved question",
  );
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Keep the author's own wording.");
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  await userEvent.type(
    screen.getByRole("textbox", { name: "Ask Writer" }),
    "Thread saved question",
  );
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Keep the author's own wording.");
  fireEvent.change(screen.getByRole("textbox", { name: /Decision for this draft/ }), {
    target: { value: "Confirmed author intent" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save decision" }));
  expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Save locally (opt in)" }));
  await screen.findByText("Saved in this browser");
  expect(window.localStorage.getItem("studio.writer.recovery")).toContain("Saved draft");
  first.unmount();
  first.query.clear();
  mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  expect(screen.getByRole("textbox", { name: "Writer document" }).textContent).toBe("Saved draft");
  expect(screen.getByText("General saved question")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  expect(screen.getByText("Thread saved question")).toBeTruthy();
  expect(
    (screen.getByRole("textbox", { name: /Decision for this draft/ }) as HTMLInputElement).value,
  ).toBe("Confirmed author intent");
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await waitFor(() =>
    expect(requests.filter(({ path }) => path.endsWith("/observe"))).toHaveLength(2),
  );
  expect(requests.at(-1)?.body).toMatchObject({
    document: { revision: 1, content: "Saved draft" },
    brief: "Restored audience",
    observations: [{ selected: true, decision: "Confirmed author intent" }],
    decisions: [
      { text: "What evidence supports this assumption?", decision: "Confirmed author intent" },
    ],
    discussion: [
      { role: "user", text: "Thread saved question" },
      { role: "assistant", text: "Keep the author's own wording." },
    ],
  });
  await userEvent.type(
    screen.getByRole("textbox", { name: "Ask Writer" }),
    "Continue restored thread",
  );
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Continue restored thread");
  expect(requests.at(-1)?.body).toMatchObject({
    brief: "Restored audience",
    decisions: [{ decision: "Confirmed author intent" }],
    discussion: [{ text: "Thread saved question" }, { role: "assistant" }],
  });
  fireEvent.click(screen.getByRole("button", { name: "← Back to conversation" }));
  expect(screen.getByText("General saved question")).toBeTruthy();
  const confirm = vi.fn().mockReturnValue(false);
  vi.stubGlobal("confirm", confirm);
  fireEvent.click(screen.getByRole("button", { name: "Forget local draft" }));
  expect(window.localStorage.getItem("studio.writer.recovery")).not.toBeNull();
  confirm.mockReturnValue(true);
  fireEvent.click(screen.getByRole("button", { name: "Forget local draft" }));
  await screen.findByText("Clear · not saved in this browser");
  expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
});

it("cannot restore, overwrite, or forget a peer's draft mounted before the account event arrives", async () => {
  installLocks();
  const tab = mount(true, false, { account: "alice", showWriter: false });
  await screen.findByText("Verified workspace");
  expect(window.localStorage.getItem("studio.account")).toBe("alice");
  const snapshot: WriterSnapshot = {
    document: { revision: 1, content: "Bob private draft" },
    brief: "Bob private brief",
    observations: [],
    generalDiscussion: [],
  };
  const raw = JSON.stringify({
    version: 1,
    account: "bob",
    generation: crypto.randomUUID(),
    snapshot,
  });
  window.localStorage.setItem("studio.account", "bob");
  window.localStorage.setItem("studio.writer.recovery", raw);
  tab.showWriter();
  expect(writerView().state.doc.toString()).toBe("");
  expect(screen.getByText("Saving unavailable · download your draft")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Save locally (opt in)" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Forget local draft" })).toBeNull();
  act(() => writerView().dispatch({ changes: { from: 0, insert: "Alice live draft" } }));
  expect(window.localStorage.getItem("studio.writer.recovery")).toBe(raw);
  expect(document.body.textContent).not.toContain("Bob private");
  tab.query.clear();
});

it("does not enable persistent recovery in auth-disabled mode from an ambient account marker", async () => {
  installLocks();
  window.localStorage.setItem("studio.account", "unverified");
  mount(true, false, {});
  await screen.findByRole("textbox", { name: "Writer document" });
  expect(screen.getByText("Saving unavailable · download your draft")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Save locally (opt in)" })).toBeNull();
});

it("retains a live draft and reports an actual quota failure instead of Saved", async () => {
  installLocks();
  const store = window.localStorage;
  vi.stubGlobal("localStorage", {
    getItem: store.getItem.bind(store),
    removeItem: store.removeItem.bind(store),
    key: store.key.bind(store),
    get length() {
      return store.length;
    },
    setItem(key: string, value: string) {
      if (key === "studio.writer.recovery") throw new DOMException("quota", "QuotaExceededError");
      store.setItem(key, value);
    },
  });
  mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  const view = writerView();
  act(() => view.dispatch({ changes: { from: 0, insert: "Live quota draft" } }));
  fireEvent.click(screen.getByRole("button", { name: "Save locally (opt in)" }));
  await screen.findByText(/Save failed · download/);
  expect(view.state.doc.toString()).toBe("Live quota draft");
  expect(screen.queryByText("Saved in this browser")).toBeNull();
  expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
});

it("keeps corrupt restore bytes untouched and preserves subsequent live edits until confirmed forget", async () => {
  installLocks();
  reconcileAccount("alice");
  const raw = JSON.stringify({
    version: 1,
    account: "alice",
    snapshot: { document: { content: "recoverable text" } },
  });
  window.localStorage.setItem("studio.writer.recovery", raw);
  mount(true, false, { account: "alice" });
  await screen.findByText(/saved data is invalid; draft not restored/);
  const view = writerView();
  expect(view.state.doc.toString()).toBe("");
  act(() => view.dispatch({ changes: { from: 0, insert: "Live draft survives" } }));
  expect(window.localStorage.getItem("studio.writer.recovery")).toBe(raw);
  expect(screen.queryByText("Saved in this browser")).toBeNull();
  const confirm = vi.fn().mockReturnValue(false);
  vi.stubGlobal("confirm", confirm);
  fireEvent.click(screen.getByRole("button", { name: "Forget local draft" }));
  expect(window.localStorage.getItem("studio.writer.recovery")).toBe(raw);
  confirm.mockReturnValue(true);
  fireEvent.click(screen.getByRole("button", { name: "Forget local draft" }));
  await screen.findByText("Clear · not saved in this browser");
  expect(view.state.doc.toString()).toBe("Live draft survives");
  expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
});

it("rejects a stale mounted save after a peer save without losing the live draft", async () => {
  const locks = installLocks();
  reconcileAccount("alice");
  const peer = new WriterRecovery(window.localStorage, locks);
  const snapshot: WriterSnapshot = {
    document: { revision: 1, content: "Saved original" },
    brief: "",
    observations: [],
    generalDiscussion: [],
  };
  expect(await peer.save(snapshot)).toBe(true);
  mount(true, false, { account: "alice" });
  await screen.findByText("Saved in this browser");
  expect(await peer.save({ ...snapshot, brief: "Peer latest" })).toBe(true);
  const raw = window.localStorage.getItem("studio.writer.recovery");
  act(() => writerView().dispatch({ changes: { from: 0, insert: "Live edit: " } }));
  await screen.findByText(/Save failed · download/);
  expect(writerView().state.doc.toString()).toBe("Live edit: Saved original");
  expect(window.localStorage.getItem("studio.writer.recovery")).toBe(raw);
});

it("finishes a pending save then confirmed forget without resurrecting queued edits", async () => {
  let release!: () => void;
  installLocks(
    new Promise<void>((resolve) => {
      release = resolve;
    }),
  );
  mount(true, false, { account: "alice" });
  await screen.findByRole("textbox", { name: "Writer document" });
  const view = writerView();
  act(() => view.dispatch({ changes: { from: 0, insert: "Pending save" } }));
  fireEvent.click(screen.getByRole("button", { name: "Save locally (opt in)" }));
  await screen.findByText("Saving…");
  act(() =>
    view.dispatch({ changes: { from: view.state.doc.length, insert: " plus queued edit" } }),
  );
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  fireEvent.click(screen.getByRole("button", { name: "Forget local draft" }));
  await act(async () => {
    release();
  });
  await screen.findByText("Clear · not saved in this browser");
  act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: " after forget" } }));
  expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
  expect(view.state.doc.toString()).toBe("Pending save plus queued edit after forget");
});

it.each(["switch", "logout"] as const)(
  "cleans Writer recovery through AuthGate on %s",
  async (kind) => {
    installLocks();
    const tab = mount(true, false, { account: "alice" });
    await screen.findByRole("textbox", { name: "Writer document" });
    act(() => writerView().dispatch({ changes: { from: 0, insert: "Alice saved draft" } }));
    fireEvent.click(screen.getByRole("button", { name: "Save locally (opt in)" }));
    await screen.findByText("Saved in this browser");
    if (kind === "switch") {
      const session: GetAuthSessionResponse = {
        mode: "oidc",
        status: "authenticated",
        account: "bob",
      };
      act(() => {
        tab.query.setQueryData(getAuthSessionOptions().queryKey, session);
      });
      await waitFor(() => expect(window.localStorage.getItem("studio.account")).toBe("bob"));
    } else {
      window.localStorage.removeItem("studio.account");
      // Hold the refetch so the signed-out state remains observable.
      vi.stubGlobal("fetch", () => new Promise<Response>(() => {}));
      act(() =>
        window.dispatchEvent(
          new StorageEvent("storage", { key: "studio.account", newValue: null }),
        ),
      );
      await waitFor(() =>
        expect(screen.queryByRole("textbox", { name: "Writer document" })).toBeNull(),
      );
    }
    expect(window.localStorage.getItem("studio.writer.recovery")).toBeNull();
    expect(document.body.textContent).not.toContain("Alice saved draft");
    if (kind === "switch") {
      await screen.findByRole("textbox", { name: "Writer document" });
      expect(writerView().state.doc.toString()).toBe("");
    }
    tab.query.clear();
  },
);

it("does not highlight or scroll an ambiguous duplicate quote in the mounted editor", async () => {
  answer = { status: "observe", text: "Which cost?", quote: "Costs" };
  mount(true);
  const view = writerView();
  act(() =>
    view.dispatch({
      changes: { from: 0, insert: "Costs first. Costs again." },
      selection: { anchor: 3 },
    }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("Which cost?");
  const scroll = vi.spyOn(EditorView, "scrollIntoView");
  fireEvent.click(screen.getByRole("button", { name: "Reveal passage" }));
  expect(screen.getByText(/passage is missing or ambiguous/)).toBeTruthy();
  expect(view.dom.querySelectorAll(".cm-writer-passage")).toHaveLength(0);
  expect(scroll).not.toHaveBeenCalled();
  expect(view.state.selection.main.anchor).toBe(3);
  // Positive control: the same observation can reveal once the quote is unique.
  act(() => view.dispatch({ changes: { from: 13, to: 18, insert: "Price" } }));
  fireEvent.click(screen.getByRole("button", { name: "Reveal passage" }));
  expect(scroll).toHaveBeenCalledOnce();
  expect(view.dom.querySelector(".cm-writer-passage")?.textContent).toBe("Costs");
});

it("keeps silent analysis invisible", async () => {
  answer = { status: "silent" };
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  act(() =>
    view.dispatch({
      changes: { from: 0, insert: "Draft" },
      userEvent: "input.type",
    }),
  );
  await waitFor(() => expect(requests).toHaveLength(1), { timeout: 3500 });
  expect(screen.getByText("Observations will appear here as you write.")).toBeTruthy();
  expect(editor.textContent).toContain("Draft");
});
