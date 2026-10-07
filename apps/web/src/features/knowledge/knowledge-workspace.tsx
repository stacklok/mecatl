// SPDX-License-Identifier: Apache-2.0

import type { ListLearnedSkillsResponse } from "@mecatl-studio/contracts/generated";
import {
  actOnLearnedSkillMutation,
  diffLearnedSkillVersionsOptions,
  getRuntimeOptions,
  listConfiguredSkillsOptions,
  listLearnedSkillChangesOptions,
  listLearnedSkillChangesQueryKey,
  listLearnedSkillsOptions,
  listLearnedSkillsQueryKey,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Brain, GraduationCap, Sparkles } from "lucide-react";
import { useState } from "react";
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
import { Input } from "../../components/ui/input";
import { SortableHead, type SortDirection } from "../../components/ui/sortable-head";
import { Table, TableBody, TableCell, TableHeader, TableRow } from "../../components/ui/table";
import { pageTitleClass } from "../../lib/typography";
import { humanizeSkillName } from "./humanize-skill-name";
import { DiffBlock, hasSkillDiffChanges, skillDiffRows } from "./skill-diff";
import { filterSkills, type SkillSortKey, sortSkills } from "./skill-inventory";
import { SkillToolDisabledBanner } from "./skill-tool-disabled-banner";

export type KnowledgeView = "configured" | "learned";
type LearnedSkill = ListLearnedSkillsResponse["items"][number];
type LearnedSkillAction = "activate" | "archive" | "reject" | "rollback";

export function KnowledgeWorkspace({
  item,
  onItemChange,
  onViewChange,
  view,
}: {
  item?: string;
  onItemChange: (item?: string) => void;
  onViewChange: (view: KnowledgeView) => void;
  view: KnowledgeView;
}) {
  const runtime = useQuery(getRuntimeOptions());
  // Hide the Learned pill only once the daemon says it has no learned-skill inventory.
  const learnedHidden = runtime.data?.capabilities.learnedSkills === false;
  const activeView = view === "learned" && learnedHidden ? "configured" : view;
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-6xl space-y-5 px-4 py-7 sm:px-8 sm:py-10">
        <SkillToolDisabledBanner />
        <h1 className={pageTitleClass()}>Skills</h1>
        <div className="inline-flex max-w-full items-center gap-0.5 overflow-x-auto rounded-full bg-muted p-1">
          {(
            [
              ["configured", "All"],
              ...(learnedHidden ? [] : [["learned", "Learned"] as const]),
            ] as const
          ).map(([value, label]) => (
            <button
              aria-pressed={activeView === value}
              className={`h-7 rounded-full px-3.5 text-sm transition-colors ${activeView === value ? "bg-background font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"}`}
              key={value}
              onClick={() => onViewChange(value)}
              type="button"
            >
              {label}
            </button>
          ))}
        </div>
        {activeView === "configured" ? (
          <ConfiguredSkills />
        ) : (
          <LearnedSkills onSelect={onItemChange} selectedId={item} />
        )}
      </div>
    </div>
  );
}

