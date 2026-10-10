// SPDX-License-Identifier: Apache-2.0

import {
  Braces,
  Copy,
  Eraser,
  FileText,
  ListTree,
  NotebookPen,
  Pencil,
  ScanEye,
  ShieldAlert,
  ShieldCheck,
} from "lucide-react";
import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "../../components/ui/button";
import { Textarea } from "../../components/ui/textarea";
import { Toggle } from "../../components/ui/toggle";
import { copyToClipboard } from "../../lib/clipboard";
import { cn } from "../../lib/utils";
import { type ApprovalDetail, ApprovalDetailPanel } from "./approval-detail-panel";
import {
  type AuthorizationHandoff,
  type AuthorizationOperation,
  AuthorizationReview,
} from "./authorization-review";
import { CodeBlock } from "./code-block";
import { HighlightedCode, langForClassName } from "./code-highlight";
import type { DelegationFocus } from "./delegation-card";
import type { DelegationFleet } from "./delegation-fleet";
import { SessionActivityContent } from "./delegation-panel";
import { parseDiffArgs, ToolDiff } from "./edit-diff";
import type { LocalFilePreview } from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import { SidePanelShell } from "./side-panel-shell";
import { SideThreadPanel } from "./side-thread-panel";
import type { ToolActivity } from "./tool-activity";
import { statusDotClass, toolStatus } from "./tool-call-list";
import { friendlyToolName } from "./tool-summary";

export type ContentPreview =
  | { askId: string; kind: "approval"; runId: string }
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
  /** The family the panel opens on when it has no focus. */
  family?: DelegationFocus["family"];
  fleet: DelegationFleet;
  focus?: DelegationFocus;
  focusRequest: number;
  onFocusChange: (focus?: DelegationFocus) => void;
  opener?: HTMLButtonElement | null;
  openerFocus?: DelegationFocus;
}

/** Find the live card again if a transcript refresh replaced the original opener. */
export function restoreActivityOpenerFocus({
  fallbackOpener,
  opener,
  openerFocus,
}: Pick<ActivityPreviewState, "fallbackOpener" | "opener" | "openerFocus">) {
  const replacement = openerFocus
    ? [...document.querySelectorAll<HTMLButtonElement>("button[data-delegation-focus]")].find(
        (button) => button.dataset.delegationFocus === JSON.stringify(openerFocus),
      )
    : undefined;
  const target = (opener?.isConnected ? opener : undefined) ?? replacement ?? fallbackOpener;
  target?.focus();
}

