// SPDX-License-Identifier: Apache-2.0

import type { ListLearnedSkillsResponse } from "@mecatl-studio/contracts/generated";
import {
  actOnLearnedSkillMutation,
  listConfiguredSkillsOptions,
  listLearnedSkillsOptions,
  listLearnedSkillsQueryKey,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Brain, GraduationCap, Sparkles } from "lucide-react";
import { useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../../components/ui/alert-dialog";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { Dialog, DialogContent } from "../../components/ui/dialog";
import { Input } from "../../components/ui/input";
import { SortableHead, type SortDirection } from "../../components/ui/sortable-head";
import { Table, TableBody, TableCell, TableHeader, TableRow } from "../../components/ui/table";
import { pageTitleClass } from "../../lib/typography";
import { filterSkills, type SkillSortKey, sortSkills } from "./skill-inventory";

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
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-6xl px-4 py-7 sm:px-8 sm:py-10">
        <h1 className={pageTitleClass()}>Skills</h1>
        <p className="mt-2 max-w-2xl text-sm text-muted-foreground">
          Inspect the procedures available to Mecatl.
        </p>
        <div className="mt-7 inline-flex max-w-full overflow-x-auto rounded-full bg-muted p-1">
          {(
            [
              ["configured", "Skills"],
              ["learned", "Learned"],
            ] as const
          ).map(([value, label]) => (
            <button
              className={`h-8 rounded-full px-4 text-sm ${view === value ? "bg-background font-medium shadow-sm" : "text-muted-foreground"}`}
              key={value}
              onClick={() => onViewChange(value)}
              type="button"
            >
              {label}
            </button>
          ))}
        </div>
        <div className="mt-5">
          {view === "configured" ? (
            <ConfiguredSkills onSelect={onItemChange} />
          ) : (
            <LearnedSkills onSelect={onItemChange} selectedId={item} />
          )}
        </div>
      </div>
    </div>
  );
}

