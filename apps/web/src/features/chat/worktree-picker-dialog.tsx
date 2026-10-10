// SPDX-License-Identifier: Apache-2.0

import type { SessionWorktreesResponse } from "@mecatl-studio/contracts";
import { Loader2, RefreshCw } from "lucide-react";
import { useId } from "react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { DialogFooter } from "../../components/ui/dialog";
import { RadioGroup, RadioGroupItem } from "../../components/ui/radio-group";

/**
 * The worktree view of the session dialog, ported from the prototype's
 * `worktree-picker-dialog.tsx`: the sibling worktrees the daemon offers as
 * a successor's destination (display metadata only: label, branch,
 * revision; never a path). `SessionInspection` keeps the choice, the
 * fork and clear calls, and the relist-on-selector-rejection rule; this
 * only renders them.
 */

export const NO_ELIGIBLE_WORKTREES = "No eligible worktrees for this session.";

function shortRevision(revision: string): string {
  return revision.slice(0, 7);
}

export function WorktreePickerView({
  busy,
  canClear,
  canFork,
  error,
  onClear,
  onFork,
  onRetry,
  onSelect,
  pending,
  selected,
  unavailable,
  worktrees,
}: {
  busy: boolean;
  canClear: boolean;
  canFork: boolean;
  error: boolean;
  onClear: () => void;
  onFork: () => void;
  onRetry: () => void;
  onSelect: (selector: string) => void;
  pending: boolean;
  selected?: string;
  /** Why no successor can be created, when neither action is offered. */
  unavailable?: string;
  worktrees?: SessionWorktreesResponse;
}) {
  const ids = useId();
  if (pending) {
    return (
      <p className="flex items-center gap-2 text-muted-foreground" role="status">
        <Loader2 aria-hidden="true" className="size-4 animate-spin" />
        Loading worktrees…
      </p>
    );
  }
  if (error) {
    return (
      <div className="flex flex-col gap-2">
        <p className="text-destructive">Worktrees could not be read.</p>
        <Button className="w-fit" onClick={onRetry} size="sm" variant="outline">
          <RefreshCw aria-hidden="true" className="size-3.5" />
          Retry
        </Button>
      </div>
    );
  }
  if (!worktrees) return null;
  const list = worktrees.items;
  const chosen = list.some((item) => item.selector === selected);
  return (
    <div className="flex flex-col gap-4">
      {list.length === 0 ? (
        <p className="text-muted-foreground">{NO_ELIGIBLE_WORKTREES}</p>
      ) : (
        <fieldset className="flex flex-col gap-1" disabled={busy}>
          <legend className="mb-1.5 text-xs font-medium text-muted-foreground" id={`${ids}-legend`}>
            Eligible worktrees
          </legend>
          <RadioGroup
            aria-labelledby={`${ids}-legend`}
            className="flex flex-col gap-1"
            disabled={busy}
            onValueChange={onSelect}
            value={chosen ? selected : ""}
          >
            {list.map((worktree, index) => (
              <label
                className="flex cursor-pointer items-center gap-3 rounded-md border border-border px-3 py-2 transition-colors hover:bg-muted/50 has-[[data-state=checked]]:border-primary has-[[data-state=checked]]:bg-muted/40 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring has-[:focus-visible]:ring-offset-2"
                htmlFor={`${ids}-radio-${index}`}
                key={worktree.selector}
              >
                <RadioGroupItem
                  aria-labelledby={`${ids}-${index}`}
                  className="focus-visible:ring-0"
                  id={`${ids}-radio-${index}`}
                  value={worktree.selector}
                />
                <span
                  className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1"
                  id={`${ids}-${index}`}
                >
                  <span className="truncate font-mono text-xs">{worktree.label}</span>
                  {worktree.branch && (
                    <Badge className="font-mono" variant="outline">
                      {worktree.branch}
                    </Badge>
                  )}
                  {worktree.revision && (
                    <span className="font-mono text-xs text-muted-foreground">
                      {shortRevision(worktree.revision)}
                    </span>
                  )}
                  {worktree.bare && <Badge variant="muted">bare</Badge>}
                </span>
              </label>
            ))}
          </RadioGroup>
        </fieldset>
      )}
      {canFork || canClear ? (
        <div className="flex flex-col gap-2">
          <p className="text-xs text-muted-foreground">
            Fork brings this conversation's history; clear starts an empty conversation with the
            same model, mode, and limits. This chat stays in the list.
          </p>
          <DialogFooter>
            {canClear && (
              <Button disabled={!chosen || busy} onClick={onClear} size="sm" variant="outline">
                Clear in selected worktree
              </Button>
            )}
            {canFork && (
              <Button disabled={!chosen || busy} onClick={onFork} size="sm">
                {busy && <Loader2 aria-hidden="true" className="size-4 animate-spin" />}
                Fork in selected worktree
              </Button>
            )}
          </DialogFooter>
        </div>
      ) : (
        unavailable && <p className="text-muted-foreground">{unavailable}</p>
      )}
    </div>
  );
}
