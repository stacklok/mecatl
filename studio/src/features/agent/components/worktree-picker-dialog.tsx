"use client";

import { Loader2, RefreshCw } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { HarnessApiError } from "@/lib/harness/errors";
import { isUnsupportedByDaemon } from "@/lib/harness/sdk";
import {
  fetchHarnessSessionIdentity,
  ThreadSourceBusyError,
} from "@/lib/harness/sessions";
import {
  fetchHarnessWorktrees,
  type HarnessWorktree,
  isStaleWorktreeSelector,
  shortRevision,
  switchHarnessSessionWorktree,
  type WorktreeSwitchMode,
} from "@/lib/harness/worktrees";
import { cn } from "@/lib/utils";

export const WORKTREE_PICKER_TITLE = "Switch worktree";

/** The daemon listed nothing: a no-FS chat, or a repo with one worktree. */
export const NO_ELIGIBLE_WORKTREES =
  "No other worktrees are eligible from this chat.";

/** A selector went stale between the list and the confirm: relist. */
export const WORKTREE_LIST_CHANGED = "Worktree list changed — pick again";

export const WORKTREE_SOURCE_BUSY =
  "This chat is still busy — wait for it to settle, then switch worktree.";

export const WORKTREES_UNSUPPORTED = "This daemon cannot list worktrees.";

export const WORKTREE_SWITCH_CONFIRM = "Switch";

/** The success toast: where the new chat is rooted, as the daemon labels it. */
export function switchedWorktreeToast(worktree: {
  label: string;
  branch: string;
}): string {
  return worktree.branch
    ? `Now working in ${worktree.label} (${worktree.branch})`
    : `Now working in ${worktree.label}`;
}

const MODE_COPY: Record<
  WorktreeSwitchMode,
  { readonly title: string; readonly detail: string }
> = {
  clear: {
    title: "Start fresh there",
    detail:
      "An empty conversation with the same model, mode and limits (the default).",
  },
  fork: {
    title: "Bring this conversation",
    detail: "A copy of this chat's history continues in the new worktree.",
  },
};

/**
 * The `/worktrees` placement picker, for the browser: lists the sibling git
 * worktrees the daemon offers as a destination from the current chat
 * (display metadata only — label, branch, revision, never a path; ADR 0291),
 * marks the one this chat already lives in, and on confirm mints a NEW chat
 * rooted at the pick — an empty successor (clear, the default) or a copy of
 * this conversation (fork). The current chat stays in the list untouched.
 *
 * Selectors are opaque and never persisted server-side, so a stale one
 * (daemon restarted, worktree removed) is not an error the user can act on:
 * the dialog says the list changed, relists, and stays open for another
 * pick. The list is read fresh every time the dialog opens.
 */
