// SPDX-License-Identifier: Apache-2.0

import { ArrowUp, LoaderCircle, Mic, Paperclip, Plus, X } from "lucide-react";
import {
  type DragEvent,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Button } from "../../components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../../components/ui/dialog";
import { Textarea } from "../../components/ui/textarea";
import {
  classifyAttachment,
  inlineTextAttachments,
  MAX_INLINE_TEXT_TOTAL_BYTES,
  readTextAttachment,
} from "../../lib/attachment-inline";
import { readDraft, writeDraft } from "../../lib/draft-store";
import { fileKindMeta } from "../../lib/file-meta";
import { MODE_OPTIONS, modeTitle, type SessionPermissionMode } from "../../lib/permission-mode";
import type { EnterSendBehavior } from "../../lib/profile-preferences";
import { cn } from "../../lib/utils";
import { ClearDraftButton } from "./clear-draft-button";
import { composerFrameClass } from "./composer-frame";
import { ComposerOptionMenu } from "./composer-option-menu";
import {
  type ComposerModeControl,
  type ComposerModelControl,
  ComposerOptionsSheet,
  type ComposerToolsControl,
  TOOL_OPTIONS,
} from "./composer-options-sheet";
import {
  acceptImageAttachments,
  type ImageAttachment,
  imageSource,
  type LocalFilePreview,
  localFileKind,
  maxImageAttachmentCount,
  readImageAttachment,
} from "./local-file-preview";
import { groupModels, ModelEffortMenu } from "./model-effort-menu";
import { useVoiceInput } from "./use-voice-input";

export interface ComposerModelOption {
  id: string;
  image: boolean;
  label: string;
  providerId: string;
  /** The inventory's `reasoning` flag: whether the model reports a reasoning-effort tier. */
  reasoning?: boolean;
}

export interface DraftChatConfiguration {
  mode: SessionPermissionMode;
  model?: { id: string; providerId: string };
  reasoningEffort: "default" | "high" | "low" | "max" | "medium" | "xhigh";
  toolAccess: "all" | "noFilesystem";
}

/**
 * A small text file staged for inlining (see `lib/attachment-inline.ts`). Its
 * content is read when it is attached, so a binary or size rejection shows at
 * once rather than at send time.
 */
interface StagedTextFile {
  id: string;
  name: string;
  size: number;
  text: string;
  type: string;
}

interface ChatComposerProps {
  /** A live chat's model can change: the deployment allows model selection. */
  canForkModel?: boolean;
  clearDraftSignal?: number;
  /** A draft's settings. Absent on a live chat, whose settings come from the `live*` props. */
  configuration?: DraftChatConfiguration;
  disabled?: boolean;
  /**
   * The draft store's key: a chat's session ID, or `new` for the draft view.
   * Switching keys swaps in that chat's own stored draft. Omit to keep the
   * draft in memory only.
   */
  draftKey?: string;
  imageAttachmentsSupported?: boolean;
  /** A live chat's Mode and Model pills are blocked: a run, a compaction, a fork, or a mode change is in progress. */
  liveControlsDisabled?: boolean;
  /** A live chat's mode, once its detail has loaded. */
  liveMode?: SessionPermissionMode;
  /** A live chat's model and effort, once its detail has loaded. */
  liveModel?: { id: string; providerId: string; reasoningEffort: string };
  models?: ComposerModelOption[];
  onConfigurationChange?: (configuration: DraftChatConfiguration) => void;
  onDraftChange?: (present: boolean) => void;
  /** A Model or Effort pick on a live chat, which forks it: always names a model and an effort. */
  onForkModel?: (
    model: { id: string; providerId: string },
    reasoningEffort: DraftChatConfiguration["reasoningEffort"],
  ) => void;
  /** A Mode pick on a live chat, which changes it in place. */
  onLiveModeChange?: (mode: SessionPermissionMode) => void;
  onPreviewFile?: (file: LocalFilePreview) => void;
  onPreviewImage?: (image: ImageAttachment) => void;
  onSeedConsumed?: () => void;
  onSend: (
    prompt: string,
    action: ComposerEnterAction,
    images: ImageAttachment[],
  ) => Promise<boolean>;
  /** A live chat's usage and Compact action, at the end of the pill row. */
  pillRowEnd?: ReactNode;
  seedCanConfirm?: boolean;
  seedContext?: SeedConfirmationContext;
  seedRequiresConfirmation?: boolean;
  seedText?: string;
  /** The same usage and Compact action, at the foot of the phone options sheet. */
  sheetFooter?: ReactNode;
  working?: boolean;
  workingBehavior?: EnterSendBehavior;
}

