// SPDX-License-Identifier: Apache-2.0

import type { ListLearnedSkillsResponse } from "@mecatl-studio/contracts/generated";
import {
  actOnLearnedSkillMutation,
  diffLearnedSkillVersionsOptions,
  listLearnedSkillChangesOptions,
  listLearnedSkillChangesQueryKey,
  listLearnedSkillsOptions,
  listLearnedSkillsQueryKey,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, useSyncExternalStore } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../../components/ui/alert-dialog";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../../components/ui/dialog";
import { errorMessage, formatDate, NO_DESCRIPTION } from "./format";
import { DiffBlock, hasSkillDiffChanges, skillDiffRows } from "./skill-diff";
import { StateCard } from "./state-card";

type LearnedSkill = ListLearnedSkillsResponse["items"][number];
type LearnedSkillAction = "activate" | "archive" | "reject" | "rollback";

/**
 * DECISION: learned-skill review follows the Studio design baseline (#1779): a flat list, a review dialog with the version diff and body, and a "Recent changes"
 * ledger. The dialog is driven by the route (`/workspace/skills/learned/$item`) so search
 * results and links still deep-link to one skill. Rejected: the routed detail page, which
 * the baseline folds into the dialog; its per-skill history is covered by the ledger.
 *
 * DECISION: the dialog offers only the actions the daemon reports in `skill.actions`.
 * Reason: the issue requires permitted-only actions; the baseline derived them from state.
 *
 * SPEC: a failed action (stale revision, conflict) re-reads the inventory and ledger and
 * shows the error inside the dialog; it never closes the dialog or reports success.
 */
const LEDGER_LIMIT = 20;

/**
 * DECISION: the publish-failure notice lives in a tiny module-level store, not component state.
 * Reason: closing the dialog navigates from `/workspace/skills/learned/$item` to
 * `/workspace/skills`, two routes, so the list remounts and component state would drop the
 * warning at the moment it matters. Rejected: a search parameter, which would put message text
 * in the URL.
 */
let publicationNotice: string | undefined;
const noticeListeners = new Set<() => void>();

function setPublicationNotice(next?: string) {
  publicationNotice = next;
  for (const listener of noticeListeners) listener();
}

function usePublicationNotice() {
  return useSyncExternalStore(
    (listener) => {
      noticeListeners.add(listener);
      return () => noticeListeners.delete(listener);
    },
    () => publicationNotice,
  );
}

/** Test seam: the store outlives a component, so tests reset it between cases. */
export function resetPublicationNotice() {
  setPublicationNotice(undefined);
}

