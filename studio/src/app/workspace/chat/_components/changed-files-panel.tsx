"use client";

import { Copy, FilePen } from "lucide-react";
import { Button } from "@/components/ui/button";
import { copyToClipboard } from "@/lib/clipboard";
import type { ToolCallFile } from "@/lib/file-meta";
import type { ChangedFile } from "@/lib/tool-summary";
import { SidePanel } from "./side-panel";

/** "written · edited ×2" — what happened to one path, in order. */
export function describeFileChanges(file: ChangedFile): string {
  const parts: string[] = [];
  if (file.writes > 0) {
    parts.push(file.writes > 1 ? `written ×${file.writes}` : "written");
  }
  if (file.edits > 0) {
    parts.push(file.edits > 1 ? `edited ×${file.edits}` : "edited");
  }
  return parts.join(" · ");
}

/** "3 files changed in this conversation" (singular-aware). */
export function changedFilesHeading(count: number): string {
  return `${count} file${count === 1 ? "" : "s"} changed in this conversation`;
}

/**
 * The conversation-wide changed-files list (the TUI's "N files changed this
 * session" appendix): every path an Edit or Write touched, in the order it
 * was first touched. A path with a Write opens that write's content in the
 * file preview; every path offers Copy path (an Edit carries no final
 * content to preview, so the path is what there is to act on).
 */
export function ChangedFilesPanel({
  files,
  onOpenFile,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  files: ChangedFile[];
  /** Opens a Write's content in the file preview panel. */
  onOpenFile?: (file: ToolCallFile) => void;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  return (
    <SidePanel
      icon={FilePen}
      title="Changed files"
      closeLabel="Close changed files"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      windowControls={windowControls}
    >
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        <p className="mb-2 text-xs text-muted-foreground">
          {files.length === 0
            ? "No files changed in this conversation yet."
            : `${changedFilesHeading(files.length)}, in the order they were first touched.`}
        </p>
        {files.length > 0 && (
          <ul className="divide-y divide-border/60">
            {files.map((file) => {
              const lastWrite = file.lastWrite;
              return (
                <li
                  key={file.path}
                  className="flex items-center gap-2 py-1.5 text-xs"
                >
                  <span
                    className="min-w-0 flex-1 truncate font-mono text-foreground"
                    title={file.path}
                  >
                    {file.path}
                  </span>
                  <span className="shrink-0 text-[11px] text-muted-foreground">
                    {describeFileChanges(file)}
                  </span>
                  {lastWrite && onOpenFile && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      aria-label={`Open ${file.path}`}
                      onClick={() => onOpenFile(lastWrite)}
                      className="h-6 px-2 text-xs"
                    >
                      Open
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={`Copy path ${file.path}`}
                    title="Copy path"
                    onClick={() => void copyToClipboard(file.path, "Path")}
                    className="size-6 text-muted-foreground hover:text-foreground"
                  >
                    <Copy className="size-3.5" />
                  </Button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </SidePanel>
  );
}
