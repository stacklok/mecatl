// SPDX-License-Identifier: Apache-2.0

import {
  history,
  historyKeymap,
  insertNewline,
  isolateHistory,
  redo,
  undo,
} from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import {
  ChangeSet,
  Compartment,
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
import { getCM, vim } from "@replit/codemirror-vim";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Plus, Settings2, X } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { Switch } from "../../components/ui/switch";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import { ChatComposer, type ComposerModelOption } from "../chat/chat-composer";
import { ChatTranscript } from "../chat/chat-transcript";
import { groupModels, ModelFilterList } from "../chat/model-effort-menu";
import { locateQuote, WriterCore, type WriterTransport } from "./writer-core";
import { readWriterDocument } from "./writer-document";
import { WriterRecovery } from "./writer-recovery";
import { acceptWriterReference, readWriterReference } from "./writer-reference";

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

function vimCommandMode(view: EditorView) {
  return getCM(view)?.state.vim?.insertMode === false;
}

function watchVimMode(view: EditorView, setMode: (mode: string) => void) {
  const cm = getCM(view);
  const updateMode = () => setMode((cm?.state.vim?.mode ?? "normal").toUpperCase());
  cm?.on("vim-mode-change", updateMode);
  updateMode();
}

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
  const [decisionInputs, setDecisionInputs] = useState<Record<string, string>>({});
  const [decisionStatus, setDecisionStatus] = useState("");
  const [revealStatus, setRevealStatus] = useState("");
  const [referenceError, setReferenceError] = useState("");
  const [selectionError, setSelectionError] = useState("");
  const [readingReferences, setReadingReferences] = useState(false);
  const [keyBindings, setKeyBindings] = useState<"standard" | "vim">("standard");
  const keyBindingsRef = useRef<"standard" | "vim">("standard");
  const [vimMode, setVimMode] = useState("NORMAL");
  const vimKeys = useRef(new Compartment());
  const readGeneration = useRef(0);
  const openGeneration = useRef(0);
  const fileInput = useRef<HTMLInputElement>(null);
  const replaceEditor = useRef<(content: string) => void>(() => {});
  const [openError, setOpenError] = useState("");
  const [documentKey, setDocumentKey] = useState(0);
  const mounted = useRef(false);
  const [selectedPassage, setSelectedPassage] = useState<
    { from: number; to: number; text: string } | undefined
  >();
  const [includedPassage, setIncludedPassage] = useState<
    { from: number; to: number; text: string } | undefined
  >();
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
    setReadingReferences(false);
    mounted.current = true;
    const extensions = [
      vimKeys.current.of(keyBindingsRef.current === "vim" ? vim() : []),
      EditorState.allowMultipleSelections.of(true),
      history(),
      passages,
      markdown(),
      syntaxHighlighting(writerHighlight),
      placeholder("What would you like to write?"),
      drawSelection(),
      lineNumbers(),
      keymap.of([
        {
          key: "Enter",
          run: (view) => vimCommandMode(view) || insertNewline(view),
        },
        {
          key: "Mod-b",
          run: (view) => vimCommandMode(view) || formatMarkdown(view, "bold"),
        },
        {
          key: "Mod-i",
          run: (view) => vimCommandMode(view) || formatMarkdown(view, "italic"),
        },
        {
          key: "Mod-Alt-1",
          run: (view) => vimCommandMode(view) || formatMarkdown(view, "heading"),
        },
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
        ".cm-fat-cursor": {
          backgroundColor: "var(--primary) !important",
          color: "var(--primary-foreground) !important",
        },
        "&:not(.cm-focused) .cm-fat-cursor": {
          backgroundColor: "transparent !important",
          outlineColor: "var(--primary)",
        },
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
    ];
    const view = new EditorView({
      parent: host.current,
      state: EditorState.create({ doc: core.document.content, extensions }),
    });
    replaceEditor.current = (content) => {
      view.setState(EditorState.create({ doc: content, extensions }));
      if (keyBindingsRef.current === "vim") {
        view.dispatch({ effects: vimKeys.current.reconfigure(vim()) });
        watchVimMode(view, setVimMode);
      }
    };
    viewRef.current = view;
    if (keyBindingsRef.current === "vim") watchVimMode(view, setVimMode);
    return () => {
      mounted.current = false;
      readGeneration.current++;
      openGeneration.current++;
      core.dispose();
      core.setReferenceLoading(false);
      core.setReferences([]);
      viewRef.current = null;
      replaceEditor.current = () => {};
      view.destroy();
    };
  }, [core, recovery]);
  function changeKeyBindings(value: string) {
    if (value !== "standard" && value !== "vim") return;
    const view = viewRef.current;
    if (!view || value === keyBindings) return;
    view.dispatch({ effects: vimKeys.current.reconfigure(value === "vim" ? vim() : []) });
    keyBindingsRef.current = value;
    setKeyBindings(value);
    if (value === "vim") watchVimMode(view, setVimMode);
  }
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

  async function openDocument(file: File | undefined) {
    if (!file) return;
    const generation = ++openGeneration.current;
    const source = core.document;
    setOpenError("");
    try {
      const content = await readWriterDocument(file);
      if (generation !== openGeneration.current || !mounted.current) return;
      if (core.document !== source) {
        setOpenError(
          "Draft changed while opening the file. Choose it again to replace the current draft.",
        );
        return;
      }
      if (
        source.content &&
        !window.confirm(
          "Replace the current Writer draft and clear its conversation, brief, decisions, references and undo history? Download it first if you want to keep it.",
        )
      )
        return;
      if (generation !== openGeneration.current || !mounted.current) return;
      if (core.document !== source) {
        setOpenError("Draft changed while confirming. Choose the file again to replace it.");
        return;
      }
      readGeneration.current++;
      setReadingReferences(false);
      setReferenceError("");
      setSelectedPassage(undefined);
      setIncludedPassage(undefined);
      setSelectionError("");
      setRevealStatus("");
      setDecisionInputs({});
      setDecisionStatus("");
      setBriefEditing(false);
      setBriefInput("");
      setDocumentKey((value) => value + 1);
      core.openDocument(content);
      replaceEditor.current(content);
    } catch (error) {
      if (generation === openGeneration.current && mounted.current)
        setOpenError(
          error instanceof Error ? error.message : "Could not read document. Try again.",
        );
    }
  }

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

  function capturePassage() {
    const view = viewRef.current;
    if (!view) return;
    const selection = view.state.selection;
    if (selection.ranges.length !== 1 || getCM(view)?.state.vim?.visualBlock) {
      setSelectedPassage(undefined);
      setSelectionError(
        "Select one contiguous passage (character or line), not a block or multiple ranges.",
      );
      return;
    }
    setSelectionError("");
    const range = selection.main;
    setSelectedPassage(
      range.from < range.to
        ? { from: range.from, to: range.to, text: view.state.sliceDoc(range.from, range.to) }
        : undefined,
    );
  }

  async function addReferences(files: FileList | null) {
    if (!files?.length) return;
    const generation = ++readGeneration.current;
    core.setReferenceLoading(true);
    setReadingReferences(true);
    setReferenceError("");
    const next = [...core.references];
    for (const file of Array.from(files)) {
      if (generation !== readGeneration.current || !mounted.current) return;
      if (next.length >= 3) {
        setReferenceError("Only three reference files can be attached.");
        break;
      }
      try {
        const reference = await readWriterReference(file);
        if (generation !== readGeneration.current || !mounted.current) return;
        const error = acceptWriterReference(next, reference);
        if (error) {
          setReferenceError(error);
          break;
        }
        next.push(reference);
      } catch (error) {
        if (generation !== readGeneration.current || !mounted.current) return;
        setReferenceError(
          error instanceof Error ? error.message : "Could not read reference file.",
        );
      }
    }
    if (
      generation === readGeneration.current &&
      mounted.current &&
      next.length !== core.references.length
    )
      core.setReferences(next);
    if (generation === readGeneration.current && mounted.current) {
      core.setReferenceLoading(false);
      setReadingReferences(false);
    }
  }

  const selected = core.observations.find((item) => item.id === core.selectedId);
  const decisionInput = selected ? (decisionInputs[selected.id] ?? selected.decision ?? "") : "";
  const savedDecision = selected?.decision ?? "";
  const decisionChanged = decisionInput.trim() !== savedDecision;
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
              setDecisionInputs((inputs) =>
                inputs[item.id] === undefined
                  ? { ...inputs, [item.id]: item.decision ?? "" }
                  : inputs,
              );
              setDecisionStatus("");
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
          <h1 className="mr-auto font-semibold">Document</h1>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                aria-label="Document key bindings"
                title="Document key bindings"
                size="icon"
                variant="ghost"
                type="button"
                className="size-8 shrink-0"
              >
                <Settings2 aria-hidden="true" className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel>Key bindings (document only)</DropdownMenuLabel>
              <DropdownMenuRadioGroup value={keyBindings} onValueChange={changeKeyBindings}>
                <DropdownMenuRadioItem value="standard">Standard</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="vim">Vim</DropdownMenuRadioItem>
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
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
          <input
            aria-label="Choose document to open"
            accept=".md,.markdown,.txt,text/plain,text/markdown"
            className="hidden"
            ref={fileInput}
            type="file"
            onChange={(event) => {
              const file = event.currentTarget.files?.[0];
              event.currentTarget.value = "";
              void openDocument(file);
            }}
          />
          <Button
            variant="outline"
            size="sm"
            onClick={() => fileInput.current?.click()}
            type="button"
          >
            Open document…
          </Button>
          <Button variant="outline" size="sm" onClick={download} type="button">
            Download .md
          </Button>
        </div>
        {openError && (
          <p className="border-b px-5 py-2 text-sm text-destructive" role="alert">
            {openError}
          </p>
        )}
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
                placeholder="e.g. Audience: new operators; purpose: explain the rollout"
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
          {keyBindings === "vim" && (
            <span
              role="status"
              aria-live="polite"
              aria-label={`Document Vim mode: ${vimMode}`}
              className="rounded border px-1.5 font-mono"
            >
              {vimMode}
            </span>
          )}
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
        aria-label="Writer conversation"
        className="flex h-[40%] min-h-0 w-full shrink-0 flex-col border-t bg-muted/20 lg:h-full lg:w-[360px] lg:border-l lg:border-t-0"
      >
        <div className="min-h-0 shrink overflow-auto">
          {core.proposalError && (
            <p role="alert" className="p-4 text-sm">
              {core.proposalError}
            </p>
          )}
        </div>
        <div className="flex flex-col gap-3 border-b p-4 text-sm">
          <h2 className="font-semibold">Writer</h2>
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
            variant="outline"
            size="sm"
            disabled={!available || core.busy !== undefined || readingReferences}
            onClick={() => void core.readNow()}
            type="button"
          >
            Read this now
          </Button>
        </div>
        {core.status && (
          <div aria-live="polite" className="px-4 pt-2 text-xs text-muted-foreground">
            {core.status}
          </div>
        )}
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
            <p className="text-sm text-muted-foreground">
              Observations will appear here as you write.
            </p>
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
          <section aria-label="Discussion" className="border-t pt-4">
            {selected && (
              <>
                <Button
                  variant="link"
                  size="sm"
                  type="button"
                  onClick={() => core.select(undefined)}
                >
                  ← Back to conversation
                </Button>
                <h3 className="mt-2 text-sm font-semibold">Observation discussion</h3>
                <blockquote className="my-3 border-l-2 pl-3 text-sm text-muted-foreground">
                  {selected.text}
                </blockquote>
                <form
                  className="flex flex-col gap-2"
                  onSubmit={(event) => {
                    event.preventDefault();
                    if (!selected || !decisionInput.trim() || !decisionChanged) return;
                    if (core.confirmDecision(selected.id, decisionInput)) {
                      setDecisionInputs((inputs) => ({
                        ...inputs,
                        [selected.id]: decisionInput.trim(),
                      }));
                      setDecisionStatus("Decision saved.");
                    }
                  }}
                >
                  <label className="text-xs" htmlFor="writer-decision">
                    Decision for this draft
                  </label>
                  <input
                    id="writer-decision"
                    className="w-full rounded border bg-background p-2 text-sm"
                    maxLength={500}
                    value={decisionInput}
                    onChange={(event) => {
                      setDecisionInputs((inputs) => ({
                        ...inputs,
                        [selected.id]: event.target.value,
                      }));
                      setDecisionStatus("");
                    }}
                  />
                  <p className="text-xs text-muted-foreground">
                    Used in future feedback; does not change the document.
                  </p>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      size="sm"
                      type="submit"
                      disabled={!decisionInput.trim() || !decisionChanged}
                    >
                      Save decision
                    </Button>
                    {savedDecision && (
                      <Button
                        size="sm"
                        variant="outline"
                        type="button"
                        onClick={() => {
                          if (core.confirmDecision(selected.id, "")) {
                            setDecisionInputs((inputs) => ({ ...inputs, [selected.id]: "" }));
                            setDecisionStatus("Decision cleared.");
                          }
                        }}
                      >
                        Clear decision
                      </Button>
                    )}
                  </div>
                  {(decisionStatus || (savedDecision && !decisionChanged)) && (
                    <p aria-live="polite" className="text-xs text-muted-foreground" role="status">
                      {decisionStatus || "Saved decision."}
                    </p>
                  )}
                </form>
              </>
            )}
            {core.proposal && core.proposal.originId === core.selectedId && (
              <section
                aria-label="Writer preview"
                className="my-3 flex flex-col gap-2 rounded border p-3 text-sm"
              >
                <h4 className="font-semibold">
                  {core.proposal.kind === "start" ? "Starting point preview" : "Revision preview"}
                </h4>
                <p>Before</p>
                <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-words rounded border p-2">
                  {core.proposal.before || "(empty document)"}
                </pre>
                <label htmlFor="writer-candidate">After (editable)</label>
                <textarea
                  id="writer-candidate"
                  className="w-full rounded border bg-background p-2"
                  rows={7}
                  maxLength={4000}
                  value={core.proposal.candidate}
                  onChange={(event) => core.updateCandidate(event.target.value)}
                />
                <div className="flex gap-2">
                  <Button
                    type="button"
                    size="sm"
                    disabled={!available || core.busy !== undefined}
                    onClick={() => {
                      const view = viewRef.current;
                      const proposal = core.proposal;
                      if (!view || !proposal) return;
                      core.applyProposal((from, to, text) => {
                        if (
                          view.state.doc.toString() !== core.document.content ||
                          view.state.sliceDoc(from, to) !== proposal.before
                        )
                          return false;
                        view.dispatch({
                          changes: { from, to, insert: text },
                          annotations: isolateHistory.of("full"),
                          userEvent: "input",
                        });
                        return true;
                      });
                    }}
                  >
                    {core.proposal.kind === "start" ? "Use this starting point" : "Apply"}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => core.discardProposal()}
                  >
                    Discard
                  </Button>
                </div>
              </section>
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
              key={`${documentKey}-${selected?.id ?? "general"}`}
              ariaLabel="Ask Writer"
              placeholder="Ask a question or describe a change…"
              disabled={!available || core.busy === "discuss" || readingReferences}
              accessory={
                <>
                  {core.references.length > 0 && (
                    <div className="px-2 pt-1">
                      <p className="mb-1 text-xs font-medium text-muted-foreground">References</p>
                      <ul
                        aria-label="Attached reference snapshots"
                        className="flex flex-wrap gap-1"
                      >
                        {core.references.map((file, index) => (
                          <li
                            className="flex max-w-full items-center gap-1 rounded-full border bg-muted/30 py-1 pr-1 pl-2 text-xs"
                            key={file.name}
                          >
                            <span className="truncate">{file.name}</span>
                            <button
                              aria-label={`Remove ${file.name}`}
                              className="rounded-full p-0.5 hover:bg-accent"
                              onClick={() => {
                                readGeneration.current++;
                                setReadingReferences(false);
                                core.setReferences(
                                  core.references.filter((_, position) => position !== index),
                                );
                                core.setReferenceLoading(false);
                                setReferenceError("");
                              }}
                              type="button"
                            >
                              <X aria-hidden="true" className="size-3" />
                            </button>
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                  {includedPassage && (
                    <div className="flex items-center gap-1 px-2 pt-1 text-xs text-muted-foreground">
                      <span className="min-w-0 truncate rounded-full border bg-muted/30 px-2 py-1">
                        Passage: {includedPassage.text}
                      </span>
                      <button
                        aria-label="Remove selected passage"
                        className="rounded-full p-1 hover:bg-accent"
                        onClick={() => setIncludedPassage(undefined)}
                        type="button"
                      >
                        <X aria-hidden="true" className="size-3" />
                      </button>
                    </div>
                  )}
                  {readingReferences && (
                    <p className="px-2 pt-1 text-xs text-muted-foreground" role="status">
                      Reading reference files…
                    </p>
                  )}
                  {selectionError && (
                    <p className="px-2 pt-1 text-xs text-destructive" role="alert">
                      {selectionError}
                    </p>
                  )}
                  {referenceError && (
                    <p className="px-2 pt-1 text-xs text-destructive" role="alert">
                      {referenceError}
                    </p>
                  )}
                </>
              }
              leadingControls={
                <>
                  <input
                    accept=".txt,.md,.markdown,.csv,.log,.xml,.go,.js,.ts,.tsx,.jsx,.py,.rs,.json,.yaml,.yml,.css,.html,.java,.c,.sh,.sql,text/*"
                    aria-label="Attach reference files"
                    hidden
                    id="writer-references"
                    multiple
                    onChange={(event) => {
                      void addReferences(event.target.files);
                      event.target.value = "";
                    }}
                    type="file"
                  />
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button
                        onPointerDownCapture={capturePassage}
                        onKeyDownCapture={(event) => {
                          if (event.key === "Enter" || event.key === " ") capturePassage();
                        }}
                        aria-label="Add context"
                        className="size-11 shrink-0 rounded-full"
                        disabled={!available || readingReferences}
                        size="icon"
                        type="button"
                        variant="ghost"
                      >
                        <Plus aria-hidden="true" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="start" className="w-72">
                      <DropdownMenuLabel className="whitespace-normal text-xs font-normal text-muted-foreground">
                        File contents are sent with Writer requests. They aren’t saved on reload. Up
                        to 3 files, 8,000 bytes each, 16,000 bytes total.
                      </DropdownMenuLabel>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onSelect={() => document.getElementById("writer-references")?.click()}
                      >
                        Attach reference files
                      </DropdownMenuItem>
                      {selectedPassage && (
                        <DropdownMenuItem onSelect={() => setIncludedPassage(selectedPassage)}>
                          Use selected passage
                        </DropdownMenuItem>
                      )}
                    </DropdownMenuContent>
                  </DropdownMenu>
                </>
              }
              onSend={async (message) => {
                if (readingReferences) return false;
                const view = viewRef.current;
                if (
                  includedPassage &&
                  (!view ||
                    view.state.sliceDoc(includedPassage.from, includedPassage.to) !==
                      includedPassage.text)
                ) {
                  setIncludedPassage(undefined);
                  setReferenceError("Selected passage changed. Select it again before sending.");
                  return false;
                }
                return core.discuss(message, includedPassage);
              }}
            />
          </section>
        </div>
      </aside>
    </div>
  );
}
