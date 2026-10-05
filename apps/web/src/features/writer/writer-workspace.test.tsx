// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { history, undo } from "@codemirror/commands";
import { ensureSyntaxTree } from "@codemirror/language";
import { EditorSelection, EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { client } from "@mecatl-studio/contracts/client";
import { getRuntimeOptions, getRuntimeSettingsOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { setRequestRecoveryState } from "../../lib/api-client";
import { formatMarkdown, WriterWorkspace } from "./writer-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
const requests: Array<{
  path: string;
  body: {
    document: { content: string; revision: number };
    message?: string;
    model?: { id: string; providerId: string };
  };
}> = [];
let answer: { status: "observe"; text: string } | { status: "silent" } = {
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
  const body = await request.clone().json();
  requests.push({ path, body });
  if (path === "/api/v1/writer/observe") return Response.json(answer);
  if (path === "/api/v1/writer/discuss") {
    if (discussionFailure) return Response.json({ error: "unavailable" }, { status: 503 });
    return Response.json({ text: "Keep the author's own wording." });
  }
  throw new Error(`Unexpected ${path}`);
};

function mount(enabled?: boolean, strict = false) {
  setRequestRecoveryState({ identityEpoch: 0, phase: "ready", workspaceMounted: true });
  vi.stubGlobal("fetch", fetchBff);
  client.setConfig({ baseUrl: "http://studio.test" });
  const query = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  query.setQueryData<unknown>(getRuntimeOptions().queryKey, { experimentalWriter: enabled });
  query.setQueryData<unknown>(getRuntimeSettingsOptions().queryKey, {
    modelsSupported: true,
    models: [{ id: "one", providerId: "provider", displayName: "One", image: false }],
  });
  const component = (
    <QueryClientProvider client={query}>
      <WriterWorkspace />
    </QueryClientProvider>
  );
  const mounted = render(strict ? <StrictMode>{component}</StrictMode> : component);
  return { ...mounted, query };
}
afterEach(() => {
  cleanup();
  runtimeFailure = false;
  discussionFailure = false;
  runtimeRequests = 0;
  requests.length = 0;
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
  fireEvent.click(screen.getByRole("button", { name: "Discuss" }));
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
  fireEvent.click(screen.getByRole("button", { name: "Pause" }));
  fireEvent.click(screen.getByRole("button", { name: "Discuss" }));
  expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Message Mecatl" }));
  act(() => view.dispatch({ changes: { from: 7, insert: " updated" }, userEvent: "input.type" }));
  await userEvent.type(screen.getByRole("textbox", { name: /message|prompt/i }), "How?");
  await userEvent.keyboard("{Enter}");
  await waitFor(() => expect(requests.some(({ path }) => path.endsWith("/discuss"))).toBe(true));
  expect(requests.find(({ path }) => path.endsWith("/discuss"))?.body).toMatchObject({
    document: { content: "Opening updated" },
    observations: [
      {
        revision: 1,
        status: "active",
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
  fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
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
  fireEvent.click(screen.getByRole("button", { name: "Discuss" }));
  discussionFailure = true;
  await userEvent.type(screen.getByRole("textbox", { name: "Message Mecatl" }), "Why?");
  await userEvent.keyboard("{Enter}");
  await screen.findByText(/Discussion failed/);
  fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
  expect(screen.queryByRole("region", { name: "Discussion" })).toBeNull();
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
