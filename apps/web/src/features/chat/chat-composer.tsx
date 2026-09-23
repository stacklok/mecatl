// SPDX-License-Identifier: Apache-2.0

import { ArrowUp, LoaderCircle, Mic, MicOff, Paperclip, SlidersHorizontal, X } from "lucide-react";
import {
  type FormEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Button } from "../../components/ui/button";
import { Textarea } from "../../components/ui/textarea";
import type { EnterSendBehavior } from "../../lib/profile-preferences";
import { ComposerOptionMenu } from "./composer-option-menu";
import {
  type ImageAttachment,
  imageSource,
  maxImageAttachmentCount,
  maxImagePromptBytes,
  readImageAttachment,
} from "./local-file-preview";
import { groupModels, ModelEffortMenu } from "./model-effort-menu";
import { useVoiceInput } from "./use-voice-input";

const MODE_OPTIONS = [
  {
    description: "Always ask before making changes.",
    title: "Manual",
    value: "default" as const,
  },
  {
    description: "Automatically accept all file edits.",
    title: "Accept edits",
    value: "acceptEdits" as const,
  },
  {
    description: "Create a plan before making changes.",
    title: "Plan",
    value: "plan" as const,
  },
];

const TOOL_OPTIONS = [
  {
    description: "The agent can use every tool, including file and shell access.",
    title: "All",
    value: "all" as const,
  },
  {
    description: "No file or shell tools; other tools stay available.",
    title: "No filesystem",
    value: "noFilesystem" as const,
  },
];

export interface ComposerModelOption {
  id: string;
  image: boolean;
  label: string;
  providerId: string;
}

export interface DraftChatConfiguration {
  mode: "acceptEdits" | "default" | "plan";
  model?: { id: string; providerId: string };
  reasoningEffort: "default" | "high" | "low" | "max" | "medium" | "xhigh";
  toolAccess: "all" | "noFilesystem";
}

interface ChatComposerProps {
  configuration?: DraftChatConfiguration;
  disabled?: boolean;
  imageAttachmentsSupported?: boolean;
  models?: ComposerModelOption[];
  onConfigurationChange?: (configuration: DraftChatConfiguration) => void;
  onPreviewImage?: (image: ImageAttachment) => void;
  onSeedConsumed?: () => void;
  onSend: (
    prompt: string,
    action: ComposerEnterAction,
    images: ImageAttachment[],
  ) => Promise<boolean>;
  safetyLevel?: string;
  seedText?: string;
  working?: boolean;
  workingBehavior?: EnterSendBehavior;
}

export type ComposerEnterAction = "send" | "queue" | "steer" | "newline";

