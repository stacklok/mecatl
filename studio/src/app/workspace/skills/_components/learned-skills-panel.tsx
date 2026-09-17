"use client";

import { Sparkles } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
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
import { onRunFinished } from "@/features/agent/run-signals";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { formatRelativeTime } from "@/lib/formatters";
import {
  diffLearnedSkillVersions,
  fetchLearnedSkill,
  type LearnedSkillAction,
  type LearnedSkillChange,
  type LearnedSkillVersion,
  listLearnedSkillChanges,
  listLearnedSkills,
  mutateLearnedSkill,
  rollbackLearnedSkill,
} from "@/lib/harness/learned-skills";
import { cn } from "@/lib/utils";

/**
 * The "Learned" view of the Skills page (ADR 0110): agent-drafted skill
 * versions awaiting human review, plus the recent lifecycle changes. This is
 * the daemon's own store — nothing here goes through the controller or
 * restarts the daemon.
 */

const STATE_BADGES: Record<
  string,
  "muted" | "info" | "warning" | "success" | "destructive" | "outline"
> = {
  draft: "muted",
  evaluated: "info",
  staged: "warning",
  active: "success",
  rejected: "destructive",
  archived: "outline",
};

function StateBadge({ state }: { state: string }) {
  return (
    <Badge variant={STATE_BADGES[state] ?? "muted"}>{state || "unknown"}</Badge>
  );
}

/** States a human can still promote or retire from. */
function isReviewable(state: string): boolean {
  return state === "draft" || state === "evaluated" || state === "staged";
}

type PendingMutation =
  | { kind: LearnedSkillAction; skill: LearnedSkillVersion }
  | { kind: "rollback"; skill: LearnedSkillVersion };

function mutationCopy(pending: PendingMutation): {
  title: string;
  description: string;
  actionLabel: string;
} {
  const { kind, skill } = pending;
  const name = `${skill.name} ${skill.version}`;
  switch (kind) {
    case "activate":
      return {
        title: `Activate ${name}?`,
        description:
          "The skill is published into the live inventory — the agent can load and follow it from the next run.",
        actionLabel: "Activate",
      };
    case "reject":
      return {
        title: `Reject ${name}?`,
        description:
          "The draft is retired without ever reaching the agent. The version stays inspectable in history.",
        actionLabel: "Reject",
      };
    case "archive":
      return {
        title: `Archive ${name}?`,
        description:
          "The skill is withdrawn from the live inventory. It stays inspectable and can be rolled back to later.",
        actionLabel: "Archive",
      };
    case "rollback":
      return {
        title: `Roll back to ${skill.supersedes}?`,
        description: `${skill.name} returns to version ${skill.supersedes}; ${skill.version} is retired.`,
        actionLabel: "Roll back",
      };
  }
}