export function LearnedSkills({
  onSelect,
  selectedId,
}: {
  onSelect: (id?: string) => void;
  selectedId?: string;
}) {
  const queryClient = useQueryClient();
  const query = useQuery(listLearnedSkillsOptions());
  const changes = useQuery(listLearnedSkillChangesOptions());
  const mutation = useMutation(actOnLearnedSkillMutation());
  const [error, setError] = useState<string>();
  const notice = usePublicationNotice();
  const [pendingAction, setPendingAction] = useState<LearnedSkillAction>();
  const selected = query.data?.items.find((skill) => skill.id === selectedId);

  async function refresh() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: listLearnedSkillsQueryKey() }),
      queryClient.invalidateQueries({ queryKey: listLearnedSkillChangesQueryKey() }),
    ]);
  }

  async function act(skill: LearnedSkill, action: LearnedSkillAction) {
    setError(undefined);
    setPublicationNotice(undefined);
    try {
      const result = await mutation.mutateAsync({
        body: {
          action,
          expectedRevision: skill.revision,
          ownerAgent: skill.ownerAgent,
          ...(action === "rollback" ? { targetVersion: skill.supersedes } : {}),
          version: skill.version,
        },
        path: { skillId: skill.id },
      });
      // The daemon records the action and then publishes it into the live catalog; the two can
      // diverge, and a recorded-but-unpublished skill must not read as success.
      if (result.publicationError)
        setPublicationNotice(
          `Recorded, but publishing into the live inventory failed: ${result.publicationError}`,
        );
      onSelect(undefined);
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setPendingAction(undefined);
      await refresh();
    }
  }

  if (query.isPending) return <StateCard text="Loading learned skills…" />;
  if (query.isError) return <StateCard error text={errorMessage(query.error)} />;
  if (!query.data.supported)
    return <StateCard text={query.data.reason} title="Learned skills are unavailable" />;
  const allChanges = changes.data?.items ?? [];
  const ledger = allChanges.slice(0, LEDGER_LIMIT);
  return (
    <div className="space-y-5">
      {notice && (
        <div
          className="flex items-start justify-between gap-3 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm"
          role="alert"
        >
          <p>{notice}</p>
          <Button onClick={() => setPublicationNotice(undefined)} size="sm" variant="ghost">
            Dismiss
          </Button>
        </div>
      )}
      {query.data.items.length === 0 ? (
        <StateCard
          icon="sparkles"
          text="When the agent drafts a procedure from reflection, it lands here for review before it can reach the live inventory."
          title="No learned skills yet"
        />
      ) : (
        <>
          {!query.data.complete && (
            <p className="text-xs text-warning">Showing the first 100 learned skills.</p>
          )}
          <div className="divide-y overflow-hidden rounded-xl border bg-card">
            {query.data.items.map((skill) => (
              <button
                className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-muted/50"
                key={`${skill.id}-${skill.version}`}
                onClick={() => onSelect(skill.id)}
                type="button"
              >
                <span className="min-w-0 flex-1">
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-sm font-medium">{skill.name}</span>
                    <span className="font-mono text-xs text-muted-foreground">{skill.version}</span>
                    <SkillState state={skill.state} />
                  </span>
                  <span className="mt-0.5 line-clamp-2 block text-xs text-muted-foreground">
                    {skill.description || NO_DESCRIPTION}
                  </span>
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">
                  {skill.ownerAgent}
                  {skill.updatedAt && ` · ${formatDate(skill.updatedAt)}`}
                </span>
              </button>
            ))}
          </div>
        </>
      )}

      {ledger.length > 0 && (
        <div className="space-y-2">
          <h2 className="text-sm font-semibold tracking-wide text-muted-foreground uppercase">
            Recent changes
          </h2>
          <ul className="divide-y overflow-hidden rounded-xl border bg-card">
            {ledger.map((change) => (
              <li className="flex flex-wrap items-center gap-2 px-4 py-2 text-xs" key={change.id}>
                <span className="font-medium">{change.name}</span>
                <span className="font-mono text-muted-foreground">{change.version}</span>
                <span className="text-muted-foreground">
                  {humanizeOperation(change.operation)}
                  {change.fromState &&
                    change.toState &&
                    ` — ${change.fromState} → ${change.toState}`}
                </span>
                {change.verdict && <Badge variant="outline">{change.verdict}</Badge>}
                {change.at && (
                  <span className="ml-auto text-muted-foreground">{formatDate(change.at)}</span>
                )}
              </li>
            ))}
          </ul>
          {(allChanges.length > LEDGER_LIMIT || changes.data?.complete === false) && (
            <p className="text-xs text-warning">
              Showing the {LEDGER_LIMIT} most recent lifecycle changes.
            </p>
          )}
        </div>
      )}

      {selectedId && !selected && (
        <p className="text-sm text-muted-foreground">
          That learned skill no longer exists or is not visible to you.
        </p>
      )}
      {selected && (
        <LearnedSkillDialog
          busy={mutation.isPending}
          error={error}
          onAction={(action) => setPendingAction(action)}
          onClose={() => {
            setError(undefined);
            onSelect(undefined);
          }}
          skill={selected}
        />
      )}
      <AlertDialog
        onOpenChange={(open) => !open && setPendingAction(undefined)}
        open={Boolean(pendingAction && selected)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingAction && selected && actionCopy(pendingAction, selected).title}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingAction && selected && actionCopy(pendingAction, selected).description}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={mutation.isPending}
              onClick={() => pendingAction && selected && void act(selected, pendingAction)}
            >
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function LearnedSkillDialog({
  busy,
  error,
  onAction,
  onClose,
  skill,
}: {
  busy: boolean;
  error?: string;
  onAction: (action: LearnedSkillAction) => void;
  onClose: () => void;
  skill: LearnedSkill;
}) {
  const comparison = useQuery({
    ...diffLearnedSkillVersionsOptions({
      path: { skillId: skill.id },
      query: {
        fromVersion: skill.supersedes || "pending",
        ownerAgent: skill.ownerAgent,
        toVersion: skill.version,
      },
    }),
    enabled: Boolean(skill.supersedes),
  });
  const rows = comparison.data?.diff
    ? skillDiffRows(comparison.data.diff, { body: skill.body, description: skill.description })
    : [];
  return (
    <Dialog onOpenChange={(open) => !open && onClose()} open>
      <DialogContent className="flex max-h-[85dvh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            <span>{skill.name}</span>
            <span className="font-mono text-sm font-normal text-muted-foreground">
              {skill.version}
            </span>
            <SkillState state={skill.state} />
          </DialogTitle>
          <DialogDescription>
            {skill.description || NO_DESCRIPTION}
            {skill.ownerAgent && ` — owned by ${skill.ownerAgent}.`}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto">
          {error && (
            <p className="rounded-lg bg-destructive/10 p-3 text-sm text-foreground" role="alert">
              {error} The skill was refreshed; review its current state before trying again.
            </p>
          )}
          {skill.supersedes && (
            <div className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">
                Changes since {skill.supersedes}
              </p>
              {comparison.isPending ? (
                <p className="text-xs text-muted-foreground">Comparing versions…</p>
              ) : comparison.isError || !hasSkillDiffChanges(rows) ? (
                <p className="text-xs text-muted-foreground">
                  No textual diff available — full body below.
                </p>
              ) : (
                <DiffBlock rows={rows} />
              )}
            </div>
          )}
          {skill.body && (
            <div className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">Body</p>
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap rounded-md bg-muted px-3 py-2 font-mono text-xs">
                {skill.body}
              </pre>
            </div>
          )}
          <p className="text-xs text-muted-foreground">
            {skill.evidenceCount} evidence reference{skill.evidenceCount === 1 ? "" : "s"}
            {skill.updatedAt && ` · updated ${formatDate(skill.updatedAt)}`}
          </p>
        </div>
        <DialogFooter className="flex-wrap gap-2">
          {skill.actions.reject && (
            <Button disabled={busy} onClick={() => onAction("reject")} size="sm" variant="outline">
              Reject
            </Button>
          )}
          {skill.actions.activate && (
            <Button disabled={busy} onClick={() => onAction("activate")} size="sm" variant="action">
              Activate
            </Button>
          )}
          {skill.actions.rollback && (
            <Button
              disabled={busy}
              onClick={() => onAction("rollback")}
              size="sm"
              variant="outline"
            >
              Roll back to {skill.supersedes}
            </Button>
          )}
          {skill.actions.archive && (
            <Button disabled={busy} onClick={() => onAction("archive")} size="sm" variant="outline">
              Archive
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

const STATE_VARIANTS: Record<
  string,
  "destructive" | "info" | "muted" | "outline" | "success" | "warning"
> = {
  active: "success",
  archived: "outline",
  evaluated: "info",
  rejected: "destructive",
  staged: "warning",
};

function SkillState({ state }: { state: string }) {
  return <Badge variant={STATE_VARIANTS[state] ?? "muted"}>{state || "unknown"}</Badge>;
}

function actionCopy(action: LearnedSkillAction, skill: LearnedSkill) {
  const name = `${skill.name} ${skill.version}`;
  if (action === "activate")
    return {
      description:
        "The skill is published into the live inventory, so the agent can load and follow it from the next run.",
      title: `Activate ${name}?`,
    };
  if (action === "reject")
    return {
      description:
        "The draft is retired without reaching the agent. The version stays inspectable in history.",
      title: `Reject ${name}?`,
    };
  if (action === "archive")
    return {
      description:
        "The skill is withdrawn from the live inventory. It stays inspectable and can be rolled back to later.",
      title: `Archive ${name}?`,
    };
  return {
    description: `${skill.name} returns to version ${skill.supersedes}; ${skill.version} is retired.`,
    title: `Roll back to ${skill.supersedes}?`,
  };
}

function humanizeOperation(value: string) {
  return value.replaceAll("_", " ");
}
