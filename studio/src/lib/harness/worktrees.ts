/**
 * Sibling git worktrees a chat can move to — the web analogue of the TUI's
 * `/worktrees` placement picker. Placement is server-owned (ADR 0291): the
 * daemon lists the worktrees ELIGIBLE from a source session as display
 * metadata (label, branch, revision — never a path) plus an opaque
 * `selector`, an HMAC token scoped to the caller and the source session that
 * only ClearSession and ForkSession accept. Selectors are never persisted
 * server-side: after a daemon restart (or a worktree add/remove) one fails
 * with a `placement_selector_*` code and the caller re-lists.
 */

import type { Worktree } from "@stacklok-oss/mecatl-sdk/gen";
import { HarnessApiError } from "./errors";
import { getHarnessClient, harness } from "./sdk";
import { clearHarnessSession, forkHarnessSessionToWorktree } from "./sessions";

/** One eligible placement choice as the daemon describes it. */
export interface HarnessWorktree {
  /** Opaque, caller + source-session scoped; accepted by clear/fork only. */
  selector: string;
  /** The daemon's placement kind word (e.g. "git-worktree"). */
  kind: string;
  /** Display label (the worktree's directory name), never a path. */
  label: string;
  /** Checked-out branch; "" for a detached HEAD. */
  branch: string;
  /** Full HEAD revision; "" when unknown. */
  revision: string;
  /** True for a bare worktree (nothing checked out). */
  bare: boolean;
}

function worktreeFromSdk(value: Worktree): HarnessWorktree {
  return {
    selector: value.selector ?? "",
    kind: value.kind ?? "",
    label: value.label ?? "",
    branch: value.branch ?? "",
    revision: value.revision ?? "",
    bare: value.bare === true,
  };
}

/**
 * Lists the worktrees the daemon offers as a destination from
 * `sessionId` (`GET /v1/worktrees?session_id=`), in `git worktree list`
 * order — the main worktree first. A no-FS session, or a repository with no
 * sibling worktrees, answers an empty list. Rows carrying no selector are
 * dropped: nothing could be done with them.
 */
export async function fetchHarnessWorktrees(
  sessionId: string,
  signal?: AbortSignal,
): Promise<HarnessWorktree[]> {
  const response = await harness(() =>
    getHarnessClient().worktrees.list(
      { $typeName: "mecatl.v1.ListWorktreesRequest", sessionId },
      { signal },
    ),
  );
  return (response.worktrees ?? [])
    .map(worktreeFromSdk)
    .filter((worktree) => worktree.selector !== "");
}

/**
 * The daemon codes that mean "this selector no longer names a worktree the
 * caller may use": it expired with the daemon's key (restart), the worktree
 * it named is gone, or it was never one of ours. The remedy is the same for
 * all three — re-list and pick again — so callers narrow on the set.
 */
const STALE_WORKTREE_SELECTOR_CODES: ReadonlySet<string> = new Set([
  "placement_selector_stale",
  "placement_selector_not_found",
  "placement_selector_invalid",
]);

/** True when a clear/fork failed because its worktree selector went stale. */
export function isStaleWorktreeSelector(error: unknown): boolean {
  return (
    error instanceof HarnessApiError &&
    STALE_WORKTREE_SELECTOR_CODES.has(error.code)
  );
}

/** The first seven characters of a revision, the way `git log --oneline`
 *  abbreviates one; "" stays "". */
export function shortRevision(revision: string): string {
  return revision.slice(0, 7);
}

/**
 * How a chat moves to a worktree: `clear` mints an EMPTY-history successor
 * rooted there (same model, mode and limits — the TUI's default), `fork`
 * carries this conversation's history along.
 */
export type WorktreeSwitchMode = "clear" | "fork";

/**
 * Moves `sourceSessionId` to the worktree `selector` names and resolves the
 * NEW session's id (its SDK handle already adopted). The source chat is left
 * intact in the list. A stale selector rejects with a `placement_selector_*`
 * HarnessApiError (see `isStaleWorktreeSelector`); a running/awaiting source
 * answers 412 on a fork (ThreadSourceBusyError) — a clear cancels it itself.
 */
export async function switchHarnessSessionWorktree(
  sourceSessionId: string,
  selector: string,
  mode: WorktreeSwitchMode,
  title: string,
  signal?: AbortSignal,
): Promise<string> {
  if (mode === "fork") {
    return forkHarnessSessionToWorktree(
      sourceSessionId,
      title,
      selector,
      signal,
    );
  }
  return clearHarnessSession(
    sourceSessionId,
    { worktreeSelector: selector },
    signal,
  );
}
