"use client";

import { CalendarClock, Ellipsis } from "lucide-react";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAgentCron } from "@/features/agent";
import { describeCron, formatUntilTime } from "@/lib/formatters";
import type { ScheduleRow } from "@/lib/protocol";
import { pageTitleClass } from "@/lib/typography";
import { cn } from "@/lib/utils";
import { CreateScheduleDialog } from "./_components/create-schedule-dialog";
import { EditScheduleDialog } from "./_components/edit-schedule-dialog";
import {
  ScheduleStatusDot,
  scheduleStatusOf,
} from "./_components/schedule-badges";

/** Fixed order: live fires first (they need eyes), then future fires, then paused. */
const STATUS_RANK: Record<string, number> = {
  Running: 0,
  Claimed: 1,
  Scheduled: 2,
  Paused: 3,
};

const FILTERS = [
  { value: "all", label: "All" },
  { value: "active", label: "Scheduled" },
  { value: "paused", label: "Paused" },
] as const;
type FilterValue = (typeof FILTERS)[number]["value"];

/** Active is everything that can still fire: running, claimed, or scheduled. */
function matchesFilter(row: ScheduleRow, filter: FilterValue): boolean {
  if (filter === "all") return true;
  const paused = scheduleStatusOf(row).label === "Paused";
  return filter === "paused" ? paused : !paused;
}

const statusRank = (r: ScheduleRow) =>
  STATUS_RANK[scheduleStatusOf(r).label] ?? 9;

/** One line for the trigger: cron in plain English, or the one-shot instant. */
function describeTrigger(row: ScheduleRow): string {
  if (row.cron) return describeCron(row.cron);
  if (row.oneShotAt !== null)
    return `Once at ${new Date(row.oneShotAt).toLocaleString()}`;
  return "—";
}

