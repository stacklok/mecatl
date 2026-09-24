// SPDX-License-Identifier: Apache-2.0

import {
  Braces,
  FileText,
  ListTree,
  NotebookPen,
  PanelRightClose,
  Pencil,
  ScanEye,
  ShieldCheck,
} from "lucide-react";
import {
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  useEffect,
  useRef,
  useState,
} from "react";
import { Button } from "../../components/ui/button";
import { Textarea } from "../../components/ui/textarea";
import { maxPanelWidth, minPanelWidth, usePanelWidth } from "../../lib/panel-width";
import {
  type AuthorizationHandoff,
  type AuthorizationOperation,
  AuthorizationReview,
} from "./authorization-review";
import { HighlightedCode } from "./code-highlight";
import type { DelegationFocus } from "./delegation-card";
import type { DelegationFleet } from "./delegation-fleet";
import { SessionActivityContent } from "./delegation-panel";
import type { LocalFilePreview } from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import { SideThreadPanel } from "./side-thread-panel";
import type { ToolActivity } from "./tool-activity";

export type ContentPreview =
  | { authorization: AuthorizationHandoff; kind: "authorization" }
  | { file: LocalFilePreview; kind: "file" }
  | { kind: "tool"; tool: ToolActivity }
  | { kind: "canvas" }
  | { kind: "activity" }
  | { kind: "thread"; messageKey: string; parentSessionId: string; sessionId: string };

/** The read-only previews `GenericPreviewPanel` renders — every kind except the independently-driven "thread" panel. */
type StaticPreview = Exclude<ContentPreview, { kind: "thread" }>;

interface ActivityPreviewState {
  fallbackOpener?: HTMLButtonElement | null;
  fleet: DelegationFleet;
  focus?: DelegationFocus;
  focusRequest: number;
  onFocusChange: (focus?: DelegationFocus) => void;
  opener?: HTMLButtonElement | null;
  openerFocus?: DelegationFocus;
}

export function ContentPreviewPanel({
  activity,
  authorizationDisabled = false,
  authorizationUncertain = false,
  canvas,
  onAuthorizationOperation,
  onRefreshAuthorizationActivity,
  onCanvasChange,
  onClose,
  preview,
}: {
  activity?: ActivityPreviewState;
  authorizationDisabled?: boolean;
  authorizationUncertain?: boolean;
  canvas: string;
  onAuthorizationOperation?: (
    operation: AuthorizationOperation,
    authorization: AuthorizationHandoff,
  ) => Promise<void>;
  onRefreshAuthorizationActivity?: (authorization: AuthorizationHandoff) => void;
  onCanvasChange: (value: string) => void;
  onClose: () => void;
  preview: ContentPreview;
}) {
  // A thread has its own composer, model picker, and independent SSE run —
  // fully unlike the read-only file/tool/canvas previews below — so it owns
  // its own frame (resize, mobile backdrop, maximize) rather than sharing
  // the generic header/body shell `GenericPreviewPanel` renders. `key` forces
  // a clean remount when the user opens a *different* thread while one is
  // already showing. This dispatch must stay hook-free: `GenericPreviewPanel`
  // is a genuinely separate component so its `usePanelWidth()` is never
  // called conditionally on the same instance.
  if (preview.kind === "thread") {
    return (
      <SideThreadPanel
        key={preview.sessionId}
        messageKey={preview.messageKey}
        onClose={onClose}
        parentSessionId={preview.parentSessionId}
        sessionId={preview.sessionId}
      />
    );
  }
  return (
    <GenericPreviewPanel
      activity={activity}
      authorizationDisabled={authorizationDisabled}
      authorizationUncertain={authorizationUncertain}
      canvas={canvas}
      onAuthorizationOperation={onAuthorizationOperation}
      onRefreshAuthorizationActivity={onRefreshAuthorizationActivity}
      onCanvasChange={onCanvasChange}
      onClose={onClose}
      preview={preview}
    />
  );
}

