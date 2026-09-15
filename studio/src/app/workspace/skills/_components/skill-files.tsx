"use client";

import { FileText } from "lucide-react";
import { useEffect, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import type { HarnessSkillFile } from "@/lib/harness/client";

/** "12345" → "12.1 KB" — coarse on purpose, it labels a list row. */
function formatSize(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * The bundled files of one skill — a skill can be a whole folder (SKILL.md
 * plus scripts/, references/, assets/…), not just a single markdown file.
 * A lone SKILL.md skips the list and renders its contents inline; a folder
 * skill lists its files, each row opening a bounded text preview. Read-only
 * either way; binary or oversized files render the controller's refusal
 * verbatim.
 */
export function SkillFiles({
  name,
  fetchFiles,
  fetchFile,
}: {
  name: string;
  fetchFiles: (
    name: string,
    signal?: AbortSignal,
  ) => Promise<HarnessSkillFile[]>;
  fetchFile: (name: string, path: string) => Promise<string>;
}) {
  const [files, setFiles] = useState<HarnessSkillFile[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // The lone-SKILL.md inline rendering: null until that shape is confirmed.
  const [inline, setInline] = useState<string | null>(null);
  const [preview, setPreview] = useState<{
    path: string;
    content: string | null;
    error: string | null;
  } | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    setInline(null);
    fetchFiles(name, controller.signal)
      .then(async (list) => {
        if (controller.signal.aborted) return;
        setFiles(list);
        // A single-file skill IS its SKILL.md — show the contents, not a
        // one-row list. Fetch failures fall back to the list rendering.
        if (list.length === 1 && list[0].path === "SKILL.md") {
          const content = await fetchFile(name, "SKILL.md").catch(() => null);
          if (!controller.signal.aborted && content !== null)
            setInline(content);
        }
      })
      .catch((caught) => {
        if (controller.signal.aborted) return;
        setFiles([]);
        setError(caught instanceof Error ? caught.message : String(caught));
      });
    return () => controller.abort();
  }, [name, fetchFiles, fetchFile]);

  function openPreview(path: string) {
    setPreview({ path, content: null, error: null });
    fetchFile(name, path)
      .then((content) =>
        setPreview((current) =>
          current?.path === path ? { path, content, error: null } : current,
        ),
      )
      .catch((caught) =>
        setPreview((current) =>
          current?.path === path
            ? {
                path,
                content: null,
                error:
                  caught instanceof Error ? caught.message : String(caught),
              }
            : current,
        ),
      );
  }

  if (files === null) {
    return <Skeleton className="h-24 rounded-lg" />;
  }

  if (error) {
    return (
      <div className="rounded-lg border border-dashed border-destructive/40 px-4 py-6 text-center text-sm text-destructive">
        {error}
      </div>
    );
  }

  if (files.length === 0) {
    return (
      <div className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
        The skill folder is empty.
      </div>
    );
  }

  if (inline !== null) {
    return (
      <pre className="overflow-x-auto rounded-lg border bg-background p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap">
        {inline}
      </pre>
    );
  }

  return (
    <>
      <ul className="divide-y overflow-hidden rounded-lg border bg-background">
        {files.map((file) => (
          <li key={file.path}>
            <button
              type="button"
              className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-muted/50"
              onClick={() => openPreview(file.path)}
            >
              <FileText
                aria-hidden="true"
                className="size-4 shrink-0 text-muted-foreground"
              />
              <span className="min-w-0 flex-1 truncate font-mono text-sm">
                {file.path}
              </span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {formatSize(file.size)}
              </span>
            </button>
          </li>
        ))}
      </ul>

      <Dialog
        open={preview !== null}
        onOpenChange={(open) => !open && setPreview(null)}
      >
        {preview && (
          <DialogContent className="flex max-h-[85vh] flex-col max-[499px]:top-0 max-[499px]:left-0 max-[499px]:h-dvh max-[499px]:max-h-none max-[499px]:w-screen max-[499px]:max-w-none max-[499px]:translate-x-0 max-[499px]:translate-y-0 max-[499px]:rounded-none max-[499px]:border-0 sm:max-w-3xl">
            <DialogHeader className="text-left">
              <DialogTitle className="break-all font-mono text-base">
                {preview.path}
              </DialogTitle>
            </DialogHeader>
            {preview.error ? (
              <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
                {preview.error}
              </p>
            ) : preview.content === null ? (
              <Skeleton className="h-40 rounded-lg" />
            ) : (
              <pre className="min-h-0 flex-1 overflow-auto rounded-lg border bg-muted/40 p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap">
                {preview.content}
              </pre>
            )}
          </DialogContent>
        )}
      </Dialog>
    </>
  );
}