export default function WorkspaceSchedulesPage() {
  const cron = useAgentCron();
  const [filter, setFilter] = useState<FilterValue>("all");
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const [editRow, setEditRow] = useState<ScheduleRow | null>(null);

  const sort = useTableSort<"name" | "schedule" | "nextRun" | "status">(
    "status",
  );

  const rows = useMemo(() => {
    // Paused/never-firing rows sort after everything with a real next fire.
    const nextAt = (r: ScheduleRow) =>
      r.enabled && r.nextFireAt !== null
        ? r.nextFireAt
        : Number.MAX_SAFE_INTEGER;
    const primary = (a: ScheduleRow, b: ScheduleRow) => {
      switch (sort.key) {
        case "name":
          return a.name.localeCompare(b.name);
        case "schedule":
          return describeTrigger(a).localeCompare(describeTrigger(b));
        case "nextRun":
          return nextAt(a) - nextAt(b);
        default:
          return statusRank(a) - statusRank(b);
      }
    };
    return cron.rows
      .filter((row) => matchesFilter(row, filter))
      .sort(
        (a, b) =>
          directed(sort.dir, primary(a, b)) || a.name.localeCompare(b.name),
      );
  }, [cron.rows, filter, sort.key, sort.dir]);

  // The mobile list has no sortable headers: fixed status-rank-then-name
  // order (the desktop default), whatever the desktop sort state says.
  const mobileRows = useMemo(
    () =>
      cron.rows
        .filter((row) => matchesFilter(row, filter))
        .sort(
          (a, b) =>
            statusRank(a) - statusRank(b) || a.name.localeCompare(b.name),
        ),
    [cron.rows, filter],
  );

  /** Kebab actions surface refusals via cron.error (rendered above the grid). */
  const act = (action: Promise<void>) => {
    void action.catch(() => {});
  };

  return (
    <div className="h-full overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
      <div className="space-y-5">
        <div className="flex items-center justify-between gap-4">
          <h1
            className={pageTitleClass("truncate pb-0 text-3xl leading-tight")}
          >
            Scheduled
          </h1>
          {/* No create button while the daemon has no scheduler to accept it. */}
          {cron.isSupported && cron.harnessLive && (
            <CreateScheduleDialog createFromDraft={cron.createFromDraft} />
          )}
        </div>

        {/* Action refusals from the daemon, verbatim — its words are the
            explanation (frequency floor, bad cron, 412 conflicts). */}
        {cron.error && (
          <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive">
            {cron.error}
          </p>
        )}

        {!cron.harnessLive ? (
          <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
            Runtime offline — the schedule registry can&rsquo;t be read.
          </div>
        ) : !cron.isSupported ? (
          // Not-wired is a different fact from empty: the daemon answered the
          // list with a refusal because it has no schedule store at all.
          <div className="space-y-2 rounded-lg border border-dashed px-6 py-12 text-center">
            <p className="text-sm font-medium">
              Scheduling is not wired on this deployment.
            </p>
            <p className="font-mono text-xs text-muted-foreground">
              {cron.notWired}
            </p>
          </div>
        ) : cron.isLoading ? (
          <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
            Loading schedules…
          </div>
        ) : cron.rows.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed py-16 text-center">
            <div className="flex size-11 items-center justify-center rounded-full bg-muted">
              <CalendarClock className="size-5 text-muted-foreground" />
            </div>
            <div className="space-y-1">
              <p className="text-sm font-medium">Nothing scheduled yet</p>
              <p className="max-w-sm text-sm text-muted-foreground">
                Set up a recurring or one-off task and the agent will run it
                unattended — on a cron, or once at a chosen time.
              </p>
            </div>
            {cron.isSupported && cron.harnessLive && (
              <CreateScheduleDialog createFromDraft={cron.createFromDraft} />
            )}
          </div>
        ) : (
          <>
            {/* Segmented control: a filled track with the active pill lifted. */}
            <div className="inline-flex items-center gap-0.5 rounded-full bg-muted p-1">
              {FILTERS.map((f) => (
                <button
                  key={f.value}
                  type="button"
                  onClick={() => setFilter(f.value)}
                  className={cn(
                    "h-7 rounded-full px-3.5 text-sm transition-colors",
                    filter === f.value
                      ? "bg-background font-medium text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {f.label}
                </button>
              ))}
            </div>

            {rows.length === 0 ? (
              <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
                No scheduled tasks match this filter.
              </div>
            ) : (
              <>
                {/* Mobile list (<500px): one cell per row — status dot, name,
                    prompt clamped below; no header, no kebab (actions live on
                    the detail page the row navigates to). Fixed
                    status-rank-then-name order. */}
                <div className="divide-y overflow-hidden rounded-lg border min-[500px]:hidden">
                  {mobileRows.map((row) => (
                    <Link
                      key={row.name}
                      href={`/workspace/schedules/${encodeURIComponent(row.name)}`}
                      className="flex items-start gap-3 px-4 py-3"
                    >
                      <span className="mt-1.5 flex shrink-0">
                        <ScheduleStatusDot row={row} />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium">
                          {row.name}
                        </span>
                        <span className="line-clamp-2 text-xs text-muted-foreground">
                          {row.prompt}
                        </span>
                      </span>
                    </Link>
                  ))}
                </div>

                <div className="overflow-hidden rounded-lg border max-[499px]:hidden">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <SortableHead
                          label="Name"
                          sortKey="name"
                          sort={sort}
                          className="w-1/2"
                        />
                        <SortableHead
                          label="Schedule"
                          sortKey="schedule"
                          sort={sort}
                        />
                        <SortableHead
                          label="Next run"
                          sortKey="nextRun"
                          sort={sort}
                        />
                        <SortableHead
                          label="Status"
                          sortKey="status"
                          sort={sort}
                        />
                        <TableHead className="w-0" aria-label="Actions" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {rows.map((row) => (
                        <ScheduleRowItem
                          key={row.name}
                          row={row}
                          onFire={() => act(cron.runJob(row.name))}
                          onToggle={() =>
                            act(
                              row.enabled
                                ? cron.pauseJob(row.name)
                                : cron.resumeJob(row.name),
                            )
                          }
                          onEdit={() => setEditRow(row)}
                          onDelete={() => setConfirmDelete(row.name)}
                        />
                      ))}
                    </TableBody>
                  </Table>
                </div>
              </>
            )}
          </>
        )}
      </div>

      {editRow && (
        <EditScheduleDialog
          row={editRow}
          updateFromDraft={cron.updateFromDraft}
          renameAndUpdateFromDraft={cron.renameAndUpdateFromDraft}
          /* The list re-reads on every action, so a rename needs no navigation
             here — the row simply reappears under its new name. */
          onRenamed={() => setEditRow(null)}
          onClose={() => setEditRow(null)}
        />
      )}

      <AlertDialog
        open={confirmDelete !== null}
        onOpenChange={(open) => !open && setConfirmDelete(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {confirmDelete}?</AlertDialogTitle>
            <AlertDialogDescription>
              The schedule stops firing. Past run transcripts stay in the
              session store.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (confirmDelete) act(cron.deleteJob(confirmDelete));
                setConfirmDelete(null);
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/**
 * One schedule as a plain table row. The name is the navigation affordance (a
 * real anchor); the kebab holds the quick actions so nothing but the detail
 * page is ever more than one click away.
 */
function ScheduleRowItem({
  row,
  onFire,
  onToggle,
  onEdit,
  onDelete,
}: {
  row: ScheduleRow;
  onFire: () => void;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const router = useRouter();
  const href = `/workspace/schedules/${encodeURIComponent(row.name)}`;
  const nextIn =
    row.enabled && row.nextFireAt !== null
      ? formatUntilTime(row.nextFireAt)
      : "";

  return (
    <TableRow className="cursor-pointer" onClick={() => router.push(href)}>
      <TableCell className="max-w-0">
        <Link
          href={href}
          onClick={(e) => e.stopPropagation()}
          className="block truncate font-medium hover:underline"
        >
          {row.name}
        </Link>
        <p className="line-clamp-1 text-xs text-muted-foreground">
          {row.prompt}
        </p>
      </TableCell>
      <TableCell
        suppressHydrationWarning
        className="text-muted-foreground"
        title={
          row.cron
            ? `${row.cron}${row.timezone ? ` (${row.timezone})` : ""}`
            : undefined
        }
      >
        {describeTrigger(row)}
      </TableCell>
      <TableCell
        suppressHydrationWarning
        className="text-muted-foreground tabular-nums"
      >
        {nextIn || "—"}
      </TableCell>
      <TableCell>
        <span className="inline-flex items-center gap-2">
          <ScheduleStatusDot row={row} />
          <span className="text-muted-foreground">
            {scheduleStatusOf(row).label}
          </span>
        </span>
      </TableCell>
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
            <DropdownMenuItem onClick={onFire}>Run now</DropdownMenuItem>
            <DropdownMenuItem onClick={onToggle}>
              {row.enabled ? "Pause" : "Resume"}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={onEdit}>Edit</DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onClick={onDelete}>
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  );
}