export interface SeedConfirmationContext {
  model: string;
  mode: string;
  target: string;
  toolAccess?: string;
}

export type ComposerEnterAction = "send" | "queue" | "steer" | "newline";

/**
 * The chat composer, in the prototype's (`stack-08`) frame: the input box
 * with its attachment pills and toolbar, then the pill row underneath for
 * Mode, Tools (a draft only), and Model. On a phone the pill row folds into
 * the options sheet.
 *
 * Studio's semantics stay: the Enter preference and `resolveComposerAction`,
 * the image limits, the seed confirmation, and voice input.
 */
export function ChatComposer({
  canForkModel = false,
  clearDraftSignal = 0,
  configuration,
  disabled = false,
  draftKey,
  imageAttachmentsSupported = false,
  liveControlsDisabled = false,
  liveMode,
  liveModel,
  models = [],
  onConfigurationChange,
  onDraftChange,
  onForkModel,
  onLiveModeChange,
  onPreviewFile,
  onPreviewImage,
  onSeedConsumed,
  onSend,
  pillRowEnd,
  seedCanConfirm = true,
  seedContext,
  seedRequiresConfirmation = false,
  seedText,
  sheetFooter,
  working = false,
  workingBehavior = "queue",
}: ChatComposerProps) {
  const [prompt, setPrompt] = useState(() => (draftKey === undefined ? "" : readDraft(draftKey)));
  const [draftOwner, setDraftOwner] = useState(draftKey);
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const [textFiles, setTextFiles] = useState<StagedTextFile[]>([]);
  const [attachmentError, setAttachmentError] = useState("");
  const [readingAttachments, setReadingAttachments] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [isDragOver, setIsDragOver] = useState(false);
  const [seedConfirmation, setSeedConfirmation] = useState<string>();
  const fileInput = useRef<HTMLInputElement>(null);
  const imageCapability = useRef(imageAttachmentsSupported);
  imageCapability.current = imageAttachmentsSupported;
  const latestDraftKey = useRef(draftKey);
  latestDraftKey.current = draftKey;
  const composing = useRef(false);
  const compositionSequence = useRef(0);
  const sawCompositionEnter = useRef(false);
  const suppressCommitEnter = useRef(false);
  const submittingRef = useRef(false);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const updatePrompt = useCallback((value: string) => setPrompt(value), []);
  const voice = useVoiceInput(prompt, updatePrompt);

  // Another chat: swap in its stored draft and drop the staged files. A send
  // in flight keeps its text and files instead: a draft's first send opens
  // the chat it created, and a refused send must leave the prompt in place.
  if (draftOwner !== draftKey) {
    setDraftOwner(draftKey);
    if (!submitting) {
      setPrompt(draftKey === undefined ? "" : readDraft(draftKey));
      setImages([]);
      setTextFiles([]);
      setAttachmentError("");
    }
  }

  // Persist on every change rather than on a timer, so nothing writes after
  // an account change has cleared storage and before this tree unmounts.
  useEffect(() => {
    if (draftKey === undefined || draftOwner !== draftKey) return;
    writeDraft(draftKey, prompt);
  }, [draftKey, draftOwner, prompt]);

  const hasDraftContent = Boolean(prompt) || images.length > 0 || textFiles.length > 0;
  useEffect(() => {
    onDraftChange?.(hasDraftContent);
  }, [hasDraftContent, onDraftChange]);
  useEffect(() => {
    if (!clearDraftSignal) return;
    setPrompt("");
    setImages([]);
    setTextFiles([]);
    setAttachmentError("");
    setSeedConfirmation(undefined);
  }, [clearDraftSignal]);

  // A starter-prompt chip seeds the draft text without sending it — the
  // caller clears seedText (onSeedConsumed) once applied so re-clicking the
  // same chip still re-seeds.
  // biome-ignore lint/correctness/useExhaustiveDependencies: onSeedConsumed is stable per caller and re-running on it would re-seed after every consume
  useEffect(() => {
    if (seedText === undefined) return;
    setPrompt(seedText);
    setSeedConfirmation(seedRequiresConfirmation ? seedText : undefined);
    onSeedConsumed?.();
    if (!seedRequiresConfirmation) textarea.current?.focus();
  }, [seedText]);
  useEffect(() => {
    if (imageAttachmentsSupported || images.length === 0) return;
    setImages([]);
    setAttachmentError("Image attachments were removed because this model does not support them.");
  }, [imageAttachmentsSupported, images.length]);
  const groupedModels = useMemo(() => groupModels(models), [models]);
  const busy = disabled || submitting;

  async function submit(
    action: ComposerEnterAction = working ? workingBehavior : "send",
    confirmedSeed = false,
  ): Promise<boolean> {
    const typed = prompt.trim();
    if (
      (!typed && images.length === 0 && textFiles.length === 0) ||
      disabled ||
      submittingRef.current ||
      (seedConfirmation !== undefined && !confirmedSeed)
    )
      return false;
    if (working && images.length > 0) {
      setAttachmentError("Wait for the active run to finish before sending images.");
      return false;
    }
    if (working && textFiles.length > 0) {
      setAttachmentError("Wait for the active run to finish before sending attachments.");
      return false;
    }
    let nextPrompt: string;
    try {
      nextPrompt = inlineTextAttachments(typed, textFiles);
    } catch (error) {
      setAttachmentError(
        error instanceof Error ? error.message : "Attachments could not be added.",
      );
      return false;
    }
    voice.stop();
    const submitKey = draftKey;
    submittingRef.current = true;
    setSubmitting(true);
    try {
      if (await onSend(nextPrompt, action, images)) {
        setPrompt("");
        setImages([]);
        setTextFiles([]);
        setAttachmentError("");
        setSeedConfirmation(undefined);
        return true;
      }
      return false;
    } finally {
      // The draft moved to the chat its send opened; the old key's copy is spent.
      if (submitKey !== undefined && latestDraftKey.current !== submitKey)
        writeDraft(submitKey, "");
      submittingRef.current = false;
      setSubmitting(false);
    }
  }

  async function addFiles(files: File[]) {
    if (files.length === 0) return;
    setReadingAttachments(true);
    setAttachmentError("");
    const errors: string[] = [];
    const imageFiles: File[] = [];
    const textCandidates: File[] = [];
    for (const file of files) {
      const classification = classifyAttachment(file, { image: imageAttachmentsSupported });
      if (classification.kind === "rejected") errors.push(classification.reason);
      else if (classification.kind === "image") imageFiles.push(file);
      else textCandidates.push(file);
    }
    try {
      if (imageFiles.length > 0) errors.push(...(await addImages(imageFiles)));
      if (textCandidates.length > 0) errors.push(...(await addTextFiles(textCandidates)));
    } finally {
      setReadingAttachments(false);
      setAttachmentError(errors.join(" "));
    }
  }

  /** Studio's image path, unchanged: count, size, and total limits, and the model's capability. */
  async function addImages(files: File[]): Promise<string[]> {
    const remaining = maxImageAttachmentCount - images.length;
    if (remaining <= 0) return [`A prompt can include up to ${maxImageAttachmentCount} images.`];
    const selected = files.slice(0, remaining);
    const results = await Promise.allSettled(selected.map(readImageAttachment));
    if (!imageCapability.current) {
      return ["Images were not attached because this model does not support them."];
    }
    const next = results.flatMap((result) => (result.status === "fulfilled" ? [result.value] : []));
    const errors = results.flatMap((result) =>
      result.status === "rejected"
        ? [result.reason instanceof Error ? result.reason.message : "An image could not be read."]
        : [],
    );
    const selectedImages = acceptImageAttachments(images, next);
    errors.push(...selectedImages.errors);
    setImages((current) => [...current, ...selectedImages.accepted]);
    if (files.length > selected.length) {
      errors.push(`A prompt can include up to ${maxImageAttachmentCount} images.`);
    }
    return errors;
  }

  async function addTextFiles(files: File[]): Promise<string[]> {
    const results = await Promise.allSettled(
      files.map(async (file) => ({ file, text: await readTextAttachment(file) })),
    );
    let total = textFiles.reduce((sum, file) => sum + file.size, 0);
    const errors: string[] = [];
    const accepted: StagedTextFile[] = [];
    for (const result of results) {
      if (result.status === "rejected") {
        errors.push(
          result.reason instanceof Error ? result.reason.message : "A file could not be read.",
        );
        continue;
      }
      const { file, text } = result.value;
      if (total + file.size > MAX_INLINE_TEXT_TOTAL_BYTES) {
        errors.push(`${file.name}: adding this would exceed the 512 KiB total for attached text.`);
        continue;
      }
      total += file.size;
      accepted.push({
        id: crypto.randomUUID(),
        name: file.name,
        size: file.size,
        text,
        type: file.type,
      });
    }
    setTextFiles((current) => [...current, ...accepted]);
    return errors;
  }

  function clearDraftNow() {
    setPrompt("");
    setImages([]);
    setTextFiles([]);
    setAttachmentError("");
    textarea.current?.focus();
  }

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    void submit();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (
      event.key === "Enter" &&
      (composing.current ||
        event.nativeEvent.isComposing ||
        event.nativeEvent.keyCode === 229 ||
        suppressCommitEnter.current)
    ) {
      if (composing.current) sawCompositionEnter.current = true;
      if (!composing.current) suppressCommitEnter.current = false;
      event.preventDefault();
      return;
    }
    if (event.key !== "Enter") suppressCommitEnter.current = false;
    const action = resolveComposerAction({
      behavior: workingBehavior,
      shift: event.shiftKey,
      working,
    });
    if (event.key === "Enter" && action !== "newline") {
      event.preventDefault();
      void submit(action);
    }
  }

  const attachDisabled = busy || readingAttachments || working;

  function handleDragOver(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    if (!attachDisabled) setIsDragOver(true);
  }

  function handleDragLeave(event: DragEvent<HTMLDivElement>) {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
    setIsDragOver(false);
  }

  function handleDrop(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    setIsDragOver(false);
    if (attachDisabled) return;
    void addFiles(Array.from(event.dataTransfer.files));
  }

  function previewTextFile(file: StagedTextFile) {
    const kind = localFileKind(file.name, file.type);
    onPreviewFile?.({
      content: file.text,
      kind: kind === "code" || kind === "markdown" ? kind : "text",
      name: file.name,
      size: file.size,
      type: file.type || "text/plain",
    });
  }

  // One branch decides whether each pill reads the draft's settings or the
  // live chat's, and what a pick does: a draft updates its configuration,
  // a live chat changes its mode in place or forks for a model or effort.
  const update = (change: Partial<DraftChatConfiguration>) => {
    if (configuration && onConfigurationChange) {
      onConfigurationChange({ ...configuration, ...change });
    }
  };
  const draftControlsDisabled = busy || working;
  const mode: ComposerModeControl | undefined = configuration
    ? {
        disabled: draftControlsDisabled,
        onChange: (next) => update({ mode: next }),
        value: configuration.mode,
      }
    : liveMode && onLiveModeChange
      ? { disabled: liveControlsDisabled, onChange: onLiveModeChange, value: liveMode }
      : undefined;
  const liveEffort = (liveModel?.reasoningEffort ??
    "default") as DraftChatConfiguration["reasoningEffort"];
  // No inventory means no choice to offer: render nothing rather than a
  // permanently dead control.
  const model: ComposerModelControl | undefined =
    models.length === 0
      ? undefined
      : configuration
        ? {
            disabled: draftControlsDisabled,
            effort: configuration.reasoningEffort,
            groupedModels,
            model: configuration.model,
            onEffortChange: (effort) =>
              update({ reasoningEffort: effort as DraftChatConfiguration["reasoningEffort"] }),
            onModelChange: (next) => update({ model: next }),
            onReset: () => update({ model: undefined, reasoningEffort: "default" }),
          }
        : liveModel && onForkModel
          ? {
              disabled: liveControlsDisabled || !canForkModel,
              effort: liveEffort,
              groupedModels,
              model: liveModel,
              onEffortChange: (effort) =>
                onForkModel(liveModel, effort as DraftChatConfiguration["reasoningEffort"]),
              onModelChange: (next) => next && onForkModel(next, liveEffort),
            }
          : undefined;
  const tools: ComposerToolsControl | undefined = configuration
    ? {
        disabled: draftControlsDisabled,
        onChange: (toolAccess) => update({ toolAccess }),
        value: configuration.toolAccess,
      }
    : undefined;
  const showPillRow = Boolean(mode || model || tools || pillRowEnd);

  return (
    <form
      className="mx-auto w-full max-w-3xl shrink-0 px-4 pb-4 max-[499px]:px-0 max-[499px]:pb-[env(safe-area-inset-bottom)] min-[500px]:px-6 min-[500px]:pb-6"
      onSubmit={handleSubmit}
    >
      <div className="relative rounded-2xl bg-zinc-50 dark:bg-zinc-900 max-[499px]:rounded-b-none">
        {/* biome-ignore lint/a11y/noStaticElementInteractions: a drop target, not a widget; the Attach button covers the same action for keyboard and screen-reader users. */}
        <div
          className={cn(
            "relative rounded-2xl border bg-background transition-colors focus-within:border-zinc-400 dark:focus-within:border-zinc-600 max-[499px]:rounded-b-none",
            composerFrameClass({
              hasText: Boolean(prompt.trim()),
              isDragOver,
              isStreaming: working,
              mode: configuration?.mode,
            }),
          )}
          onDragLeave={handleDragLeave}
          onDragOver={handleDragOver}
          onDrop={handleDrop}
        >
          {voice.isListening && (
            <div className="flex items-center gap-2 px-4 pt-3 pb-1">
              <span aria-hidden="true" className="size-2 animate-pulse rounded-full bg-brand" />
              <span className="text-xs font-medium text-brand">Listening</span>
            </div>
          )}

          {isDragOver && (
            <div className="pointer-events-none absolute inset-0 z-20 flex items-center justify-center rounded-2xl bg-brand/10 dark:bg-brand/15">
              <div className="flex items-center gap-2 text-brand">
                <Paperclip aria-hidden="true" className="size-5" />
                <span className="text-sm font-medium">Drop files to attach</span>
              </div>
            </div>
          )}

          {(images.length > 0 || textFiles.length > 0) && (
            <ul aria-label="Attachments" className="flex flex-wrap gap-1.5 px-4 pt-3">
              {images.map((image) => (
                <AttachmentPill
                  key={image.id}
                  name={image.name}
                  onPreview={onPreviewImage ? () => onPreviewImage(image) : undefined}
                  onRemove={() =>
                    setImages((current) => current.filter((item) => item.id !== image.id))
                  }
                  previewUrl={imageSource(image)}
                  type={image.mimeType}
                />
              ))}
              {textFiles.map((file) => (
                <AttachmentPill
                  key={file.id}
                  name={file.name}
                  onPreview={onPreviewFile ? () => previewTextFile(file) : undefined}
                  onRemove={() =>
                    setTextFiles((current) => current.filter((item) => item.id !== file.id))
                  }
                  type={file.type}
                />
              ))}
            </ul>
          )}

          <div className="px-4 pt-4 pb-2">
            <Textarea
              aria-label="Message Mecatl"
              className="field-sizing-content max-h-48 min-h-[4.5rem] resize-none rounded-none border-0 bg-transparent px-0 py-0 text-sm shadow-none placeholder:opacity-60 focus-visible:ring-0 dark:bg-transparent max-[499px]:min-h-[1.75rem]"
              disabled={busy}
              onChange={(event) => setPrompt(event.target.value)}
              onCompositionEnd={() => {
                composing.current = false;
                suppressCommitEnter.current = !sawCompositionEnter.current;
                const sequence = compositionSequence.current;
                queueMicrotask(() => {
                  if (compositionSequence.current === sequence) suppressCommitEnter.current = false;
                });
              }}
              onCompositionStart={() => {
                composing.current = true;
                compositionSequence.current += 1;
                sawCompositionEnter.current = false;
                suppressCommitEnter.current = false;
              }}
              onKeyDown={handleKeyDown}
              placeholder={
                busy
                  ? "Mecatl is working…"
                  : working
                    ? workingBehavior === "steer"
                      ? "Steer the agent…"
                      : "Queue a message…"
                    : configuration
                      ? "Start a new chat…"
                      : "Send a message…"
              }
              ref={textarea}
              value={prompt}
            />
          </div>

          {attachmentError && (
            <p className="px-4 pb-2 text-xs text-destructive" role="alert">
              {attachmentError}
            </p>
          )}
          {voice.errorMessage && (
            <p className="px-4 pb-2 text-xs text-destructive" role="alert">
              {voice.errorMessage}
            </p>
          )}
          {voice.interimTranscript && (
            <p className="px-4 pb-2 text-xs text-muted-foreground" role="status">
              Hearing: {voice.interimTranscript}
            </p>
          )}

          <div className="flex items-center gap-1 px-2 pb-2">
            <input
              aria-label="Choose files to attach"
              className="sr-only"
              multiple
              onChange={(event) => {
                const files = Array.from(event.currentTarget.files ?? []);
                event.currentTarget.value = "";
                void addFiles(files);
              }}
              ref={fileInput}
              tabIndex={-1}
              type="file"
            />
            <ComposerOptionsSheet
              attachDisabled={attachDisabled}
              disabled={busy || working}
              footer={sheetFooter}
              mode={mode}
              model={model}
              onAddFile={() => fileInput.current?.click()}
              tools={tools}
            />
            <Button
              aria-label="Attach files"
              className="size-8 shrink-0 rounded-full text-muted-foreground hover:bg-muted/60"
              disabled={attachDisabled}
              onClick={() => fileInput.current?.click()}
              size="icon"
              title={
                working
                  ? "Wait for the active run to finish before attaching files"
                  : imageAttachmentsSupported
                    ? "Attach images or text files"
                    : "Attach text files"
              }
              type="button"
              variant="ghost"
            >
              <Plus aria-hidden="true" />
            </Button>
            <Button
              aria-label={voice.isListening ? "Stop dictation" : "Start dictation"}
              aria-pressed={voice.isListening}
              className={cn(
                "size-8 shrink-0 rounded-full border-0 shadow-none",
                voice.isListening
                  ? "bg-brand/10 text-brand hover:bg-brand/20"
                  : "bg-transparent text-muted-foreground hover:bg-muted/60",
              )}
              disabled={busy}
              onClick={voice.toggle}
              size="icon"
              title={voice.isSupported ? "Dictate a message" : "Check dictation availability"}
              type="button"
              variant="ghost"
            >
              <Mic aria-hidden="true" />
            </Button>
            <div className="ml-auto flex items-center gap-1">
              <ClearDraftButton
                disabled={busy}
                hasDraft={hasDraftContent}
                onClear={clearDraftNow}
              />
              <Button
                aria-label={
                  working
                    ? `${workingBehavior === "queue" ? "Queue" : "Steer"} message`
                    : busy
                      ? "Mecatl is working"
                      : "Send message"
                }
                className="size-8 shrink-0 rounded-full bg-brand text-brand-foreground hover:bg-brand/90"
                disabled={busy || (!prompt.trim() && images.length === 0 && textFiles.length === 0)}
                size="icon"
                type="submit"
              >
                {busy ? (
                  <LoaderCircle aria-hidden="true" className="animate-spin" />
                ) : (
                  <ArrowUp aria-hidden="true" />
                )}
              </Button>
            </div>
          </div>
        </div>

        {showPillRow && (
          <section
            aria-label="Chat configuration and usage"
            className="hide-scrollbar @container -mt-4 flex items-center gap-1 overflow-x-auto rounded-b-2xl border border-t-0 border-zinc-300 bg-zinc-50 px-2 pt-5 pb-1.5 dark:border-zinc-700 dark:bg-zinc-900 max-[499px]:hidden"
          >
            {mode && (
              <ComposerOptionMenu
                disabled={mode.disabled}
                items={MODE_OPTIONS}
                label="Mode"
                menuClassName="w-72"
                onSelect={mode.onChange}
                value={mode.value}
                valueLabel={modeTitle(mode.value)}
              />
            )}
            {tools && (
              <ComposerOptionMenu
                disabled={tools.disabled}
                items={TOOL_OPTIONS}
                label="Tools"
                onSelect={tools.onChange}
                value={tools.value}
                valueLabel={
                  TOOL_OPTIONS.find((option) => option.value === tools.value)?.title ?? "All"
                }
              />
            )}
            {model && (
              <ModelEffortMenu
                disabled={model.disabled}
                effort={model.effort}
                groupedModels={model.groupedModels}
                model={model.model}
                onEffortChange={model.onEffortChange}
                onModelChange={model.onModelChange}
                onReset={model.onReset}
              />
            )}
            {pillRowEnd}
          </section>
        )}
      </div>
      <Dialog
        onOpenChange={(open) => {
          if (!open) setSeedConfirmation(undefined);
        }}
        open={seedConfirmation !== undefined}
      >
        <DialogContent
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            textarea.current?.focus();
          }}
          showCloseButton={false}
        >
          <DialogHeader>
            <DialogTitle>Send this prompt?</DialogTitle>
            <DialogDescription>
              This link filled the composer. Review the text before starting a run.
            </DialogDescription>
          </DialogHeader>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Chat</dt>
            <dd className="min-w-0 break-words">{seedContext?.target ?? "New chat"}</dd>
            <dt className="text-muted-foreground">Model</dt>
            <dd className="min-w-0 break-words">{seedContext?.model ?? "Automatic"}</dd>
            <dt className="text-muted-foreground">Permission mode</dt>
            <dd className="min-w-0 break-words">{seedContext?.mode ?? "Manual"}</dd>
            {seedContext?.toolAccess && (
              <>
                <dt className="text-muted-foreground">Tool access</dt>
                <dd className="min-w-0 break-words">{seedContext.toolAccess}</dd>
              </>
            )}
          </dl>
          <p className="max-h-48 overflow-y-auto whitespace-pre-wrap break-words rounded-lg border bg-muted/30 p-3 text-sm">
            {seedConfirmation}
          </p>
          <DialogFooter>
            <Button onClick={() => setSeedConfirmation(undefined)} type="button" variant="outline">
              Edit prompt
            </Button>
            <Button
              disabled={busy || working || !seedCanConfirm || !prompt.trim()}
              onClick={() => void submit("send", true)}
              type="button"
            >
              Send prompt
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </form>
  );
}