export function ContentPreviewPanel({
  activity,
  approval,
  authorizationDisabled = false,
  authorizationUncertain = false,
  canvas,
  escapeManagedExternally = false,
  escapeHint = false,
  onAuthorizationOperation,
  onRefreshAuthorizationActivity,
  onCanvasChange,
  onClose,
  preview,
}: {
  activity?: ActivityPreviewState;
  /** The live ask an "approval" preview shows, re-resolved by the surface each render. */
  approval?: ApprovalDetail;
  authorizationDisabled?: boolean;
  authorizationUncertain?: boolean;
  canvas: string;
  /** The owning chat surface applies ask and run priority before closing this panel. */
  escapeManagedExternally?: boolean;
  escapeHint?: boolean;
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
      approval={approval}
      authorizationDisabled={authorizationDisabled}
      authorizationUncertain={authorizationUncertain}
      canvas={canvas}
      escapeManagedExternally={escapeManagedExternally}
      escapeHint={escapeHint}
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
  approval,
  authorizationDisabled,
  authorizationUncertain,
  canvas,
  escapeManagedExternally,
  escapeHint,
  onAuthorizationOperation,
  onRefreshAuthorizationActivity,
  onCanvasChange,
  onClose,
  preview,
}: {
  activity?: ActivityPreviewState;
  approval?: ApprovalDetail;
  authorizationDisabled: boolean;
  authorizationUncertain: boolean;
  canvas: string;
  escapeManagedExternally: boolean;
  escapeHint: boolean;
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
    if (preview.kind === "activity" && activity) restoreActivityOpenerFocus(activity);
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    if (event.key !== "Escape" || escapeManagedExternally || event.defaultPrevented) return;
    if (
      document.querySelector(
        '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [role="menu"][data-state="open"]',
      )
    )
      return;
    event.preventDefault();
    event.stopPropagation();
    close();
  }

  if (preview.kind === "approval" && !approval) return null;

  return (
    <SidePanelShell
      actions={
        preview.kind === "approval" && approval && approval.total > 1 ? (
          <Badge className="tabular-nums" variant="outline">
            {approval.position} of {approval.total}
          </Badge>
        ) : undefined
      }
      autoFocusClose={preview.kind !== "activity"}
      icon={
        preview.kind === "approval" ? (
          <ShieldAlert aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        ) : preview.kind === "authorization" ? (
          <ShieldCheck aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        ) : preview.kind === "activity" ? (
          <ListTree aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        ) : preview.kind === "canvas" ? (
          <NotebookPen aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        ) : preview.kind === "tool" ? (
          <Braces aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        ) : (
          <FileText aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        )
      }
      escapeHint={escapeHint}
      onClose={close}
      onKeyDown={handleKeyDown}
      restoreFocusOnClose={preview.kind !== "activity"}
      surface={preview.kind === "approval" ? "approval" : undefined}
      title={
        preview.kind === "approval"
          ? approval?.approval.tool || "Permission required"
          : previewTitle(preview)
      }
      titleRef={title}
      titleTabIndex={preview.kind === "activity" ? -1 : undefined}
    >
      {preview.kind === "approval" && approval ? (
        <ApprovalDetailPanel
          key={`${preview.runId}\u0000${preview.askId}`}
          approval={approval.approval}
          disabled={approval.disabled}
          onRespond={approval.onRespond}
          uncertain={approval.uncertain}
        />
      ) : preview.kind === "authorization" ? (
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
          family={activity.family}
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

/**
 * The local canvas, in the prototype's `local-canvas-panel.tsx` layout: a
 * toolbar with the view switch, Copy, and Clear above the editor, and the
 * browser-only note under it. Studio keeps its Edit and Preview words for the
 * two views (the prototype's Raw and Styled).
 */
function LocalCanvasEditor({
  onChange,
  value,
}: {
  onChange: (value: string) => void;
  value: string;
}) {
  const [previewing, setPreviewing] = useState(false);
  const segment =
    "h-7 min-w-0 rounded-full px-3 text-xs text-muted-foreground hover:bg-transparent hover:text-foreground data-[state=on]:bg-background data-[state=on]:text-foreground data-[state=on]:shadow-sm";
  return (
    <div className="flex min-h-full flex-col">
      <div className="sticky top-0 z-10 flex items-center justify-between gap-2 border-b bg-background px-3 py-1.5">
        {/* biome-ignore lint/a11y/useSemanticElements: a group of two toggle buttons, not a form fieldset */}
        <div
          aria-label="Canvas view"
          className="flex gap-0.5 rounded-full bg-muted p-1"
          role="group"
        >
          <Toggle
            className={segment}
            onPressedChange={(pressed) => pressed && setPreviewing(true)}
            pressed={previewing}
            size="sm"
          >
            <ScanEye aria-hidden="true" />
            Preview
          </Toggle>
          <Toggle
            className={segment}
            onPressedChange={(pressed) => pressed && setPreviewing(false)}
            pressed={!previewing}
            size="sm"
          >
            <Pencil aria-hidden="true" />
            Edit
          </Toggle>
        </div>
        <div className="flex items-center gap-1">
          <Button
            aria-label="Copy canvas"
            className="size-7 text-muted-foreground"
            disabled={!value}
            onClick={() => void copyToClipboard(value, "Canvas")}
            size="icon"
            type="button"
            variant="ghost"
          >
            <Copy aria-hidden="true" className="size-4" />
          </Button>
          <Button
            aria-label="Clear canvas"
            className="size-7 text-muted-foreground"
            disabled={!value}
            onClick={() => onChange("")}
            size="icon"
            type="button"
            variant="ghost"
          >
            <Eraser aria-hidden="true" className="size-4" />
          </Button>
        </div>
      </div>
      <div className="flex flex-1 flex-col p-4">
        {previewing ? (
          <div className="min-h-64 flex-1 text-sm leading-relaxed">
            {value ? (
              <MarkdownMessage>{value}</MarkdownMessage>
            ) : (
              <p className="text-muted-foreground" role="status">
                No canvas notes yet.
              </p>
            )}
          </div>
        ) : (
          <>
            <Textarea
              aria-label="Local canvas"
              className="min-h-64 flex-1 resize-none font-mono text-xs"
              onChange={(event) => onChange(event.target.value)}
              placeholder="# Notes"
              value={value}
            />
            <p className="mt-2 text-xs text-muted-foreground">Saved only in this browser.</p>
          </>
        )}
      </div>
    </div>
  );
}

/**
 * One call's full detail, ported from the prototype's tool panel: its status,
 * the input (an Edit or Write as its diff, with Raw back to the formatted
 * JSON; anything else as that JSON), and the whole output.
 */
function ToolResultPreview({ tool }: { tool: ToolActivity }) {
  const [showRaw, setShowRaw] = useState(false);
  const status = toolStatus(tool);
  const head = friendlyToolName(tool.name);
  const diffable = parseDiffArgs(tool.name, tool.args) !== null;
  const showDiff = diffable && !showRaw;
  return (
    <div className="px-4 py-3">
      <p className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
        <span aria-hidden="true" className={cn("size-1.5 rounded-full", statusDotClass(status))} />
        {status}
        {head.mcp && (
          <span className="truncate font-mono" title="Exact tool name">
            · {head.raw}
          </span>
        )}
      </p>
      <div className="flex items-end justify-between gap-2">
        <PreviewLabel>{showDiff ? "Input — diff" : "Input"}</PreviewLabel>
        {diffable && (
          <Button
            aria-pressed={showRaw}
            className="mb-1 h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
            onClick={() => setShowRaw((value) => !value)}
            size="sm"
            type="button"
            variant="ghost"
          >
            {showRaw ? "Diff" : "Raw"}
          </Button>
        )}
      </div>
      {showDiff ? (
        <ToolDiff name={tool.name} rawArgs={tool.args} />
      ) : (
        <PreviewCode value={formatStructured(tool.args || "{}")} />
      )}
      <PreviewLabel error={tool.isError}>{tool.isError ? "Failed output" : "Output"}</PreviewLabel>
      <PreviewCode
        error={tool.isError}
        value={
          tool.output === undefined
            ? "(still running)"
            : formatStructured(tool.output || "No output")
        }
      />
    </div>
  );
}

function PreviewLabel({ children, error }: { children: string; error?: boolean }) {
  return (
    <h3
      className={cn(
        "mt-4 mb-1.5 text-[11px] font-medium uppercase tracking-wide",
        error ? "text-destructive" : "text-muted-foreground",
      )}
    >
      {children}
    </h3>
  );
}

function PreviewCode({ error, value }: { error?: boolean; value: string }) {
  return (
    <pre
      className={cn(
        "overflow-auto whitespace-pre-wrap break-words rounded-lg border p-3 text-xs leading-5",
        error
          ? "border-destructive/40 bg-destructive/5 text-destructive/90"
          : "border-border bg-muted/30 text-foreground/80",
      )}
    >
      <HighlightedCode code={value} />
    </pre>
  );
}

function FilePreview({ file }: { file: LocalFilePreview }) {
  return (
    <div className="flex min-h-full flex-col">
      <p className="sticky top-0 z-10 border-b bg-background/95 px-4 py-1.5 text-xs text-muted-foreground backdrop-blur lg:px-6">
        {file.sent ? "Conversation image" : "Local preview"} ·{" "}
        {file.size ? formatBytes(file.size) : "remote source"}
        {!file.sent && " · not sent to Mecatl"}
      </p>
      <div className="min-h-0 flex-1">
        <FilePreviewContent file={file} />
      </div>
    </div>
  );
}

function FilePreviewContent({ file }: { file: LocalFilePreview }) {
  if (file.kind === "image" && file.dataUrl?.startsWith("data:image/")) {
    return (
      <div className="p-4 lg:p-6">
        <img alt={file.name} className="max-w-full rounded-lg" src={file.dataUrl} />
      </div>
    );
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
      <div className="px-4 py-4 text-sm leading-relaxed lg:px-6 lg:py-6 lg:text-[15px]">
        <MarkdownMessage>{file.content}</MarkdownMessage>
      </div>
    );
  }
  if (file.kind === "code" && file.content !== undefined) {
    return (
      <CodeBlock
        code={file.content}
        lang={langForClassName(`language-${file.name.split(".").at(-1) ?? ""}`)}
      />
    );
  }
  if (file.content !== undefined) {
    return (
      <pre className="whitespace-pre-wrap p-4 font-mono text-xs leading-relaxed lg:p-6">
        {file.content}
      </pre>
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

function previewTitle(preview: Exclude<StaticPreview, { kind: "approval" }>) {
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
