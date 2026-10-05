// SPDX-License-Identifier: Apache-2.0

import { history, historyKeymap, isolateHistory, redo, undo } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import {
  ChangeSet,
  EditorSelection,
  EditorState,
  StateEffect,
  StateField,
} from "@codemirror/state";
import {
  Decoration,
  drawSelection,
  EditorView,
  keymap,
  lineNumbers,
  placeholder,
} from "@codemirror/view";
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
import { Switch } from "../../components/ui/switch";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import { ChatComposer, type ComposerModelOption } from "../chat/chat-composer";
import { ChatTranscript } from "../chat/chat-transcript";
import { groupModels, ModelFilterList } from "../chat/model-effort-menu";
import { locateQuote, WriterCore, type WriterTransport } from "./writer-core";
import { WriterRecovery } from "./writer-recovery";

const revealPassages = StateEffect.define<Array<{ from: number; to: number }>>();
const passages = StateField.define({
  create: () => Decoration.none,
  update(value, transaction) {
    if (transaction.docChanged) return Decoration.none;
    for (const effect of transaction.effects) {
      if (effect.is(revealPassages))
        return Decoration.set(
          effect.value.map(({ from, to }) =>
            Decoration.mark({ class: "cm-writer-passage" }).range(from, to),
          ),
          true,
        );
    }
    return value;
  },
  provide: (field) => EditorView.decorations.from(field),
});

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
  const persistRef = useRef<() => void>(() => {});
  const coreRef = useRef<WriterCore | null>(null);
  if (!coreRef.current)
    coreRef.current = new WriterCore(transport, () => {
      refresh((value) => value + 1);
      persistRef.current();
    });
  const core = coreRef.current;
  const recoveryRef = useRef<WriterRecovery | null>(null);
  const initialized = useRef(false);
  const enabled = useRef(false);
  const saving = useRef(false);
  const pending = useRef(false);
  const [saveState, setSaveState] = useState("Clear · not saved in this browser");
  const [briefEditing, setBriefEditing] = useState(false);
  const [briefInput, setBriefInput] = useState("");
  const [decisionInput, setDecisionInput] = useState("");
  const [revealStatus, setRevealStatus] = useState("");
  if (!recoveryRef.current) {
    try {
      recoveryRef.current = new WriterRecovery(window.localStorage, navigator.locks);
    } catch {
      /* browser storage is unavailable */
    }
  }
  const recovery = recoveryRef.current;
  persistRef.current = () => {
    if (!enabled.current || !recovery) return;
    pending.current = true;
    if (saving.current) return;
    saving.current = true;
    const flush = async () => {
      while (pending.current && enabled.current) {
        pending.current = false;
        setSaveState("Saving…");
        if (!(await recovery.save(core.snapshot()))) {
          setSaveState(
            "Save failed · download your draft; another tab or storage may have changed",
          );
          break;
        }
        setSaveState("Saved in this browser");
      }
      saving.current = false;
    };
    void flush();
  };
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
  const host = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  useEffect(() => {
    if (!host.current) return;
    if (!initialized.current) {
      initialized.current = true;
      const saved = recovery?.read();
      if (saved?.kind === "saved") {
        core.restore(saved.saved.snapshot);
        enabled.current = true;
        setSaveState("Saved in this browser");
      } else if (saved?.kind === "corrupt")
        setSaveState("Save failed · saved data is invalid; draft not restored");
      else if (saved?.kind === "unavailable" || !recovery?.available)
        setSaveState("Saving unavailable · download your draft");
    }
    core.revive();
    const view = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: core.document.content,
        extensions: [
          history(),
          passages,
          markdown(),
          syntaxHighlighting(writerHighlight),
          placeholder("Start writing in Markdown…"),
          drawSelection(),
          lineNumbers(),
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
            ".cm-scroller": {
              overflow: "auto",
              fontFamily: "inherit",
              lineHeight: "1.8",
              justifyContent: "center",
            },
            ".cm-gutters": {
              backgroundColor: "transparent",
              color: "var(--muted-foreground)",
              border: "none",
            },
            ".cm-content": {
              boxSizing: "border-box",
              flex: "0 1 75ch",
              width: "75ch",
              minWidth: "0",
              padding: "2rem 1.5rem",
              margin: "0",
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
            ".cm-writer-passage": {
              backgroundColor: "var(--accent)",
              outline: "1px solid var(--primary)",
            },
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
  }, [core, recovery]);
  useEffect(() => {
    if (!recovery) return;
    const changed = (event: StorageEvent) => {
      if (event.key === recovery.storageKey && enabled.current) {
        enabled.current = false;
        setSaveState("Save failed · another tab changed this draft; download before leaving");
      }
    };
    window.addEventListener("storage", changed);
    return () => window.removeEventListener("storage", changed);
  }, [recovery]);
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

  function reveal(quote: string[]) {
    const matches = quote.map((text) => locateQuote(core.document.content, text));
    if (matches.some((match) => !match)) {
      setRevealStatus(
        "Earlier draft / changed · passage is missing or ambiguous in the current draft",
      );
      viewRef.current?.dispatch({ effects: revealPassages.of([]) });
      return;
    }
    const ranges = matches.filter((match) => match !== undefined);
    const first = ranges[0];
    if (!first) return;
    viewRef.current?.dispatch({
      effects: [revealPassages.of(ranges), EditorView.scrollIntoView(first.from, { y: "center" })],
    });
    setRevealStatus("Passage highlighted in the current draft; caret unchanged");
  }

  const selected = core.observations.find((item) => item.id === core.selectedId);
  const active = core.observations.filter((item) => item.status === "open");
  const historyItems = core.observations.filter((item) => item.status !== "open");
  function observation(item: (typeof core.observations)[number]) {
    return (
      <article className="mb-3 rounded-lg border bg-background p-3 text-sm" key={item.id}>
        <p className="mb-2 text-xs text-muted-foreground">
          {item.status === "not-relevant"
            ? "Not relevant"
            : item.status === "addressed"
              ? "Addressed"
              : "Open"}
          {item.revision !== core.document.revision && " · earlier draft"}
        </p>
        <p className="whitespace-pre-wrap break-words">{item.text}</p>
        {(item.quotes?.length || item.quote) && (
          <div className="mt-2">
            <p className="truncate text-xs text-muted-foreground">
              {(item.quotes ?? (item.quote ? [item.quote] : []))
                .map((quote) => `“${quote}”`)
                .join(" · ")}
            </p>
            <Button
              variant="link"
              size="sm"
              type="button"
              onClick={() => reveal(item.quotes ?? (item.quote ? [item.quote] : []))}
            >
              Reveal passage
            </Button>
          </div>
        )}
        <div className="mt-3 flex flex-wrap gap-2">
          <Button
            variant="link"
            size="sm"
            onClick={() => {
              core.select(item.id);
              setDecisionInput(item.decision ?? "");
            }}
            type="button"
          >
            {core.selectedId === item.id ? "Viewing thread" : "Open thread"}
          </Button>
          {item.status === "open" ? (
            <>
              <Button
                variant="link"
                size="sm"
                onClick={() => core.setObservationStatus(item.id, "addressed")}
                type="button"
              >
                Addressed
              </Button>
              <Button
                variant="link"
                size="sm"
                onClick={() => core.setObservationStatus(item.id, "not-relevant")}
                type="button"
              >
                Not relevant
              </Button>
            </>
          ) : (
            <Button
              variant="link"
              size="sm"
              onClick={() => core.setObservationStatus(item.id, "open")}
              type="button"
            >
              Reopen
            </Button>
          )}
        </div>
      </article>
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col lg:flex-row">
      <section aria-label="Document" className="flex min-h-0 min-w-0 flex-1 flex-col bg-muted/20">
        <div className="flex flex-nowrap items-center gap-3 overflow-x-auto border-b px-5 py-3 text-sm">
          <h1 className="mr-auto font-semibold">Writer</h1>
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
        <div className="border-b px-5 py-2 text-sm">
          {!briefEditing ? (
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setBriefInput(core.brief);
                  setBriefEditing(true);
                }}
                type="button"
              >
                {core.brief ? "Edit writing brief" : "Add brief"}
              </Button>
              {core.brief && (
                <span className="max-w-full truncate text-xs text-muted-foreground">
                  {core.brief}
                </span>
              )}
              <span className="text-xs text-muted-foreground">
                Used to guide feedback—not included in your document
              </span>
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <label htmlFor="writer-brief">
                Writing brief · Used to guide feedback—not included in your document
              </label>
              <textarea
                id="writer-brief"
                className="w-full rounded border bg-background p-2"
                rows={3}
                maxLength={2000}
                placeholder="e.g. Audience: new operators; purpose: explain the rollout; focus feedback on unsupported claims"
                value={briefInput}
                onChange={(event) => setBriefInput(event.target.value)}
              />
              <div className="flex gap-2">
                <Button
                  size="sm"
                  onClick={() => {
                    core.setBrief(briefInput.trim());
                    setBriefEditing(false);
                  }}
                  type="button"
                >
                  Apply brief
                </Button>
                {core.brief && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      core.setBrief("");
                      setBriefEditing(false);
                    }}
                    type="button"
                  >
                    Clear brief
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setBriefEditing(false)}
                  type="button"
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </div>
        <div
          className="m-3 min-h-0 flex-1 overflow-hidden rounded-lg border border-border bg-background shadow-sm"
          ref={host}
        />
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-t px-5 py-2 text-xs text-muted-foreground">
          <span role="status">{saveState}</span>
          {!enabled.current &&
            recovery?.available &&
            !saveState.startsWith("Saving unavailable") && (
              <Button
                variant="link"
                size="sm"
                type="button"
                onClick={() => {
                  enabled.current = true;
                  persistRef.current();
                }}
              >
                Save locally (opt in)
              </Button>
            )}
          {(enabled.current || saveState.startsWith("Save failed")) && recovery?.available && (
            <Button
              variant="link"
              size="sm"
              type="button"
              onClick={async () => {
                if (
                  !window.confirm(
                    "Forget this browser's saved Writer draft? Your current tab stays open, but the saved copy cannot be recovered.",
                  )
                )
                  return;
                // Stop any new writes, then let the lock serialize with a pending save.
                enabled.current = false;
                if (await recovery?.forget()) setSaveState("Clear · not saved in this browser");
                else setSaveState("Save failed · could not forget saved draft");
                refresh((value) => value + 1);
              }}
            >
              Forget local draft
            </Button>
          )}
          <details>
            <summary>Storage and privacy</summary>
            <p className="mt-1 max-w-prose">
              Browser storage is not a backup or cross-device sync. Existing Studio account cleanup
              removes saved data on sign-out or account change. Analysis sends this document and
              recent context to the configured provider. Download .md works independently.
            </p>
          </details>
        </div>
      </section>
      <aside
        aria-label="Writer observations"
        className="flex h-[40%] min-h-0 w-full shrink-0 flex-col border-t bg-muted/20 lg:h-full lg:w-[360px] lg:border-l lg:border-t-0"
      >
        <div className="flex flex-col gap-3 border-b p-4 text-sm">
          <h2 className="font-semibold">Observations</h2>
          <label
            className="flex min-h-11 items-center justify-between gap-3"
            htmlFor="automatic-feedback"
          >
            <span>Automatic feedback</span>
            <Switch
              aria-label="Automatic feedback"
              checked={!core.paused}
              id="automatic-feedback"
              onCheckedChange={(checked) => core.setPaused(checked !== true)}
            />
          </label>
          {core.paused && (
            <p className="text-xs text-muted-foreground">
              Off: checks run only when you select Read this now.
            </p>
          )}
          <Button
            className="w-full"
            disabled={!available || core.busy !== undefined}
            onClick={() => void core.readNow()}
            type="button"
          >
            Read this now
          </Button>
        </div>
        <div aria-live="polite" className="px-4 pt-2 text-xs text-muted-foreground">
          {core.status}
        </div>
        {core.error && (
          <div className="m-4 rounded border p-3 text-sm" role="alert">
            {core.error}{" "}
            {!core.error.startsWith("Decision limit reached") && (
              <Button variant="link" size="sm" onClick={() => void core.retry()} type="button">
                Retry analysis
              </Button>
            )}
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
          {active.map(observation)}
          {historyItems.length > 0 && (
            <details>
              <summary className="cursor-pointer text-sm">
                History · {historyItems.length} closed threads
              </summary>
              <div className="pt-3">{historyItems.map(observation)}</div>
            </details>
          )}
          {revealStatus && (
            <p role="status" className="text-xs">
              {revealStatus}
            </p>
          )}
          <Button variant="outline" size="sm" type="button" onClick={() => core.select(undefined)}>
            Ask Writer · general discussion
          </Button>
          <section aria-label="Discussion" className="border-t pt-4">
            <h3 className="mb-2 text-sm font-semibold">
              {selected ? "Observation discussion" : "General discussion"}
            </h3>
            {selected && (
              <>
                <p className="mb-3 whitespace-pre-wrap break-words text-sm">{selected.text}</p>
                <label className="text-xs" htmlFor="writer-decision">
                  Author decision (optional, used in future context)
                </label>
                <input
                  id="writer-decision"
                  className="w-full rounded border bg-background p-2 text-sm"
                  maxLength={500}
                  value={decisionInput}
                  onChange={(event) => setDecisionInput(event.target.value)}
                />
                <Button
                  variant="link"
                  size="sm"
                  type="button"
                  onClick={() => core.confirmDecision(selected.id, decisionInput)}
                >
                  Confirm decision
                </Button>
              </>
            )}
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
              key={selected?.id ?? "general"}
              disabled={!available || core.busy === "discuss"}
              onSend={async (message) => core.discuss(message)}
            />
          </section>
        </div>
      </aside>
    </div>
  );
}
