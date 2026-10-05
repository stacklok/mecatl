// SPDX-License-Identifier: Apache-2.0

import { history, historyKeymap, isolateHistory, redo, undo } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { ChangeSet, EditorSelection, EditorState } from "@codemirror/state";
import { drawSelection, EditorView, keymap, placeholder } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { discussWriter, observeWriter } from "@mecatl-studio/contracts/generated";
import { getRuntimeOptions, getRuntimeSettingsOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import { ChatComposer, type ComposerModelOption } from "../chat/chat-composer";
import { ChatTranscript } from "../chat/chat-transcript";
import { groupModels, ModelFilterList } from "../chat/model-effort-menu";
import { WriterCore, type WriterTransport } from "./writer-core";

const writerHighlight = HighlightStyle.define([
  { tag: tags.heading, color: "var(--primary)", fontWeight: "bold" },
  { tag: tags.strong, fontWeight: "bold" },
  { tag: tags.emphasis, fontStyle: "italic" },
  { tag: [tags.link, tags.url], color: "var(--primary)", textDecoration: "underline" },
  { tag: tags.monospace, color: "var(--muted-foreground)" },
]);

export function formatMarkdown(view: EditorView, kind: "heading" | "bold" | "italic") {
  const { from, to, anchor, head } = view.state.selection.main;
  if (kind === "heading") {
    const line = view.state.doc.lineAt(from);
    const heading = /^(#{1,6})(?:[ \t]+|$)/.exec(line.text);
    const changes = {
      from: line.from,
      to: line.from + (heading?.[0].length ?? 0),
      insert: heading?.[1] === "#" ? "" : "# ",
    };
    const mapping = ChangeSet.of(changes, view.state.doc.length);
    view.dispatch({
      changes,
      selection: EditorSelection.single(mapping.mapPos(anchor, 1), mapping.mapPos(head, 1)),
      annotations: isolateHistory.of("full"),
      userEvent: "input",
    });
  } else {
    const marker = kind === "bold" ? "**" : "*";
    const before = view.state.sliceDoc(0, from).match(/\*+$/)?.[0].length ?? 0;
    const after = view.state.sliceDoc(to).match(/^\*+/)?.[0].length ?? 0;
    const remove =
      kind === "bold" ? before >= 2 && after >= 2 : before % 2 === 1 && after % 2 === 1;
    const offset = remove ? -marker.length : marker.length;
    view.dispatch({
      changes: remove
        ? [
            { from: from - marker.length, to: from },
            { from: to, to: to + marker.length },
          ]
        : [
            { from, insert: marker },
            { from: to, insert: marker },
          ],
      selection: EditorSelection.single(anchor + offset, head + offset),
      annotations: isolateHistory.of("full"),
      userEvent: "input",
    });
  }
  if (!view.hasFocus) view.focus();
  return true;
}

const transport: WriterTransport = {
  async observe(body, signal) {
    const { data } = await observeWriter({ body, signal, throwOnError: true });
    return data;
  },
  async discuss(body, signal) {
    const { data } = await discussWriter({ body, signal, throwOnError: true });
    return data;
  },
};

export function WriterWorkspace() {
  const runtime = useQuery(getRuntimeOptions());
  const available = !runtime.isError && runtime.data?.experimentalWriter === true;
  const [enabledOnce, setEnabledOnce] = useState(available);
  useEffect(() => {
    if (available) setEnabledOnce(true);
  }, [available]);
  if (!available && !enabledOnce) {
    return (
      <div className="p-8" role="status">
        Writer is unavailable.
      </div>
    );
  }
  return <WriterEditor available={available} />;
}

function WriterEditor({ available }: { available: boolean }) {
  const [, refresh] = useState(0);
  const coreRef = useRef<WriterCore | null>(null);
  if (!coreRef.current)
    coreRef.current = new WriterCore(transport, () => refresh((value) => value + 1));
  const core = coreRef.current;
  const settings = useQuery(getRuntimeSettingsOptions());
  const disabledModels = useDisabledModels().disabled;
  const models: ComposerModelOption[] =
    settings.data?.models
      .filter((model) => !disabledModels.has(modelPreferenceId(model)))
      .map((model) => ({
        id: model.id,
        providerId: model.providerId,
        label: model.displayName,
        image: model.image,
      })) ?? [];
  const groupedModels = groupModels(models);
  const modelDisabled =
    !available || core.busy !== undefined || settings.isError || !settings.data?.modelsSupported;
  const selectedModel = core.model;
  const selectedModelLabel =
    groupedModels
      .get(selectedModel?.providerId ?? "")
      ?.find((model) => model.id === selectedModel?.id)?.label ??
    (selectedModel ? `${selectedModel.id} (${selectedModel.providerId})` : "Deployment default");
  useLayoutEffect(() => core.setAvailable(available), [core, available]);
  const discussionHost = useRef<HTMLElement>(null);
  const selectedId = core.selectedId;
  useEffect(() => {
    if (selectedId) discussionHost.current?.querySelector("textarea")?.focus();
  }, [selectedId]);
  const host = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  useEffect(() => {
    if (!host.current) return;
    core.revive();
    const view = new EditorView({
      parent: host.current,
      state: EditorState.create({
        extensions: [
          history(),
          markdown(),
          syntaxHighlighting(writerHighlight),
          placeholder("Start writing in Markdown…"),
          drawSelection(),
          keymap.of([
            { key: "Mod-b", run: (view) => formatMarkdown(view, "bold") },
            { key: "Mod-i", run: (view) => formatMarkdown(view, "italic") },
            { key: "Mod-Alt-1", run: (view) => formatMarkdown(view, "heading") },
            ...historyKeymap,
          ]),
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({
            "aria-label": "Writer document",
            role: "textbox",
            "aria-multiline": "true",
          }),
          EditorView.theme({
            "&": {
              height: "100%",
              fontSize: "16px",
              backgroundColor: "var(--background)",
              color: "var(--foreground)",
            },
            ".cm-scroller": { overflow: "auto", fontFamily: "inherit", lineHeight: "1.8" },
            ".cm-content": {
              padding: "2rem 1.5rem",
              maxWidth: "75ch",
              margin: "0 auto",
              minHeight: "100%",
              caretColor: "var(--primary)",
            },
            ".cm-placeholder": {
              color: "var(--muted-foreground)",
              display: "inline-block",
              lineHeight: "inherit",
              verticalAlign: "baseline",
            },
            ".cm-cursor": { borderLeftColor: "var(--primary)" },
            "&.cm-focused": { outline: "2px solid var(--ring)", outlineOffset: "-2px" },
            ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
              backgroundColor: "var(--accent) !important",
            },
            ".cm-content ::selection": { backgroundColor: "var(--accent)" },
          }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) core.edit(update.state.doc.toString());
          }),
        ],
      }),
    });
    viewRef.current = view;
    return () => {
      viewRef.current = null;
      view.destroy();
      core.dispose();
    };
  }, [core]);
  useEffect(() => {
    const guard = (event: BeforeUnloadEvent) => {
      if (!core.document.content) return;
      event.preventDefault();
    };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  }, [core]);

  function download() {
    const blob = new Blob([core.document.content], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "writer.md";
    anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }

  const selected = core.observations.find((item) => item.id === core.selectedId);
  const active = core.observations.filter((item) => item.status === "active");
  return (
    <div className="flex h-full min-h-0 flex-col lg:flex-row">
      <section aria-label="Document" className="flex min-h-0 min-w-0 flex-1 flex-col bg-muted/20">
        <div className="flex flex-wrap items-center gap-3 border-b px-5 py-3 text-sm">
          <h1 className="mr-auto font-semibold">Writer</h1>
          <span className="text-muted-foreground">
            Revision {core.document.revision} · {active.length} unresolved
          </span>
          <DropdownMenu>
            <DropdownMenuTrigger asChild disabled={modelDisabled}>
              <button
                aria-label="Writer model"
                className="flex h-7 max-w-48 items-center gap-1.5 rounded border bg-background px-2 text-xs text-foreground disabled:pointer-events-none disabled:opacity-50"
                type="button"
              >
                <span className="truncate">{selectedModelLabel}</span>
                <ChevronDown aria-hidden="true" className="size-3 shrink-0 text-muted-foreground" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-64 p-0">
              <ModelFilterList
                defaultLabel="Deployment default"
                groupedModels={groupedModels}
                model={selectedModel}
                onModelChange={(model) => core.setModel(model)}
              />
            </DropdownMenuContent>
          </DropdownMenu>
          {(["heading", "bold", "italic"] as const).map((kind) => (
            <Button
              key={kind}
              aria-label={`Format ${kind}`}
              title={`${kind} (${kind === "heading" ? "Ctrl/⌘+Alt+1" : `Ctrl/⌘+${kind === "bold" ? "B" : "I"}`})`}
              variant="outline"
              size="sm"
              onClick={() => viewRef.current && formatMarkdown(viewRef.current, kind)}
              type="button"
            >
              {kind === "heading" ? "H1" : kind === "bold" ? "B" : "I"}
            </Button>
          ))}
          <Button
            variant="outline"
            size="sm"
            onClick={() => viewRef.current && undo(viewRef.current)}
            type="button"
          >
            Undo
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => viewRef.current && redo(viewRef.current)}
            type="button"
          >
            Redo
          </Button>
          <Button variant="outline" size="sm" onClick={download} type="button">
            Download .md
          </Button>
        </div>
        <div
          className="m-3 min-h-0 flex-1 overflow-hidden rounded-lg border border-border bg-background shadow-sm"
          ref={host}
        />
        <p className="border-t px-5 py-2 text-xs text-muted-foreground">
          This document exists only in this tab. Download before leaving. Analysis sends the current
          document and recent context to the server; no browser storage is used automatically.
        </p>
      </section>
      <aside
        aria-label="Writer observations"
        className="flex max-h-[50%] min-h-0 w-full flex-col border-t bg-muted/20 lg:max-h-none lg:w-[360px] lg:border-l lg:border-t-0"
      >
        <div className="flex items-center justify-between gap-2 border-b p-4 text-sm">
          <h2 className="font-semibold">Observations</h2>
          <Button
            aria-pressed={core.paused}
            variant="outline"
            size="sm"
            onClick={() => core.setPaused(!core.paused)}
            type="button"
          >
            {core.paused ? "Resume" : "Pause"}
          </Button>
        </div>
        <div aria-live="polite" className="px-4 pt-2 text-xs text-muted-foreground">
          {core.status}
        </div>
        {core.error && (
          <div className="m-4 rounded border p-3 text-sm" role="alert">
            {core.error}{" "}
            <Button variant="link" size="sm" onClick={() => core.retry()} type="button">
              Retry analysis
            </Button>
          </div>
        )}
        {core.document.content.length > 100_000 && (
          <p className="px-4 text-sm text-destructive" role="alert">
            Document exceeds 100,000 characters. No requests will be sent until it is shorter.
          </p>
        )}
        <div className="min-h-0 flex-1 overflow-auto p-4">
          {active.length === 0 && (
            <p className="text-sm text-muted-foreground">No open observations.</p>
          )}
          {active.map((item) => (
            <article className="mb-3 rounded-lg border bg-background p-3 text-sm" key={item.id}>
              <p className="mb-2 text-xs text-muted-foreground">Revision {item.revision}</p>
              <p className="whitespace-pre-wrap break-words">{item.text}</p>
              <div className="mt-3 flex gap-3">
                <Button variant="link" size="sm" onClick={() => core.select(item.id)} type="button">
                  Discuss
                </Button>
                <Button
                  variant="link"
                  size="sm"
                  onClick={() => core.dismiss(item.id)}
                  type="button"
                >
                  Dismiss
                </Button>
              </div>
            </article>
          ))}
          {selected && (
            <section aria-label="Discussion" className="border-t pt-4" ref={discussionHost}>
              <h3 className="mb-2 text-sm font-semibold">
                Discussion · revision {selected.revision}
              </h3>
              <p className="mb-3 whitespace-pre-wrap break-words text-sm">{selected.text}</p>
              <ChatTranscript
                showToolCalls={false}
                agentName="Writer"
                messages={core.discussion.map((entry, index) => ({
                  id: `writer-${index}`,
                  role: entry.role,
                  content: entry.text,
                }))}
              />
              <ChatComposer
                key={selected.id}
                disabled={!available || core.busy !== undefined}
                onSend={async (message) => core.discuss(message)}
              />
            </section>
          )}
        </div>
      </aside>
    </div>
  );
}