function GenericPreviewPanel({
  activity,
  authorizationDisabled,
  authorizationUncertain,
  canvas,
  onAuthorizationOperation,
  onRefreshAuthorizationActivity,
  onCanvasChange,
  onClose,
  preview,
}: {
  activity?: ActivityPreviewState;
  authorizationDisabled: boolean;
  authorizationUncertain: boolean;
  canvas: string;
  onAuthorizationOperation?: (
    operation: AuthorizationOperation,
    authorization: AuthorizationHandoff,
  ) => Promise<void>;
  onRefreshAuthorizationActivity?: (authorization: AuthorizationHandoff) => void;
  onCanvasChange: (value: string) => void;
  onClose: () => void;
  preview: StaticPreview;
}) {
  const width = usePanelWidth("contentPreview");
  const title = useRef<HTMLHeadingElement>(null);
  const lastActivityFocusRequest = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (preview.kind !== "activity" || !activity) return;
    if (lastActivityFocusRequest.current === activity.focusRequest) return;
    lastActivityFocusRequest.current = activity.focusRequest;
    if (!activity?.focus) title.current?.focus();
  }, [preview.kind, activity]);

  function close() {
    onClose();
    if (preview.kind !== "activity") return;
    const replacement = activity?.openerFocus
      ? [...document.querySelectorAll<HTMLButtonElement>("button[data-delegation-focus]")].find(
          (button) => button.dataset.delegationFocus === JSON.stringify(activity.openerFocus),
        )
      : undefined;
    const target =
      (activity?.opener?.isConnected ? activity.opener : undefined) ??
      replacement ??
      activity?.fallbackOpener;
    target?.focus();
  }

  function startResize(event: ReactPointerEvent<HTMLButtonElement>) {
    event.currentTarget.focus();
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = width.value;
    const resize = (moveEvent: PointerEvent) =>
      width.setValue(startWidth - moveEvent.clientX + startX);
    const finish = () => {
      window.removeEventListener("pointermove", resize);
      window.removeEventListener("pointerup", finish);
    };
    window.addEventListener("pointermove", resize);
    window.addEventListener("pointerup", finish);
  }

  return (
    <>
      <button
        aria-label="Close preview"
        className="absolute inset-0 z-30 bg-black/35 min-[760px]:hidden"
        onClick={close}
        type="button"
      />
      <aside
        aria-label={previewTitle(preview)}
        className="absolute inset-x-0 bottom-0 z-40 flex h-[94dvh] flex-col rounded-t-2xl border bg-background shadow-2xl min-[760px]:relative min-[760px]:inset-auto min-[760px]:order-3 min-[760px]:h-full min-[760px]:w-[var(--content-panel-width)] min-[760px]:shrink-0 min-[760px]:rounded-none min-[760px]:border-y-0 min-[760px]:border-r-0"
        style={{ "--content-panel-width": `${width.value}px` } as CSSProperties}
        onKeyDown={(event) => {
          if (preview.kind === "activity" && event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            close();
          }
        }}
      >
        <button
          aria-label="Resize preview panel"
          className="absolute inset-y-0 -left-1 z-10 hidden w-2 cursor-col-resize touch-none border-0 bg-transparent p-0 hover:bg-brand/20 min-[760px]:block"
          onKeyDown={(event) => {
            if (event.key === "ArrowLeft") width.setValue(width.value + 12);
            else if (event.key === "ArrowRight") width.setValue(width.value - 12);
            else return;
            event.preventDefault();
          }}
          onPointerDown={startResize}
          title={`Resize preview (${minPanelWidth}–${maxPanelWidth}px)`}
          type="button"
        />
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
          {preview.kind === "authorization" ? (
            <ShieldCheck aria-hidden="true" className="size-4 text-brand-ink" />
          ) : preview.kind === "activity" ? (
            <ListTree aria-hidden="true" className="size-4 text-brand-ink" />
          ) : preview.kind === "canvas" ? (
            <NotebookPen aria-hidden="true" className="size-4 text-brand-ink" />
          ) : preview.kind === "tool" ? (
            <Braces aria-hidden="true" className="size-4 text-brand-ink" />
          ) : (
            <FileText aria-hidden="true" className="size-4 text-brand-ink" />
          )}
          <h2
            className="min-w-0 flex-1 truncate text-sm font-semibold"
            ref={title}
            tabIndex={preview.kind === "activity" ? -1 : undefined}
          >
            {previewTitle(preview)}
          </h2>
          <Button aria-label="Close preview" onClick={close} size="icon" variant="ghost">
            <PanelRightClose aria-hidden="true" />
          </Button>
        </header>
        <div className="min-h-0 flex-1 overflow-auto">
          {preview.kind === "authorization" ? (
            <AuthorizationReview
              key={`${preview.authorization.sessionId}\u0000${preview.authorization.authorizationId}\u0000${authorizationUncertain}`}
              authorization={preview.authorization}
              disabled={authorizationDisabled || !onAuthorizationOperation}
              uncertain={authorizationUncertain}
              onOperate={onAuthorizationOperation ?? (async () => undefined)}
              onRefreshActivity={onRefreshAuthorizationActivity}
            />
          ) : preview.kind === "activity" && activity ? (
            <SessionActivityContent
              key={activity.focusRequest}
              fleet={activity.fleet}
              focus={activity.focus}
              onFocusChange={activity.onFocusChange}
            />
          ) : preview.kind === "canvas" ? (
            <LocalCanvasEditor onChange={onCanvasChange} value={canvas} />
          ) : preview.kind === "tool" ? (
            <ToolResultPreview tool={preview.tool} />
          ) : preview.kind === "file" ? (
            <FilePreview file={preview.file} />
          ) : null}
        </div>
      </aside>
    </>
  );
}