export function LearnedSkillsPanel() {
  const { connected } = useRuntimeStatus();
  const [skills, setSkills] = useState<LearnedSkillVersion[]>([]);
  const [changes, setChanges] = useState<LearnedSkillChange[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<LearnedSkillVersion | null>(null);
  const [pending, setPending] = useState<PendingMutation | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      // Changes are best-effort decoration; the skill list is the surface.
      const changesPromise = listLearnedSkillChanges(
        { limit: 20 },
        signal,
      ).catch(() => ({ changes: [] as LearnedSkillChange[], nextCursor: "" }));
      const page = await listLearnedSkills({}, signal);
      if (signal?.aborted) return;
      setSkills(page.skills);
      setError(null);
      const recent = await changesPromise;
      if (!signal?.aborted) setChanges(recent.changes);
    } catch (caught) {
      if (signal?.aborted) return;
      setSkills([]);
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      if (!signal?.aborted) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    // Kept fresh without a reload (mecatui re-lists receipts on every run
    // result): a run terminal this page observed re-reads the list, and so
    // does the tab coming back into view after the daemon may have moved.
    const stopRuns = onRunFinished(() => void load(controller.signal));
    const onVisibility = () => {
      if (document.visibilityState === "visible") void load(controller.signal);
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      controller.abort();
      stopRuns();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [connected, load]);

  const runMutation = async (mutation: PendingMutation) => {
    const { skill } = mutation;
    setBusy(true);
    try {
      const result =
        mutation.kind === "rollback"
          ? await rollbackLearnedSkill({
              id: skill.id,
              ownerAgent: skill.ownerAgent,
              targetVersion: skill.supersedes,
              expectedRevision: skill.revision,
            })
          : await mutateLearnedSkill(mutation.kind, {
              id: skill.id,
              ownerAgent: skill.ownerAgent,
              version: skill.version,
              expectedRevision: skill.revision,
            });
      if (result.publicationError) {
        toast.warning(
          `Recorded, but publishing failed: ${result.publicationError}`,
        );
      } else {
        toast.success(`${skill.name} is now ${result.skill.state}`);
      }
      setSelected(null);
      await load();
    } catch (caught) {
      toast.error(caught instanceof Error ? caught.message : String(caught));
      // The revision may be stale — re-read so the next attempt is honest.
      await load();
    } finally {
      setBusy(false);
    }
  };

  if (isLoading && connected) {
    return (
      <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
        Loading learned skills…
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-dashed border-destructive/40 py-12 text-center text-sm text-destructive">
        {error}
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {skills.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed py-16 text-center">
          <div className="flex size-11 items-center justify-center rounded-full bg-muted">
            <Sparkles className="size-5 text-muted-foreground" />
          </div>
          <div className="space-y-1">
            <p className="text-sm font-medium">No learned skills yet</p>
            <p className="max-w-sm text-sm text-muted-foreground">
              When the agent drafts a procedure from reflection, it lands here
              for review before it can reach the live inventory.
            </p>
          </div>
        </div>
      ) : (
        <div className="divide-y overflow-hidden rounded-lg border">
          {skills.map((skill) => (
            <button
              key={`${skill.id}-${skill.version}`}
              type="button"
              onClick={() => setSelected(skill)}
              className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-muted/50"
            >
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-sm font-medium">
                    {skill.name}
                  </span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {skill.version}
                  </span>
                  <StateBadge state={skill.state} />
                </span>
                <span className="mt-0.5 line-clamp-2 block text-xs text-muted-foreground">
                  {skill.description || "No description recorded."}
                </span>
              </span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {skill.ownerAgent}
                {skill.updatedAtUnix > 0 &&
                  ` · ${formatRelativeTime(skill.updatedAtUnix * 1000)} ago`}
              </span>
            </button>
          ))}
        </div>
      )}

      {changes.length > 0 && (
        <div className="space-y-2">
          <h2 className="text-sm font-semibold tracking-wide text-muted-foreground uppercase">
            Recent changes
          </h2>
          <ul className="divide-y overflow-hidden rounded-lg border">
            {changes.map((change) => (
              <li
                key={change.id}
                className="flex flex-wrap items-center gap-2 px-4 py-2 text-xs"
              >
                <span className="font-medium">{change.name}</span>
                <span className="font-mono text-muted-foreground">
                  {change.version}
                </span>
                <span className="text-muted-foreground">
                  {change.operation.replaceAll("_", " ")}
                  {change.fromState &&
                    change.toState &&
                    ` — ${change.fromState} → ${change.toState}`}
                </span>
                {change.verdict && (
                  <Badge variant="outline">{change.verdict}</Badge>
                )}
                {change.atUnix > 0 && (
                  <span className="ml-auto text-muted-foreground">
                    {formatRelativeTime(change.atUnix * 1000)} ago
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}

      {selected && (
        <LearnedSkillDetailDialog
          skill={selected}
          busy={busy}
          onClose={() => setSelected(null)}
          onAction={(kind) => setPending({ kind, skill: selected })}
        />
      )}

      <AlertDialog
        open={pending !== null}
        onOpenChange={(open) => !open && setPending(null)}
      >
        {pending && (
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{mutationCopy(pending).title}</AlertDialogTitle>
              <AlertDialogDescription>
                {mutationCopy(pending).description}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  void runMutation(pending);
                  setPending(null);
                }}
              >
                {mutationCopy(pending).actionLabel}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        )}
      </AlertDialog>
    </div>
  );
}

/**
 * The review detail: full body, and — when this version superseded another —
 * the unified diff against it, added/removed lines tinted. Actions depend on
 * the version's state; every one confirms first (the page's dialog idiom).
 */
function LearnedSkillDetailDialog({
  skill,
  busy,
  onClose,
  onAction,
}: {
  skill: LearnedSkillVersion;
  busy: boolean;
  onClose: () => void;
  onAction: (kind: PendingMutation["kind"]) => void;
}) {
  const [detail, setDetail] = useState<LearnedSkillVersion | null>(null);
  const [diff, setDiff] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    fetchLearnedSkill(
      skill.id,
      skill.ownerAgent,
      skill.version,
      controller.signal,
    )
      .then((full) => {
        if (!controller.signal.aborted) setDetail(full);
      })
      .catch((caught) => {
        if (!controller.signal.aborted) {
          setDetail(skill); // fall back to the bounded list projection
          setLoadError(
            caught instanceof Error ? caught.message : String(caught),
          );
        }
      });
    if (skill.supersedes) {
      diffLearnedSkillVersions(
        skill.id,
        skill.ownerAgent,
        skill.supersedes,
        skill.version,
        controller.signal,
      )
        .then((text) => {
          if (!controller.signal.aborted) setDiff(text);
        })
        .catch(() => {
          // Diff is decoration; the body below still tells the story.
          if (!controller.signal.aborted) setDiff(null);
        });
    }
    return () => controller.abort();
  }, [skill]);

  const body = detail?.body ?? skill.body;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            <span>{skill.name}</span>
            <span className="font-mono text-sm font-normal text-muted-foreground">
              {skill.version}
            </span>
            <StateBadge state={skill.state} />
          </DialogTitle>
          <DialogDescription>
            {skill.description || "No description recorded."}
            {skill.ownerAgent && ` — owned by ${skill.ownerAgent}.`}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto">
          {loadError && <p className="text-xs text-destructive">{loadError}</p>}
          {skill.supersedes && (
            <div className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">
                Changes since {skill.supersedes}
              </p>
              {diff === null ? (
                <p className="text-xs text-muted-foreground">
                  Diff unavailable — full body below.
                </p>
              ) : (
                <DiffBlock diff={diff} />
              )}
            </div>
          )}
          {body && (
            <div className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">Body</p>
              <pre className="max-h-72 overflow-auto rounded-md bg-muted px-3 py-2 font-mono text-xs whitespace-pre-wrap">
                {body}
              </pre>
            </div>
          )}
          {skill.evidenceCount > 0 && (
            <p className="text-xs text-muted-foreground">
              Drafted from {skill.evidenceCount} evidence ref
              {skill.evidenceCount === 1 ? "" : "s"}.
            </p>
          )}
        </div>
        <DialogFooter className="flex-wrap gap-2">
          {isReviewable(skill.state) && (
            <>
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => onAction("reject")}
              >
                Reject
              </Button>
              <Button
                size="sm"
                disabled={busy}
                onClick={() => onAction("activate")}
              >
                Activate
              </Button>
            </>
          )}
          {skill.state === "active" && (
            <>
              {skill.supersedes && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() => onAction("rollback")}
                >
                  Roll back to {skill.supersedes}
                </Button>
              )}
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => onAction("archive")}
              >
                Archive
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Unified diff, one line per row: additions green, removals red, hunks muted. */
function DiffBlock({ diff }: { diff: string }) {
  const lines = diff.split("\n");
  return (
    <pre className="max-h-72 overflow-auto rounded-md border bg-muted/40 px-0 py-1 font-mono text-xs">
      {lines.map((line, index) => (
        <div
          // biome-ignore lint/suspicious/noArrayIndexKey: diff lines are positional and static
          key={index}
          className={cn(
            "px-3 whitespace-pre-wrap",
            line.startsWith("+") &&
              !line.startsWith("+++") &&
              "bg-success/10 text-success",
            line.startsWith("-") &&
              !line.startsWith("---") &&
              "bg-destructive/10 text-destructive",
            (line.startsWith("@@") ||
              line.startsWith("+++") ||
              line.startsWith("---")) &&
              "text-muted-foreground",
          )}
        >
          {line || " "}
        </div>
      ))}
    </pre>
  );
}
