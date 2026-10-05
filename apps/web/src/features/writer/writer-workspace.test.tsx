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
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    return Response.json({ text: "Keep the author's own wording." });
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
  vi.unstubAllGlobals();
});

it("shows an empty Markdown surface with theme-aware styles and formats selected text as one undoable edit", () => {
  mount(true);
  const editor = screen.getByRole("textbox", { name: "Writer document" });
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (!view) throw new Error("CodeMirror did not mount");
  expect(screen.getByText("Start writing in Markdown…")).toBeTruthy();
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
  await userEvent.type(screen.getByRole("textbox", { name: "Message Mecatl" }), "Why?");
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
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Message Mecatl" }));
  act(() => view.dispatch({ changes: { from: 7, insert: " updated" }, userEvent: "input.type" }));
  await userEvent.type(screen.getByRole("textbox", { name: /message|prompt/i }), "How?");
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
  expect(screen.getByText("No open observations.")).toBeTruthy();
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
  await userEvent.type(screen.getByRole("textbox", { name: "Message Mecatl" }), "Why?");
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
  fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("Why the expense?");
  expect(requests[0]?.body).toMatchObject({ brief: "Audience: operators" });
  act(() => view.dispatch({ selection: { anchor: 4 } }));
  fireEvent.click(screen.getByRole("button", { name: "Reveal passage" }));
  expect(view.state.selection.main.anchor).toBe(4);
  expect(editor.querySelector(".cm-writer-passage")?.textContent).toBe("Costs");
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  fireEvent.change(screen.getByRole("textbox", { name: /Author decision/ }), {
    target: { value: "Costs outside scope" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Confirm decision" }));
  fireEvent.click(screen.getByRole("button", { name: "Addressed" }));
  expect(screen.getByText("History · 1 closed threads").closest("details")?.open).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: /Ask Writer · general/ }));
  fireEvent.click(screen.getByText("History · 1 closed threads"));
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  expect((screen.getByRole("textbox", { name: /Author decision/ }) as HTMLInputElement).value).toBe(
    "Costs outside scope",
  );
  act(() => view.dispatch({ changes: { from: 0, to: 5, insert: "Price" } }));
  fireEvent.click(screen.getByRole("button", { name: "Reveal passage" }));
  expect(screen.getByText(/Earlier draft \/ changed/)).toBeTruthy();
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
  expect(screen.getByRole("textbox", { name: "Message Mecatl" })).not.toHaveProperty(
    "disabled",
    true,
  );
  await userEvent.type(screen.getByRole("textbox", { name: "Message Mecatl" }), "Why?");
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
  fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
  await screen.findByText("What evidence supports this assumption?");
  await userEvent.type(
    screen.getByRole("textbox", { name: "Message Mecatl" }),
    "General saved question",
  );
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Keep the author's own wording.");
  fireEvent.click(screen.getByRole("button", { name: "Open thread" }));
  await userEvent.type(
    screen.getByRole("textbox", { name: "Message Mecatl" }),
    "Thread saved question",
  );
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Keep the author's own wording.");
  fireEvent.change(screen.getByRole("textbox", { name: /Author decision/ }), {
    target: { value: "Confirmed author intent" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Confirm decision" }));
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
  expect((screen.getByRole("textbox", { name: /Author decision/ }) as HTMLInputElement).value).toBe(
    "Confirmed author intent",
  );
  fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
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
    screen.getByRole("textbox", { name: "Message Mecatl" }),
    "Continue restored thread",
  );
  await userEvent.keyboard("{Enter}");
  await screen.findByText("Continue restored thread");
  expect(requests.at(-1)?.body).toMatchObject({
    brief: "Restored audience",
    decisions: [{ decision: "Confirmed author intent" }],
    discussion: [{ text: "Thread saved question" }, { role: "assistant" }],
  });
  fireEvent.click(screen.getByRole("button", { name: /Ask Writer · general/ }));
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
  fireEvent.click(screen.getByRole("button", { name: "Read this now" }));
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
  expect(screen.getByText("No open observations.")).toBeTruthy();
  expect(editor.textContent).toContain("Draft");
});