function LocalCanvasEditor({
  onChange,
  value,
}: {
  onChange: (value: string) => void;
  value: string;
}) {
  const [previewing, setPreviewing] = useState(false);
  return (
    <div className="flex min-h-full flex-col p-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <p className="text-xs text-muted-foreground">Saved only in this browser.</p>
        <Button onClick={() => setPreviewing((current) => !current)} size="sm" variant="outline">
          {previewing ? <Pencil aria-hidden="true" /> : <ScanEye aria-hidden="true" />}
          {previewing ? "Edit" : "Preview"}
        </Button>
      </div>
      {previewing ? (
        <div className="prose min-h-64 flex-1 text-sm">
          {value ? <MarkdownMessage>{value}</MarkdownMessage> : <p>No canvas notes yet.</p>}
        </div>
      ) : (
        <Textarea
          aria-label="Local canvas"
          className="min-h-64 flex-1 resize-none font-mono text-xs"
          onChange={(event) => onChange(event.target.value)}
          placeholder="# Notes"
          value={value}
        />
      )}
    </div>
  );
}

function ToolResultPreview({ tool }: { tool: ToolActivity }) {
  return (
    <div className="space-y-5 p-4">
      <PreviewCode label="Input" value={formatStructured(tool.args || "{}")} />
      <PreviewCode
        error={tool.isError}
        label={tool.isError ? "Failed output" : "Output"}
        value={formatStructured(tool.output || "No output")}
      />
    </div>
  );
}

function PreviewCode({ error, label, value }: { error?: boolean; label: string; value: string }) {
  return (
    <section>
      <h3
        className={`mb-2 text-xs font-medium ${error ? "text-destructive" : "text-muted-foreground"}`}
      >
        {label}
      </h3>
      <pre className="overflow-auto whitespace-pre-wrap rounded-lg border bg-muted/30 p-3 leading-5">
        <HighlightedCode code={value} />
      </pre>
    </section>
  );
}

function FilePreview({ file }: { file: LocalFilePreview }) {
  return (
    <div className="flex min-h-full flex-col">
      <p className="border-b bg-muted/20 px-4 py-2 text-xs text-muted-foreground">
        {file.sent ? "Conversation image" : "Local preview"} ·{" "}
        {file.size ? formatBytes(file.size) : "remote source"}
        {!file.sent && " · not sent to Mecatl"}
      </p>
      <div className="min-h-0 flex-1 overflow-auto">
        <FilePreviewContent file={file} />
      </div>
    </div>
  );
}

function FilePreviewContent({ file }: { file: LocalFilePreview }) {
  if (file.kind === "image" && file.dataUrl) {
    return <img alt={file.name} className="max-w-full p-4" src={file.dataUrl} />;
  }
  if (file.kind === "pdf" && file.dataUrl) {
    return <iframe className="h-full w-full border-0" src={file.dataUrl} title={file.name} />;
  }
  if (file.kind === "markdown" && file.content !== undefined) {
    return (
      <div className="p-5 text-sm">
        <MarkdownMessage>{file.content}</MarkdownMessage>
      </div>
    );
  }
  if (file.kind === "code" && file.content !== undefined) {
    return (
      <pre className="overflow-auto whitespace-pre p-5 leading-5">
        <HighlightedCode code={file.content} />
      </pre>
    );
  }
  if (file.content !== undefined) {
    return (
      <pre className="whitespace-pre-wrap p-5 font-mono text-xs leading-5">{file.content}</pre>
    );
  }
  return (
    <div className="grid min-h-64 place-content-center p-6 text-center text-sm text-muted-foreground">
      <p>No browser preview is available for this file.</p>
    </div>
  );
}

function previewTitle(preview: StaticPreview) {
  if (preview.kind === "authorization") return "Authorization review";
  if (preview.kind === "activity") return "Session activity";
  if (preview.kind === "canvas") return "Local canvas";
  if (preview.kind === "tool") return `${preview.tool.name} result`;
  return preview.file.name;
}

function formatStructured(value: string) {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  return `${(value / 1024).toFixed(value < 10_240 ? 1 : 0)} KB`;
}
