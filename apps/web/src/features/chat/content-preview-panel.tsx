// SPDX-License-Identifier: Apache-2.0

import {
  Braces,
  FileText,
  ListTree,
  NotebookPen,
  Pencil,
  ScanEye,
  ShieldCheck,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import { Textarea } from "../../components/ui/textarea";
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
import { SidePanelShell } from "./side-panel-shell";
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
  // A thread owns its composer and SSE run, while the frame is shared.
  // A different thread gets a fresh content instance.
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
  const title = useRef<HTMLHeadingElement>(null);
  const lastActivityFocusRequest = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (preview.kind !== "activity" || !activity) return;
    if (lastActivityFocusRequest.current === activity.focusRequest) return;
    lastActivityFocusRequest.current = activity.focusRequest;
    if (!activity.focus) title.current?.focus();
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

  return (
    <SidePanelShell
      icon={
        preview.kind === "authorization" ? (
          <ShieldCheck aria-hidden="true" className="size-4 text-brand-ink" />
        ) : preview.kind === "activity" ? (
          <ListTree aria-hidden="true" className="size-4 text-brand-ink" />
        ) : preview.kind === "canvas" ? (
          <NotebookPen aria-hidden="true" className="size-4 text-brand-ink" />
        ) : preview.kind === "tool" ? (
          <Braces aria-hidden="true" className="size-4 text-brand-ink" />
        ) : (
          <FileText aria-hidden="true" className="size-4 text-brand-ink" />
        )
      }
      onClose={close}
      title={previewTitle(preview)}
      titleRef={title}
      titleTabIndex={preview.kind === "activity" ? -1 : undefined}
    >
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
    </SidePanelShell>
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
  if (file.kind === "image" && file.dataUrl?.startsWith("data:image/")) {
    return <img alt={file.name} className="max-w-full p-4" src={file.dataUrl} />;
  }
  if (file.kind === "image" && file.dataUrl && isWebUrl(file.dataUrl)) {
    return (
      <p className="p-5 text-sm">
        Remote images do not load in the preview.{" "}
        <a href={file.dataUrl} rel="noreferrer" target="_blank">
          Open remote image
        </a>
      </p>
    );
  }
  if (file.kind === "pdf" && file.dataUrl?.startsWith("data:application/pdf")) {
    return (
      <p className="p-5 text-sm">
        Inline PDF preview is unavailable under this site’s content policy.{" "}
        <a download={file.name} href={file.dataUrl}>
          Download PDF
        </a>
      </p>
    );
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
      <p>
        {file.reason === "oversized"
          ? "This file is larger than the 5 MB preview limit."
          : file.reason === "unsupported"
            ? "This file type is not supported for inline preview."
            : "No browser preview is available for this file."}
      </p>
    </div>
  );
}

function isWebUrl(value: string) {
  try {
    return ["https:", "http:"].includes(new URL(value).protocol);
  } catch {
    return false;
  }
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
