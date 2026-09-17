"use client";

import { Ellipsis, Sparkles } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import {
  directed,
  SortableHead,
  useTableSort,
} from "@/components/sortable-head";
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
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAgentSkills } from "@/features/agent/hooks/use-agent-skills";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import type { HarnessSkillInfo } from "@/lib/harness/client";
import { pageTitleClass } from "@/lib/typography";
import { cn } from "@/lib/utils";
import { CreateSkillDialog } from "./_components/create-skill-dialog";
import { EditSkillDialog } from "./_components/edit-skill-dialog";
import { LearnedSkillsPanel } from "./_components/learned-skills-panel";
import { SkillToolDisabledBanner } from "./_components/skill-tool-disabled-banner";
import { SkillsViewQueryWatcher } from "./_components/view-query-watcher";

/** "pr-feedback" → "Pr Feedback"; the raw slug stays the id/route param. */
function humanizeSkillName(name: string): string {
  return name
    .split(/[-_]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/** Why a management action is unavailable in external mode. */
const externalManagedTitle = "Managed by the external mecated deployment";

/** One merged row: the daemon's inventory plus the controller's parked skills. */
interface SkillListRow extends HarnessSkillInfo {
  enabled: boolean;
}

/**
 * State filter over the merged list (the schedules page's segmented-control
 * idiom): Enabled is the daemon-visible inventory, Disabled the controller's
 * parked list. In external mode the disabled list is simply empty — the pills
 * still render and Disabled shows the empty state, no special casing.
 */
const SKILL_FILTERS = [
  { value: "all", label: "All" },
  { value: "enabled", label: "Enabled" },
  { value: "disabled", label: "Disabled" },
] as const;
/** "learned" is a separate daemon-owned inventory (ADR 0110), not a filter
 *  over the workspace rows — its pill renders only when the daemon reports
 *  `capabilities.learned_skills`. */
type SkillFilterValue = (typeof SKILL_FILTERS)[number]["value"] | "learned";

function matchesSkillFilter(
  row: SkillListRow,
  filter: SkillFilterValue,
): boolean {
  if (filter === "all") return true;
  if (filter === "learned") return false; // learned renders its own panel
  return filter === "enabled" ? row.enabled : !row.enabled;
}

const byHumanName = (a: SkillListRow, b: SkillListRow) =>
  humanizeSkillName(a.name).localeCompare(humanizeSkillName(b.name));

/** A pending confirm: enable/disable and delete both warn before a write that
 *  restarts the daemon (the surfaces-warn-before-restart rule). */
type PendingAction = { kind: "toggle" | "delete"; row: SkillListRow };

function confirmCopy(pending: PendingAction): {
  title: string;
  description: string;
  actionLabel: string;
} {
  const { kind, row } = pending;
  if (kind === "delete") {
    return {
      title: `Delete ${row.name}?`,
      description: row.enabled
        ? "The skill's folder (and any bundled assets) is removed from the workspace, and the daemon restarts — in-flight runs and session ids die with it."
        : "The skill's folder (and any bundled assets) is removed from the workspace. It is already disabled, so the daemon keeps running.",
      actionLabel: "Delete",
    };
  }
  return row.enabled
    ? {
        title: `Disable ${row.name}?`,
        description:
          "The skill moves out of the daemon's sight and the daemon restarts — in-flight runs and session ids die with it. Enable it again any time.",
        actionLabel: "Disable",
      }
    : {
        title: `Enable ${row.name}?`,
        description:
          "The skill moves back into the daemon's skills directory and the daemon restarts — in-flight runs and session ids die with it.",
        actionLabel: "Enable",
      };
}

export default function WorkspaceSkillsPage() {
  const router = useRouter();
  const {
    skills,
    disabled,
    manageable,
    isLoading,
    error,
    actionError,
    create,
    createFiles,
    fetchBody,
    saveBody,
    setEnabled,
    remove,
  } = useAgentSkills();
  const { serverCapabilities } = useRuntimeStatus();
  const learnedSupported = serverCapabilities.learned_skills === true;
  const sort = useTableSort<"name" | "description" | "status">("name");
  const [filter, setFilter] = useState<SkillFilterValue>("all");
  const [pending, setPending] = useState<PendingAction | null>(null);
  const [editing, setEditing] = useState<SkillListRow | null>(null);

  // A daemon restart can drop the capability mid-session; never strand the
  // view on a pill that no longer renders.
  const activeFilter: SkillFilterValue =
    filter === "learned" && !learnedSupported ? "all" : filter;
  const filterPills: { value: SkillFilterValue; label: string }[] = [
    ...SKILL_FILTERS,
    ...(learnedSupported
      ? [{ value: "learned" as const, label: "Learned" }]
      : []),
  ];

  const rows = useMemo<SkillListRow[]>(
    () => [
      ...skills.map((skill) => ({ ...skill, enabled: true })),
      ...disabled.map((skill) => ({
        name: skill.name,
        description: skill.description,
        agentOwned: false,
        ownerAgent: "",
        activeVersion: "",
        enabled: false,
      })),
    ],
    [skills, disabled],
  );

  const filtered = useMemo(
    () => rows.filter((row) => matchesSkillFilter(row, activeFilter)),
    [rows, activeFilter],
  );

  const sorted = useMemo(() => {
    const primary = (a: SkillListRow, b: SkillListRow) => {
      switch (sort.key) {
        case "description":
          return a.description.localeCompare(b.description);
        case "status":
          return Number(b.enabled) - Number(a.enabled);
        default:
          return byHumanName(a, b);
      }
    };
    // Name stays the direction-independent tiebreak so equal rows are stable.
    return [...filtered].sort(
      (a, b) => directed(sort.dir, primary(a, b)) || byHumanName(a, b),
    );
  }, [filtered, sort.key, sort.dir]);

  // The mobile list has no sortable headers: fixed alphabetical order by
  // humanized name, whatever the desktop sort state says.
  const mobileRows = useMemo(() => [...filtered].sort(byHumanName), [filtered]);

  /** Refusals surface via actionError (rendered above the table). */
  const act = (action: Promise<void>) => {
    void action.catch(() => {});
  };

  // Shared between the no-skills-at-all case and the workspace tabs of a
  // daemon that still has a learned inventory to show.
  const emptyState = (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <div className="flex size-11 items-center justify-center rounded-full bg-muted">
        <Sparkles className="size-5 text-muted-foreground" />
      </div>
      <div className="space-y-1">
        <p className="text-sm font-medium">No skills here yet</p>
        <p className="max-w-sm text-sm text-muted-foreground">
          Drop a <code className="font-mono text-xs">SKILL.md</code> into{" "}
          <code className="font-mono text-xs">.mecatl/skills</code> and it'll
          show up here, ready for the agent to use.
        </p>
      </div>
    </div>
  );

  return (
    <div className="h-full overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
      {/* `/workspace/skills?view=learned` (the learning queue's "View learned
          skill" link) lands on the Learned tab. */}
      <SkillsViewQueryWatcher
        onView={(view) => {
          if (view === "learned") setFilter("learned");
        }}
      />
      <div className="space-y-5">
        {/* The daemon's own `capabilities.skills === false`: the Skill tool
            is off, so nothing listed here reaches the agent until it is on. */}
        <SkillToolDisabledBanner />
        <div className="flex items-center justify-between gap-4">
          <h1
            className={pageTitleClass("truncate pb-0 text-3xl leading-tight")}
          >
            Skills
          </h1>
          {/* Authoring is controller-owned: external mode has no create at
              all, matching how the row actions degrade there. */}
          {manageable && (
            <CreateSkillDialog
              create={create}
              createFiles={createFiles}
              onCreated={(name) =>
                router.push(`/workspace/skills/${encodeURIComponent(name)}`)
              }
            />
          )}
        </div>

        {/* Management refusals from the controller, verbatim — its words are
            the explanation (name collisions, a failed daemon restart). */}
        {actionError && (
          <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive">
            {actionError}
          </p>
        )}

        {isLoading ? (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
            {["a", "b", "c"].map((key) => (
              <Skeleton key={key} className="h-28 rounded-lg" />
            ))}
          </div>
        ) : error ? (
          <div className="rounded-lg border border-dashed border-destructive/40 py-12 text-center text-sm text-destructive">
            {error}
          </div>
        ) : rows.length === 0 && !learnedSupported ? (
          emptyState
        ) : (
          <>
            {/* Segmented control: a filled track with the active pill lifted
                (the schedules page's idiom). */}
            <div className="inline-flex items-center gap-0.5 rounded-full bg-muted p-1">
              {filterPills.map((f) => (
                <button
                  key={f.value}
                  type="button"
                  onClick={() => setFilter(f.value)}
                  className={cn(
                    "h-7 rounded-full px-3.5 text-sm transition-colors",
                    activeFilter === f.value
                      ? "bg-background font-medium text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {f.label}
                </button>
              ))}
            </div>

            {activeFilter === "learned" ? (
              <LearnedSkillsPanel />
            ) : rows.length === 0 ? (
              emptyState
            ) : filtered.length === 0 ? (
              <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
                No skills match this filter.
              </div>
            ) : (
              <>
                {/* Desktop table card, matching the admin tables (the
                    schedules page's pre-card idiom): rounded border shell,
                    Name bounded so Description sits next to it instead of
                    across a dead gap, max-w-0 truncation on the cells. */}
                <div className="overflow-hidden rounded-lg border max-[499px]:hidden">
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <SortableHead
                          label="Name"
                          sortKey="name"
                          sort={sort}
                          className="w-px whitespace-nowrap"
                        />
                        <SortableHead
                          label="Description"
                          sortKey="description"
                          sort={sort}
                        />
                        {manageable && (
                          <SortableHead
                            label="Status"
                            sortKey="status"
                            sort={sort}
                            className="w-px whitespace-nowrap"
                          />
                        )}
                        <TableHead className="w-0" aria-label="Actions" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {sorted.map((row) => (
                        <SkillTableRow
                          key={`${row.name}-${row.enabled ? "on" : "off"}`}
                          row={row}
                          manageable={manageable}
                          onEdit={() => setEditing(row)}
                          onToggle={() => setPending({ kind: "toggle", row })}
                          onDelete={() => setPending({ kind: "delete", row })}
                        />
                      ))}
                    </TableBody>
                  </Table>
                </div>

                {/* Mobile list (<500px): one cell per row — status dot, the
                    humanized name, description clamped below; no slug, no
                    header, no kebab (actions live on the detail page the row
                    navigates to). Fixed alphabetical order. */}
                <div className="divide-y overflow-hidden rounded-lg border min-[500px]:hidden">
                  {mobileRows.map((row) => (
                    <Link
                      key={`${row.name}-${row.enabled ? "on" : "off"}`}
                      href={`/workspace/skills/${encodeURIComponent(row.name)}`}
                      className={cn(
                        "flex items-start gap-3 px-4 py-3",
                        !row.enabled && "opacity-60",
                      )}
                    >
                      <span
                        aria-hidden="true"
                        className={cn(
                          "mt-1.5 size-1.5 shrink-0 rounded-full",
                          row.enabled
                            ? "bg-emerald-500"
                            : "bg-muted-foreground/50",
                        )}
                      />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium">
                          {humanizeSkillName(row.name)}
                        </span>
                        <span className="line-clamp-2 text-xs text-muted-foreground">
                          {row.description}
                        </span>
                      </span>
                    </Link>
                  ))}
                </div>
              </>
            )}
          </>
        )}
      </div>

      {editing && (
        <EditSkillDialog
          name={editing.name}
          enabled={editing.enabled}
          fetchBody={fetchBody}
          saveBody={saveBody}
          onClose={() => setEditing(null)}
        />
      )}

      <AlertDialog
        open={pending !== null}
        onOpenChange={(open) => !open && setPending(null)}
      >
        {pending && (
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{confirmCopy(pending).title}</AlertDialogTitle>
              <AlertDialogDescription>
                {confirmCopy(pending).description}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  act(
                    pending.kind === "delete"
                      ? remove(pending.row.name)
                      : setEnabled(pending.row.name, !pending.row.enabled),
                  );
                  setPending(null);
                }}
              >
                {confirmCopy(pending).actionLabel}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        )}
      </AlertDialog>
    </div>
  );
}

/**
 * One skill row: humanized name (a real anchor to the detail page) over the
 * raw slug, the one-line summary the model sees, the managed-mode status, and
 * the actions kebab. The whole row navigates on click; the name link keeps a
 * genuine href for middle-click/new-tab. Disabled skills render muted — they
 * exist in the workspace but the daemon cannot see them.
 */
function SkillTableRow({
  row,
  manageable,
  onEdit,
  onToggle,
  onDelete,
}: {
  row: SkillListRow;
  manageable: boolean;
  onEdit: () => void;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const router = useRouter();
  const href = `/workspace/skills/${encodeURIComponent(row.name)}`;

  return (
    <TableRow
      className={cn("cursor-pointer", !row.enabled && "opacity-60")}
      onClick={() => router.push(href)}
    >
      <TableCell className="w-px max-w-[360px] whitespace-nowrap pr-6">
        <Link
          href={href}
          className="block truncate text-sm font-medium hover:underline"
          onClick={(e) => e.stopPropagation()}
        >
          {humanizeSkillName(row.name)}
        </Link>
        <p className="truncate font-mono text-xs text-muted-foreground">
          {row.name}
        </p>
      </TableCell>
      <TableCell className="w-full max-w-0">
        <p className="line-clamp-1 whitespace-normal text-xs text-muted-foreground">
          {row.description}
        </p>
      </TableCell>
      {manageable && (
        <TableCell className="whitespace-nowrap">
          <span className="inline-flex items-center gap-2">
            <span
              aria-hidden="true"
              className={cn(
                "size-1.5 rounded-full",
                row.enabled ? "bg-emerald-500" : "bg-muted-foreground/50",
              )}
            />
            <span className="text-muted-foreground">
              {row.enabled ? "Active" : "Disabled"}
            </span>
          </span>
        </TableCell>
      )}
      {/* Actions must not trigger the row navigation. */}
      <TableCell className="w-0 pr-2" onClick={(e) => e.stopPropagation()}>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="size-8"
              aria-label={`Actions for ${row.name}`}
            >
              <Ellipsis className="size-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              disabled={!manageable}
              title={manageable ? undefined : externalManagedTitle}
              onClick={onEdit}
            >
              Edit
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={!manageable}
              title={manageable ? undefined : externalManagedTitle}
              onClick={onToggle}
            >
              {row.enabled ? "Disable" : "Enable"}
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              disabled={!manageable}
              title={manageable ? undefined : externalManagedTitle}
              onClick={onDelete}
            >
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  );
}