export function ChatComposer({
  configuration,
  disabled = false,
  imageAttachmentsSupported = false,
  models = [],
  onConfigurationChange,
  onPreviewImage,
  onSeedConsumed,
  onSend,
  safetyLevel = "managed",
  seedText,
  working = false,
  workingBehavior = "queue",
}: ChatComposerProps) {
  const [prompt, setPrompt] = useState("");
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const [attachmentError, setAttachmentError] = useState("");
  const [readingAttachments, setReadingAttachments] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [mobileOptionsOpen, setMobileOptionsOpen] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const updatePrompt = useCallback((value: string) => setPrompt(value), []);
  const voice = useVoiceInput(prompt, updatePrompt);

  // A starter-prompt chip seeds the draft text without sending it — the
  // caller clears seedText (onSeedConsumed) once applied so re-clicking the
  // same chip still re-seeds.
  // biome-ignore lint/correctness/useExhaustiveDependencies: onSeedConsumed is stable per caller and re-running on it would re-seed after every consume
  useEffect(() => {
    if (seedText === undefined) return;
    setPrompt(seedText);
    onSeedConsumed?.();
    textarea.current?.focus();
  }, [seedText]);
  useEffect(() => {
    if (imageAttachmentsSupported || images.length === 0) return;
    setImages([]);
    setAttachmentError("Image attachments were removed because this model does not support them.");
  }, [imageAttachmentsSupported, images.length]);
  const groupedModels = useMemo(() => groupModels(models), [models]);
  const busy = disabled || submitting;

  async function submit(action: ComposerEnterAction = working ? workingBehavior : "send") {
    const nextPrompt = prompt.trim();
    if ((!nextPrompt && images.length === 0) || busy) return;
    if (working && images.length > 0) {
      setAttachmentError("Wait for the active run to finish before sending images.");
      return;
    }
    voice.stop();
    setSubmitting(true);
    try {
      if (await onSend(nextPrompt, action, images)) {
        setPrompt("");
        setImages([]);
        setAttachmentError("");
      }
    } finally {
      setSubmitting(false);
    }
  }

  async function addImages(files: File[]) {
    if (!imageAttachmentsSupported || files.length === 0) return;
    setReadingAttachments(true);
    setAttachmentError("");
    try {
      const remaining = maxImageAttachmentCount - images.length;
      if (remaining <= 0) {
        setAttachmentError(`A prompt can include up to ${maxImageAttachmentCount} images.`);
        return;
      }
      const selected = files.slice(0, remaining);
      const results = await Promise.allSettled(selected.map(readImageAttachment));
      const next = results.flatMap((result) =>
        result.status === "fulfilled" ? [result.value] : [],
      );
      const errors = results.flatMap((result) =>
        result.status === "rejected"
          ? [result.reason instanceof Error ? result.reason.message : "An image could not be read."]
          : [],
      );
      let totalBytes = images.reduce((sum, image) => sum + image.size, 0);
      const accepted = next.filter((image) => {
        if (totalBytes + image.size > maxImagePromptBytes) {
          errors.push(`${image.name} exceeds the 20 MB total image limit.`);
          return false;
        }
        totalBytes += image.size;
        return true;
      });
      setImages((current) => [...current, ...accepted]);
      if (files.length > selected.length) {
        errors.push(`A prompt can include up to ${maxImageAttachmentCount} images.`);
      }
      setAttachmentError(errors.join(" "));
    } finally {
      setReadingAttachments(false);
    }
  }

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    void submit();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
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

  const update = (change: Partial<DraftChatConfiguration>) => {
    if (configuration && onConfigurationChange) {
      onConfigurationChange({ ...configuration, ...change });
    }
  };

  return (
    <form className="mx-auto w-full max-w-3xl px-4 pb-4 sm:px-6 sm:pb-6" onSubmit={handleSubmit}>
      <div className="rounded-2xl border bg-card p-2 shadow-[0_8px_30px_rgb(0_0_0/0.06)] focus-within:ring-2 focus-within:ring-ring/40">
        <Textarea
          aria-label="Message Mecatl"
          className="max-h-48 min-h-16 resize-none border-0 bg-transparent px-2 py-2 shadow-none focus-visible:ring-0 dark:bg-transparent"
          disabled={busy}
          onChange={(event) => setPrompt(event.target.value)}
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

        {images.length > 0 && (
          <ul className="flex gap-2 overflow-x-auto px-2 pb-2">
            {images.map((image) => (
              <li
                className="relative flex w-32 shrink-0 items-center gap-2 rounded-lg border bg-muted/25 p-1.5 pr-7"
                key={image.id}
              >
                <button
                  aria-label={`Preview ${image.name}`}
                  className="shrink-0"
                  onClick={() => onPreviewImage?.(image)}
                  type="button"
                >
                  <img
                    alt=""
                    className="size-10 rounded-md object-cover"
                    src={imageSource(image)}
                  />
                </button>
                <span className="min-w-0 truncate text-xs" title={image.name}>
                  {image.name}
                </span>
                <button
                  aria-label={`Remove ${image.name}`}
                  className="absolute right-1 top-1 rounded-full p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
                  onClick={() =>
                    setImages((current) => current.filter((item) => item.id !== image.id))
                  }
                  type="button"
                >
                  <X aria-hidden="true" className="size-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}

        {attachmentError && (
          <p className="px-2 pb-2 text-xs text-destructive" role="alert">
            {attachmentError}
          </p>
        )}

        {configuration && (
          <div className="hidden flex-wrap items-center gap-1.5 border-t px-1 pt-2 sm:flex">
            <ComposerOptionMenu
              disabled={busy || working}
              items={MODE_OPTIONS}
              label="Mode"
              onSelect={(mode) => update({ mode })}
              value={configuration.mode}
              valueLabel={
                MODE_OPTIONS.find((option) => option.value === configuration.mode)?.title ??
                "Manual"
              }
            />

            <ComposerOptionMenu
              disabled={busy || working}
              items={TOOL_OPTIONS}
              label="Tools"
              onSelect={(toolAccess) => update({ toolAccess })}
              value={configuration.toolAccess}
              valueLabel={
                TOOL_OPTIONS.find((option) => option.value === configuration.toolAccess)?.title ??
                "All"
              }
            />

            <div
              aria-label={`Safety level: ${safetyLabel(safetyLevel)}. Managed by your organization.`}
              className="flex h-8 items-center gap-1.5 rounded-full border bg-muted/30 px-3 text-xs"
              role="status"
              title="Safety level is managed by your organization"
            >
              <span className="font-medium">Safety</span>
              <span className="text-muted-foreground">{safetyLabel(safetyLevel)}</span>
            </div>

            {/* No inventory means no choice to offer: render nothing rather
                than a permanently dead control. The deployment's model
                inventory arrives with the settings surface. */}
            {models.length > 0 ? (
              <ModelEffortMenu
                disabled={busy || working}
                effort={configuration.reasoningEffort}
                groupedModels={groupedModels}
                model={configuration.model}
                onEffortChange={(reasoningEffort) =>
                  update({
                    reasoningEffort: reasoningEffort as DraftChatConfiguration["reasoningEffort"],
                  })
                }
                onModelChange={(model) => update({ model })}
                onReset={() => update({ model: undefined, reasoningEffort: "default" })}
              />
            ) : null}
          </div>
        )}

        <div className="flex items-center justify-between gap-3 px-1 pb-1 pt-2">
          <div className="flex min-w-0 items-center gap-2">
            {onPreviewImage && (
              <>
                <input
                  accept="image/*"
                  aria-label="Choose images to attach"
                  className="sr-only"
                  onChange={(event) => {
                    const files = Array.from(event.currentTarget.files ?? []);
                    event.currentTarget.value = "";
                    void addImages(files);
                  }}
                  multiple
                  ref={fileInput}
                  type="file"
                />
                <Button
                  aria-label="Attach images"
                  className="size-8 shrink-0 rounded-full"
                  disabled={busy || readingAttachments || working || !imageAttachmentsSupported}
                  onClick={() => fileInput.current?.click()}
                  size="icon"
                  title={
                    imageAttachmentsSupported
                      ? working
                        ? "Wait for the active run to finish before attaching images"
                        : "Attach images"
                      : "The selected model does not support image attachments"
                  }
                  type="button"
                  variant="ghost"
                >
                  <Paperclip aria-hidden="true" />
                </Button>
              </>
            )}
            {configuration && (
              <Button
                aria-label="Chat options"
                className="size-8 shrink-0 rounded-full sm:hidden"
                disabled={busy || working}
                onClick={() => setMobileOptionsOpen(true)}
                size="icon"
                type="button"
                variant="ghost"
              >
                <SlidersHorizontal aria-hidden="true" />
              </Button>
            )}
            {voice.isSupported && (
              <Button
                aria-label={voice.isListening ? "Stop dictation" : "Start dictation"}
                aria-pressed={voice.isListening}
                className="size-8 shrink-0 rounded-full"
                disabled={busy}
                onClick={voice.toggle}
                size="icon"
                type="button"
                variant={voice.isListening ? "secondary" : "ghost"}
              >
                {voice.isListening ? <MicOff aria-hidden="true" /> : <Mic aria-hidden="true" />}
              </Button>
            )}
            <p className="truncate text-xs text-muted-foreground">
              {working
                ? `Enter to ${workingBehavior} · Shift + Enter does the opposite`
                : "Enter to send · Shift + Enter for a line break"}
            </p>
          </div>
          <Button
            aria-label={
              working
                ? `${workingBehavior === "queue" ? "Queue" : "Steer"} message`
                : busy
                  ? "Mecatl is working"
                  : "Send message"
            }
            className="size-8 shrink-0 rounded-full"
            disabled={busy || (!prompt.trim() && images.length === 0)}
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
      {configuration && mobileOptionsOpen && (
        <MobileConfigurationSheet
          configuration={configuration}
          models={models}
          onChange={update}
          onClose={() => setMobileOptionsOpen(false)}
        />
      )}
    </form>
  );
}

function MobileConfigurationSheet({
  configuration,
  models,
  onChange,
  onClose,
}: {
  configuration: DraftChatConfiguration;
  models: ComposerModelOption[];
  onChange: (change: Partial<DraftChatConfiguration>) => void;
  onClose: () => void;
}) {
  const groupedModels = groupModels(models);
  return (
    <div className="fixed inset-0 z-50 flex items-end bg-black/45 sm:hidden">
      <button
        aria-label="Close chat options"
        className="absolute inset-0"
        onClick={onClose}
        type="button"
      />
      <section
        aria-labelledby="mobile-chat-options-title"
        aria-modal="true"
        className="relative z-10 w-full rounded-t-2xl border bg-background p-5 pb-[max(1.25rem,env(safe-area-inset-bottom))] shadow-2xl"
        onKeyDown={(event) => {
          if (event.key === "Escape") onClose();
        }}
        role="dialog"
      >
        <div className="flex items-center justify-between gap-3">
          <h2 className="font-semibold" id="mobile-chat-options-title">
            Chat options
          </h2>
          <Button
            aria-label="Close chat options"
            onClick={onClose}
            size="icon"
            type="button"
            variant="ghost"
          >
            <X aria-hidden="true" />
          </Button>
        </div>
        <div className="mt-4 grid gap-4">
          <MobileSelect
            label="Mode"
            onChange={(value) => onChange({ mode: value as DraftChatConfiguration["mode"] })}
            value={configuration.mode}
          >
            <option value="default">Manual</option>
            <option value="plan">Plan</option>
            <option value="acceptEdits">Accept edits</option>
          </MobileSelect>
          {/* Model and effort belong to the deployment's model inventory. With
              no inventory there is nothing to choose, so this sheet omits both
              rather than showing a dead Model row beside a live Effort row that
              applies to a model the user cannot pick. Same rule as the desktop
              control above. */}
          {models.length > 0 ? (
            <>
              <MobileSelect
                label="Model"
                onChange={(value) => {
                  const selected = models.find((model) => modelKey(model) === value);
                  onChange({
                    model: selected
                      ? { id: selected.id, providerId: selected.providerId }
                      : undefined,
                  });
                }}
                value={configuration.model ? modelKey(configuration.model) : ""}
              >
                <option value="">Default</option>
                {[...groupedModels.entries()].map(([providerId, providerModels]) => (
                  <optgroup key={providerId} label={humanize(providerId)}>
                    {providerModels.map((model) => (
                      <option key={modelKey(model)} value={modelKey(model)}>
                        {model.label}
                      </option>
                    ))}
                  </optgroup>
                ))}
              </MobileSelect>
              <MobileSelect
                label="Effort"
                onChange={(value) =>
                  onChange({ reasoningEffort: value as DraftChatConfiguration["reasoningEffort"] })
                }
                value={configuration.reasoningEffort}
              >
                <option value="default">Auto</option>
                <option value="low">Low</option>
                <option value="medium">Medium</option>
                <option value="high">High</option>
                <option value="xhigh">Extra high</option>
                <option value="max">Max</option>
              </MobileSelect>
            </>
          ) : null}
          <MobileSelect
            label="Tools"
            onChange={(value) =>
              onChange({ toolAccess: value as DraftChatConfiguration["toolAccess"] })
            }
            value={configuration.toolAccess}
          >
            <option value="all">All</option>
            <option value="noFilesystem">No filesystem</option>
          </MobileSelect>
        </div>
      </section>
    </div>
  );
}

function MobileSelect({
  children,
  disabled,
  label,
  onChange,
  value,
}: {
  children: React.ReactNode;
  disabled?: boolean;
  label: string;
  onChange: (value: string) => void;
  value: string;
}) {
  return (
    <label className="grid gap-1.5 text-sm">
      <span className="font-medium">{label}</span>
      <select
        className="h-11 rounded-lg border bg-background px-3"
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        value={value}
      >
        {children}
      </select>
    </label>
  );
}

function modelKey(model: { id: string; providerId: string }): string {
  return JSON.stringify([model.providerId, model.id]);
}

function humanize(value: string): string {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function safetyLabel(value: string): string {
  if (!value) return "Managed";
  return value.charAt(0).toUpperCase() + value.slice(1).toLowerCase();
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