/**
 * The composer's attachment chip, from the prototype's `AttachmentPill`
 * (`stack-08`): a thumbnail for an image or a file-kind icon otherwise, the
 * name, and a remove button.
 */
export function AttachmentPill({
  name,
  onPreview,
  onRemove,
  previewUrl,
  type,
}: {
  name: string;
  onPreview?: () => void;
  onRemove: () => void;
  previewUrl?: string;
  type?: string;
}) {
  const kind = fileKindMeta(name, type);
  const KindIcon = kind.icon;
  return (
    <li
      className={cn(
        "inline-flex h-7 max-w-full items-center gap-1.5 rounded-full border border-brand/30 bg-brand/5 pr-1.5 text-xs text-brand",
        previewUrl ? "pl-1" : "pl-2.5",
      )}
    >
      <button
        aria-label={`Preview ${name}`}
        className="inline-flex min-w-0 items-center gap-1.5 disabled:cursor-default"
        disabled={!onPreview}
        onClick={onPreview}
        type="button"
      >
        {previewUrl ? (
          <img alt="" className="size-5 shrink-0 rounded-full object-cover" src={previewUrl} />
        ) : (
          <KindIcon aria-hidden="true" className="size-3 shrink-0" />
        )}
        <Tooltip onlyWhenTruncated>
          <TooltipTrigger asChild>
            <span className="max-w-40 truncate">{name}</span>
          </TooltipTrigger>
          <TooltipContent className="max-w-[min(32rem,calc(100vw-2rem))] break-words">
            {name}
          </TooltipContent>
        </Tooltip>
      </button>
      <button
        aria-label={`Remove ${name}`}
        className="flex size-4 shrink-0 items-center justify-center rounded-full hover:bg-brand/10"
        onClick={onRemove}
        type="button"
      >
        <X aria-hidden="true" className="size-3" />
      </button>
    </li>
  );
}

export function resolveComposerAction({
  behavior,
  shift,
  working,
}: {
  behavior: EnterSendBehavior;
  shift: boolean;
  working: boolean;
}): ComposerEnterAction {
  if (!working) return shift ? "newline" : "send";
  if (!shift) return behavior;
  return behavior === "queue" ? "steer" : "queue";
}