function ConfiguredSkills() {
  const query = useQuery(listConfiguredSkillsOptions());
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<{ direction: SortDirection; key: SkillSortKey }>({
    direction: "asc",
    key: "name",
  });
  if (query.isPending) return <StateCard text="Loading skills…" />;
  if (query.isError) return <StateCard error text={errorMessage(query.error)} />;
  if (!query.data.supported)
    return <StateCard text={query.data.reason} title="Skills are unavailable" />;
  if (query.data.items.length === 0) return <StateCard icon="skill" text="No skills here yet" />;
  const rows = sortSkills(filterSkills(query.data.items, filter), sort.key, sort.direction);
  const toggle = (key: SkillSortKey) =>
    setSort((current) => ({
      direction: current.key === key && current.direction === "asc" ? "desc" : "asc",
      key,
    }));
  const direction = (key: SkillSortKey) => (sort.key === key ? sort.direction : undefined);
  return (
    <div className="space-y-3">
      <Input
        aria-label="Filter skills"
        onChange={(event) => setFilter(event.target.value)}
        placeholder="Filter by name, description, or owner"
        value={filter}
      />
      {rows.length === 0 ? (
        <StateCard text={`No skills match "${filter.trim()}".`} />
      ) : (
        <>
          <div className="overflow-hidden rounded-xl border bg-card max-[499px]:hidden">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <SortableHead
                    className="w-px whitespace-nowrap px-4"
                    direction={direction("name")}
                    label="Name"
                    onSort={() => toggle("name")}
                  />
                  <SortableHead
                    className="px-4"
                    direction={direction("description")}
                    label="Description"
                    onSort={() => toggle("description")}
                  />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((skill) => (
                  <TableRow key={skill.name}>
                    <TableCell className="w-px max-w-[360px] whitespace-nowrap px-4 py-3 pr-6">
                      <Link
                        className="block truncate text-sm font-medium hover:underline"
                        params={{ item: skill.name, view: "configured" }}
                        to="/workspace/skills/$view/$item"
                      >
                        {humanizeSkillName(skill.name)}
                      </Link>
                      <p className="truncate font-mono text-xs text-muted-foreground">
                        {skill.name}
                      </p>
                    </TableCell>
                    <TableCell className="w-full max-w-0 px-4 py-3">
                      <p className="line-clamp-1 whitespace-normal text-xs text-muted-foreground">
                        {skill.description || "No description recorded."}
                      </p>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <div className="divide-y overflow-hidden rounded-xl border bg-card min-[500px]:hidden">
            {[...rows]
              .sort((a, b) => humanizeSkillName(a.name).localeCompare(humanizeSkillName(b.name)))
              .map((skill) => (
                <Link
                  className="block px-4 py-3"
                  key={skill.name}
                  params={{ item: skill.name, view: "configured" }}
                  to="/workspace/skills/$view/$item"
                >
                  <span className="block truncate text-sm font-medium">
                    {humanizeSkillName(skill.name)}
                  </span>
                  <span className="line-clamp-2 text-xs text-muted-foreground">
                    {skill.description || "No description recorded."}
                  </span>
                </Link>
              ))}
          </div>
        </>
      )}
    </div>
  );
}

/**
 * DECISION: learned-skill review follows the Studio design baseline (mecatl-prototypes PR
 * #46): a flat list, a review dialog with the version diff and body, and a "Recent changes"
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

function LearnedSkills({
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
    try {
      await mutation.mutateAsync({
        body: {
          action,
          expectedRevision: skill.revision,
          ownerAgent: skill.ownerAgent,
          ...(action === "rollback" ? { targetVersion: skill.supersedes } : {}),
          version: skill.version,
        },
        path: { skillId: skill.id },
      });
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
  const ledger = (changes.data?.items ?? []).slice(0, LEDGER_LIMIT);
  return (
    <div className="space-y-5">
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
                    {skill.description || "No description recorded."}
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
          {changes.data && !changes.data.complete && (
            <p className="text-xs text-warning">Showing the most recent lifecycle changes only.</p>
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
            {skill.description || "No description recorded."}
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

export function StateCard({
  error,
  icon,
  text,
  title,
}: {
  error?: boolean;
  icon?: "memory" | "skill" | "sparkles";
  text: string;
  title?: string;
}) {
  const Icon = icon === "memory" ? Brain : icon === "sparkles" ? Sparkles : GraduationCap;
  return (
    <div
      className={`flex min-h-60 flex-col items-center justify-center rounded-xl border border-dashed p-8 text-center ${error ? "border-destructive/40 text-destructive" : ""}`}
    >
      {icon && (
        <span className="flex size-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
          <Icon className="size-5" />
        </span>
      )}
      {title && <h2 className="mt-4 font-semibold">{title}</h2>}
      <p className="mt-2 max-w-md text-sm text-muted-foreground">{text}</p>
    </div>
  );
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(
    new Date(value),
  );
}
function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "The request could not be completed.";
}