export function WorktreePickerDialog({
  sessionId,
  sessionTitle,
  open,
  onOpenChange,
  onSwitched,
}: {
  /** The source chat; null renders nothing (a draft has no placement). */
  sessionId: string | null;
  /** The source chat's title, carried onto a fork. */
  sessionTitle: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Adopts the new chat once the daemon answered — refresh the list, move
   *  the UI there. Runs BEFORE the dialog closes and the toast shows. */
  onSwitched: (
    newSessionId: string,
    worktree: HarnessWorktree,
    mode: WorktreeSwitchMode,
  ) => void | Promise<void>;
}) {
  const [worktrees, setWorktrees] = useState<HarnessWorktree[] | null>(null);
  const [currentLabel, setCurrentLabel] = useState<string | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [selected, setSelected] = useState<string | null>(null);
  const [mode, setMode] = useState<WorktreeSwitchMode>("clear");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const radioGroup = useId();
  const modeGroup = useId();

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` is the Retry/relist trigger — bumping it re-runs the read
  useEffect(() => {
    if (!open || !sessionId) return;
    const controller = new AbortController();
    setWorktrees(null);
    setListError(null);
    setSubmitError(null);
    // The current placement's DISPLAY label, so the row this chat already
    // lives in is marked. Best-effort: an older daemon or a failed read only
    // loses the mark, never the list.
    fetchHarnessSessionIdentity(sessionId, controller.signal)
      .then((identity) => {
        if (!controller.signal.aborted)
          setCurrentLabel(identity.placement?.label ?? null);
      })
      .catch(() => {
        if (!controller.signal.aborted) setCurrentLabel(null);
      });
    fetchHarnessWorktrees(sessionId, controller.signal)
      .then((list) => {
        if (!controller.signal.aborted) setWorktrees(list);
      })
      .catch((caught: unknown) => {
        if (controller.signal.aborted) return;
        setListError(
          isUnsupportedByDaemon(caught)
            ? WORKTREES_UNSUPPORTED
            : caught instanceof Error
              ? caught.message
              : String(caught),
        );
      });
    return () => controller.abort();
  }, [open, sessionId, attempt]);

  // A fresh open starts from no pick and the default mode.
  useEffect(() => {
    if (open) {
      setSelected(null);
      setMode("clear");
    }
  }, [open]);

  const loading = open && worktrees === null && listError === null;
  const pick = worktrees?.find((w) => w.selector === selected) ?? null;
  const canConfirm = !!pick && !submitting && !loading;

  const confirm = async () => {
    if (!sessionId || !pick) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const newId = await switchHarnessSessionWorktree(
        sessionId,
        pick.selector,
        mode,
        sessionTitle,
      );
      await onSwitched(newId, pick, mode);
      toast.success(switchedWorktreeToast(pick));
      onOpenChange(false);
    } catch (caught) {
      if (isStaleWorktreeSelector(caught)) {
        // The pick no longer names a worktree we may use: relist, keep the
        // dialog open, and ask for another pick.
        toast.error(WORKTREE_LIST_CHANGED);
        setSelected(null);
        setAttempt((n) => n + 1);
        return;
      }
      const busy =
        caught instanceof ThreadSourceBusyError ||
        (caught instanceof HarnessApiError && caught.status === 412);
      setSubmitError(
        busy
          ? WORKTREE_SOURCE_BUSY
          : caught instanceof Error
            ? caught.message
            : String(caught),
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{WORKTREE_PICKER_TITLE}</DialogTitle>
          <DialogDescription>
            Start a chat rooted at another worktree of this repository. This
            chat stays in the list.
          </DialogDescription>
        </DialogHeader>

        {loading && (
          <p
            className="flex items-center gap-2 text-sm text-muted-foreground"
            role="status"
          >
            <Loader2 className="size-4 animate-spin" />
            Listing worktrees…
          </p>
        )}

        {listError && (
          <div className="flex flex-col gap-2">
            <p className="text-sm text-destructive" role="alert">
              {listError}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="w-fit"
              onClick={() => setAttempt((n) => n + 1)}
            >
              <RefreshCw className="size-3.5" />
              Retry
            </Button>
          </div>
        )}

        {worktrees && worktrees.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {NO_ELIGIBLE_WORKTREES}
          </p>
        )}

        {worktrees && worktrees.length > 0 && (
          <>
            <fieldset className="flex flex-col gap-1" disabled={submitting}>
              <legend className="mb-1.5 text-xs font-medium text-muted-foreground">
                Worktree
              </legend>
              {worktrees.map((worktree) => {
                const isCurrent =
                  currentLabel !== null && worktree.label === currentLabel;
                const checked = selected === worktree.selector;
                return (
                  <label
                    key={worktree.selector}
                    className={cn(
                      "flex cursor-pointer items-center gap-3 rounded-md border border-border px-3 py-2 text-sm transition-colors hover:bg-muted/50",
                      "has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring has-[:focus-visible]:ring-offset-2",
                      checked && "border-primary bg-muted/40",
                    )}
                  >
                    <input
                      type="radio"
                      name={radioGroup}
                      value={worktree.selector}
                      checked={checked}
                      onChange={() => setSelected(worktree.selector)}
                      className="size-4 shrink-0 accent-primary"
                    />
                    {/* The `{" "}` separators are for the radio's accessible
                        name ("feature-x feature/x 0123456"); flex ignores
                        whitespace-only text nodes, so nothing shifts. */}
                    <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
                      <span className="truncate font-mono text-xs">
                        {worktree.label}
                      </span>{" "}
                      {worktree.branch && (
                        <Badge variant="outline" className="font-mono">
                          {worktree.branch}
                        </Badge>
                      )}{" "}
                      {worktree.revision && (
                        <span className="font-mono text-xs text-muted-foreground">
                          {shortRevision(worktree.revision)}
                        </span>
                      )}{" "}
                      {worktree.bare && <Badge variant="muted">bare</Badge>}{" "}
                      {isCurrent && <Badge variant="info">current</Badge>}
                    </span>
                  </label>
                );
              })}
            </fieldset>

            <fieldset className="flex flex-col gap-1" disabled={submitting}>
              <legend className="mb-1.5 text-xs font-medium text-muted-foreground">
                What to bring
              </legend>
              {(Object.keys(MODE_COPY) as WorktreeSwitchMode[]).map((key) => {
                const detailId = `${modeGroup}-${key}`;
                return (
                  <div key={key} className="flex flex-col gap-0.5 px-1 py-1">
                    <label className="flex cursor-pointer items-center gap-3 text-sm">
                      <input
                        type="radio"
                        name={modeGroup}
                        value={key}
                        checked={mode === key}
                        onChange={() => setMode(key)}
                        aria-describedby={detailId}
                        className="size-4 shrink-0 accent-primary"
                      />
                      {MODE_COPY[key].title}
                    </label>
                    <p
                      id={detailId}
                      className="pl-7 text-xs text-muted-foreground"
                    >
                      {MODE_COPY[key].detail}
                    </p>
                  </div>
                );
              })}
            </fieldset>
          </>
        )}

        {submitError && (
          <p className="text-sm text-destructive" role="alert">
            {submitError}
          </p>
        )}

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
          >
            Cancel
          </Button>
          <Button onClick={() => void confirm()} disabled={!canConfirm}>
            {submitting && <Loader2 className="size-4 animate-spin" />}
            {WORKTREE_SWITCH_CONFIRM}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