function ConfiguredSkills({ onSelect }: { onSelect: (name: string) => void }) {
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
  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">
        Configured skills are read-only here and managed by the Mecatl deployment.
      </p>
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
          <div className="hidden overflow-hidden rounded-xl border bg-card md:block">
            <Table>
              <TableHeader className="text-xs text-muted-foreground">
                <TableRow>
                  {(
                    [
                      ["name", "Skill"],
                      ["version", "Version"],
                      ["owner", "Owner"],
                    ] as const
                  ).map(([key, label]) => (
                    <SortableHead
                      className="px-4"
                      direction={sort.key === key ? sort.direction : undefined}
                      key={key}
                      label={label}
                      onSort={() => toggle(key)}
                    />
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((skill) => (
                  <TableRow key={skill.name}>
                    <TableCell className="whitespace-normal px-4 py-3">
                      <button
                        className="text-left font-medium"
                        onClick={() => onSelect(skill.name)}
                        type="button"
                      >
                        {humanize(skill.name)}
                      </button>
                      <span className="ml-2 font-mono text-xs text-muted-foreground">
                        {skill.name}
                      </span>
                      {skill.agentOwned && (
                        <Badge className="ml-2" variant="info">
                          agent-owned
                        </Badge>
                      )}
                      <p className="mt-1 line-clamp-1 text-muted-foreground">
                        {skill.description || "No description recorded."}
                      </p>
                    </TableCell>
                    <TableCell className="px-4 py-3 font-mono text-xs">
                      {skill.activeVersion || "unversioned"}
                    </TableCell>
                    <TableCell className="px-4 py-3 text-xs text-muted-foreground">
                      {skill.ownerAgent || "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <ul className="divide-y overflow-hidden rounded-xl border bg-card md:hidden">
            {rows.map((skill) => (
              <li key={skill.name}>
                <button
                  className="block w-full p-4 text-left"
                  onClick={() => onSelect(skill.name)}
                  type="button"
                >
                  <span className="flex items-center gap-2 font-medium">
                    {humanize(skill.name)}
                    {skill.agentOwned && <Badge variant="info">agent-owned</Badge>}
                  </span>
                  <span className="mt-1 line-clamp-2 block text-sm text-muted-foreground">
                    {skill.description || "No description recorded."}
                  </span>
                  <span className="mt-2 block font-mono text-xs text-muted-foreground">
                    {skill.activeVersion || "unversioned"}
                    {skill.ownerAgent && ` · ${skill.ownerAgent}`}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}

function LearnedSkills({
  onSelect,
  selectedId,
}: {
  onSelect: (id?: string) => void;
  selectedId?: string;
}) {
  const queryClient = useQueryClient();
  const query = useQuery(listLearnedSkillsOptions());
  const mutation = useMutation(actOnLearnedSkillMutation());
  const [error, setError] = useState<string>();
  const [pendingAction, setPendingAction] = useState<{
    action: LearnedSkillAction;
    skill: LearnedSkill;
  }>();
  const selected = query.data?.items.find((skill) => skill.id === selectedId);

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
      await queryClient.invalidateQueries({ queryKey: listLearnedSkillsQueryKey() });
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setPendingAction(undefined);
    }
  }

  if (query.isPending) return <StateCard text="Loading learned skills…" />;
  if (query.isError) return <StateCard error text={errorMessage(query.error)} />;
  if (!query.data.supported)
    return <StateCard text={query.data.reason} title="Learned skills are unavailable" />;
  if (query.data.items.length === 0)
    return (
      <StateCard
        icon="sparkles"
        text="Procedures drafted from reflection will appear here for review before reaching the live inventory."
        title="No learned skills yet"
      />
    );
  return (
    <>
      {error && (
        <p className="mb-3 rounded-lg bg-destructive/10 p-3 text-sm text-foreground">{error}</p>
      )}
      {!query.data.complete && (
        <p className="mb-3 text-xs text-warning">Showing the first 100 learned skills.</p>
      )}
      <div className="divide-y overflow-hidden rounded-xl border bg-card">
        {query.data.items.map((skill) => (
          <button
            className="flex w-full items-start gap-3 p-4 text-left hover:bg-muted/40"
            key={`${skill.id}-${skill.version}`}
            onClick={() => onSelect(skill.id)}
            type="button"
          >
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{skill.name}</span>
                <span className="font-mono text-xs text-muted-foreground">{skill.version}</span>
                <SkillState state={skill.state} />
              </span>
              <span className="mt-1 line-clamp-2 block text-sm text-muted-foreground">
                {skill.description || "No description recorded."}
              </span>
            </span>
            <span className="shrink-0 text-xs text-muted-foreground">{skill.ownerAgent}</span>
          </button>
        ))}
      </div>
      {selected && (
        <LearnedSkillDialog
          busy={mutation.isPending}
          onAction={(action) => setPendingAction({ action, skill: selected })}
          onClose={() => onSelect(undefined)}
          skill={selected}
        />
      )}
      <AlertDialog
        onOpenChange={(open) => !open && setPendingAction(undefined)}
        open={Boolean(pendingAction)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingAction &&
                `${actionLabel(pendingAction.action)} ${pendingAction.skill.name} ${pendingAction.skill.version}?`}
            </AlertDialogTitle>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={mutation.isPending}
              onClick={() => pendingAction && void act(pendingAction.skill, pendingAction.action)}
            >
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function LearnedSkillDialog({
  busy,
  onAction,
  onClose,
  skill,
}: {
  busy: boolean;
  onAction: (action: "activate" | "archive" | "reject" | "rollback") => void;
  onClose: () => void;
  skill: LearnedSkill;
}) {
  return (
    <Modal onClose={onClose}>
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">{skill.name}</h2>
        <span className="font-mono text-sm text-muted-foreground">{skill.version}</span>
        <SkillState state={skill.state} />
      </div>
      <p className="mt-2 text-sm text-muted-foreground">
        {skill.description || "No description recorded."}
      </p>
      {skill.body && (
        <pre className="mt-4 max-h-80 overflow-auto whitespace-pre-wrap rounded-lg bg-muted p-4 font-mono text-xs leading-5">
          {skill.body}
        </pre>
      )}
      <p className="mt-3 text-xs text-muted-foreground">
        {skill.evidenceCount} evidence reference{skill.evidenceCount === 1 ? "" : "s"}
        {skill.updatedAt && ` · updated ${formatDate(skill.updatedAt)}`}
      </p>
      <div className="mt-5 flex flex-wrap justify-end gap-2">
        {skill.actions.reject && (
          <Button disabled={busy} onClick={() => onAction("reject")} variant="outline">
            Reject
          </Button>
        )}
        {skill.actions.activate && (
          <Button disabled={busy} onClick={() => onAction("activate")} variant="action">
            Activate
          </Button>
        )}
        {skill.actions.rollback && (
          <Button disabled={busy} onClick={() => onAction("rollback")} variant="outline">
            Roll back to {skill.supersedes}
          </Button>
        )}
        {skill.actions.archive && (
          <Button disabled={busy} onClick={() => onAction("archive")} variant="outline">
            Archive
          </Button>
        )}
      </div>
    </Modal>
  );
}

export function Modal({ children, onClose }: { children: React.ReactNode; onClose: () => void }) {
  return (
    <Dialog onOpenChange={(open) => !open && onClose()} open>
      <DialogContent className="max-h-[85dvh] max-w-2xl overflow-y-auto">{children}</DialogContent>
    </Dialog>
  );
}

function SkillState({ state }: { state: string }) {
  const variant =
    state === "active"
      ? "success"
      : state === "rejected"
        ? "destructive"
        : state === "staged"
          ? "warning"
          : "muted";
  return <Badge variant={variant}>{state || "unknown"}</Badge>;
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

function actionLabel(action: string) {
  return action.charAt(0).toUpperCase() + action.slice(1);
}
function humanize(value: string) {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
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
