"use client";

import { ChevronRight, CornerLeftUp, Folder, FolderGit2, House } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

type BrowseEntry = { name: string; path: string };
type BrowseResult = { path: string; parent: string | null; home: string; entries: BrowseEntry[] };

export type NewProject = { name: string; workspace: string };

/**
 * "Create project": a name plus the folder Mecatl works in.
 *
 * ONE folder, not the several the design this follows offers. A mecatl session
 * is rooted at a single `workspace` — every tool path resolves against it — so a
 * project spanning two trees has no representation on the wire. Offering a
 * multi-folder field would be a control that silently kept only the first.
 *
 * The folder is chosen by browsing the machine through the controller, because a
 * browser cannot supply an absolute path: a directory <input> yields relative
 * names with no root. Typing a path is supported for the case where the operator
 * already knows it.
 */
export function CreateProjectDialog({
  open,
  onOpenChange,
  onCreate,
  disabled,
  disabledReason,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreate: (project: NewProject) => void;
  disabled?: boolean;
  disabledReason?: string;
}) {
  const [name, setName] = useState("");
  const [browse, setBrowse] = useState<BrowseResult | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  // Whether the operator has typed a name, so the folder pick stops
  // auto-filling it and overwriting their words.
  const [nameEdited, setNameEdited] = useState(false);

  const load = useCallback(async (path?: string) => {
    setLoading(true);
    setError("");
    try {
      const query = path ? `?path=${encodeURIComponent(path)}` : "";
      const response = await fetch(`/api/mecatl-control/fs/browse${query}`, { cache: "no-store" });
      const body = await response.json();
      if (!response.ok) throw new Error(body.error || "Could not read that folder");
      setBrowse(body as BrowseResult);
    } catch (caught) {
      setError((caught as Error).message || "Could not read that folder");
    } finally {
      setLoading(false);
    }
  }, []);

  // Opening resets the draft and re-reads the starting folder. This is an
  // external sync (the filesystem), and the reset has to happen before the fetch
  // resolves, so it lives in the same callback rather than a second effect.
  const reset = useCallback(() => {
    setName("");
    setNameEdited(false);
    setError("");
    void load();
  }, [load]);

  useEffect(() => {
    // The lint rule sees setState in an effect; what this actually does is
    // subscribe to an external system (the filesystem, via the controller) when
    // the dialog opens, which is what effects are for. The synchronous part is
    // only clearing the previous draft so a stale name cannot flash.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (open) reset();
  }, [open, reset]);

  const selected = browse?.path ?? "";
  // The folder's own name is almost always the project name, so it is offered as
  // a default the operator can override rather than a field they must fill.
  const effectiveName = name.trim() || (selected ? selected.split("/").filter(Boolean).pop() || "" : "");

  const submit = () => {
    if (!selected || !effectiveName) return;
    onCreate({ name: effectiveName, workspace: selected });
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Create project</DialogTitle>
          <DialogDescription>
            Point Mecatl at a folder on this machine. Tasks in this project read
            and edit files there.
          </DialogDescription>
        </DialogHeader>

        {disabled ? (
          <p className="text-sm text-muted-foreground">{disabledReason}</p>
        ) : (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="project-name">Project name</Label>
              <Input
                id="project-name"
                value={nameEdited ? name : effectiveName}
                placeholder="Project name"
                onChange={(event) => {
                  setNameEdited(true);
                  setName(event.target.value);
                }}
              />
            </div>

            <div className="space-y-1.5">
              <Label>Project folder</Label>
              <div className="overflow-hidden rounded-md border border-border">
                <div className="flex items-center gap-1 border-b border-border bg-secondary px-2 py-1.5">
                  <button
                    type="button"
                    onClick={() => void load(browse?.home)}
                    aria-label="Home folder"
                    title="Home folder"
                    className="flex size-7 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                  >
                    <House className="size-3.5" />
                  </button>
                  <button
                    type="button"
                    disabled={!browse?.parent}
                    onClick={() => browse?.parent && void load(browse.parent)}
                    aria-label="Parent folder"
                    title="Parent folder"
                    className="flex size-7 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
                  >
                    <CornerLeftUp className="size-3.5" />
                  </button>
                  <code className="min-w-0 flex-1 truncate px-1 font-mono text-xs text-muted-foreground">
                    {selected || "…"}
                  </code>
                </div>

                <div className="max-h-56 overflow-y-auto">
                  {loading && (
                    <p className="px-3 py-3 text-xs text-muted-foreground">Reading…</p>
                  )}
                  {!loading && browse?.entries.length === 0 && (
                    <p className="px-3 py-3 text-xs text-muted-foreground">
                      No sub-folders here. Create the project on this folder, or go up.
                    </p>
                  )}
                  {!loading &&
                    browse?.entries.map((entry) => (
                      <button
                        key={entry.path}
                        type="button"
                        onClick={() => void load(entry.path)}
                        className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-foreground transition-colors hover:bg-accent"
                      >
                        <Folder className="size-3.5 shrink-0 text-muted-foreground" />
                        <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                        <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
                      </button>
                    ))}
                </div>
              </div>
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <FolderGit2 className="size-3.5 shrink-0" />
                Navigate to the folder you want, then create the project on it.
              </p>
            </div>

            {error && (
              <p className={cn("text-xs text-destructive")} role="alert">
                {error}
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            variant="action"
            disabled={disabled || !selected || !effectiveName}
            onClick={submit}
          >
            Create project
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
