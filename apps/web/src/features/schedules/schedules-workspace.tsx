// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import {
  actOnScheduleMutation,
  createScheduleMutation,
  deleteScheduleMutation,
  listSchedulesOptions,
  listSchedulesQueryKey,
  updateScheduleMutation,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CalendarClock, Ellipsis, Plus } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { SortableHead, type TableSort, useTableSort } from "@/components/sortable-head";
import { PageShell } from "../../components/shell/page-shell";
import { Alert, AlertDescription } from "../../components/ui/alert";
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
import { Button, buttonVariants } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "../../components/ui/empty";
import { InputSearch } from "../../components/ui/input-search";
import { Skeleton } from "../../components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../components/ui/table";
import { ToggleGroup, ToggleGroupItem } from "../../components/ui/toggle-group";
import { errorMessage } from "../../lib/error-message";
import { formatDate } from "../../lib/formatters";
import { pageTitleClass } from "../../lib/typography";
import { useShortcut } from "../shortcuts/shortcut-provider";
import { canRunNow, canTogglePause } from "./schedule-detail";
import { canEditSchedule, ScheduleForm } from "./schedule-form";
import {
  deriveMobileScheduleInventory,
  deriveScheduleInventory,
  type ScheduleInventoryOrder,
  scheduleTriggerLabel,
} from "./schedule-inventory";
import { ScheduleStatusBadge, ScheduleStatusDot } from "./schedule-status";

type Schedule = ListSchedulesResponse["items"][number];
type Filter = "all" | "paused" | "scheduled";

export const scheduleInventoryPolling = {
  refetchInterval: 5_000,
  refetchIntervalInBackground: false,
} as const;

/**
 * TERM: The schedule inventory is the caller-visible set returned by the
 * authenticated BFF. Avoid: scheduler queue, which is runtime work and includes
 * facts a Studio user may not be authorized to inspect.
 *
 * SPEC: At the agreed 500px pivot the inventory changes between the frozen
 * sortable desktop table and a compact mobile list; neither presentation uses
 * horizontal page scrolling. Desktop columns are Name, Schedule, Next run,
 * Last run, Runs, Status, and Actions. Mobile rows show status, name, prompt,
 * run count, and last run, then navigate to detail for actions.
 *
 * SPEC: Local search covers name, prompt, human trigger, and raw cron. `/`
 * focuses it; Escape clears a non-empty query, then blurs an empty field.
 * Search-empty and status-filter-empty states remain distinct.
 *
 * DECISION: The desktop inventory and detail run log reuse #1853's merged
 * stock `Table` primitives and stateless `SortableHead`; schedules do not copy
 * table code from SEP or maintain another table primitive. History is
 * detail-only in the frozen design.
 *
 * DECISION: Filter and sort state remain local, matching both prototype PR #45
 * and #1853's configured-skills usage. The shared primitives standardize table
 * markup and accessibility but intentionally own no state or URL persistence.
 * Browser history therefore carries schedule navigation, not transient list
 * controls or potentially sensitive prompt search text.
 *
 * DECISION: Scheduled contains effective Running, Claimed, and Scheduled
 * statuses; Paused contains only effective Paused. Completed appears in All.
 * A disabled schedule that is firing remains Scheduled because the BFF's
 * effective status is Running.
 */
export function SchedulesWorkspace() {
  const queryClient = useQueryClient();
  const schedules = useQuery({
    ...listSchedulesOptions(),
    ...scheduleInventoryPolling,
    // DECISION: Match the existing visibility-gated auto-refresh pattern: poll
    // every five seconds while this page is mounted, but never in a background
    // tab. Query refresh preserves local controls and scroll without the router
    // refresh restoration needed by the Next.js implementation.
  });
  const create = useMutation(createScheduleMutation());
  const update = useMutation(updateScheduleMutation());
  const remove = useMutation(deleteScheduleMutation());
  const action = useMutation(actOnScheduleMutation());
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");
  const sort = useTableSort<ScheduleInventoryOrder>("status");
  const [editor, setEditor] = useState<Schedule | "new">();
  const [confirmDelete, setConfirmDelete] = useState<Schedule>();
  const [error, setError] = useState<string>();
  const editorOpener = useRef<HTMLElement | null>(null);
  const deleteOpener = useRef<HTMLButtonElement | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  useShortcut("schedules.filter", () => searchRef.current?.focus());

  const filtered = useMemo(
    () =>
      (schedules.data?.items ?? []).filter((item) =>
        filter === "all"
          ? true
          : filter === "paused"
            ? item.status === "paused"
            : item.status === "running" || item.status === "claimed" || item.status === "scheduled",
      ),
    [filter, schedules.data?.items],
  );
  const items = useMemo(
    () => deriveScheduleInventory(filtered, query, sort.key, sort.dir),
    [filtered, query, sort.dir, sort.key],
  );
  const mobileItems = useMemo(
    () => deriveMobileScheduleInventory(filtered, query),
    [filtered, query],
  );

  function openEditor(next: Schedule | "new") {
    editorOpener.current = document.activeElement as HTMLElement | null;
    setEditor(next);
  }

  function closeEditor() {
    setEditor(undefined);
    queueMicrotask(() => editorOpener.current?.focus());
  }

  function openDelete(schedule: Schedule, opener: HTMLButtonElement) {
    deleteOpener.current = opener;
    setConfirmDelete(schedule);
  }

  function closeDelete() {
    setConfirmDelete(undefined);
    queueMicrotask(() => deleteOpener.current?.focus());
  }

  function onSearchKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    if (query) setQuery("");
    else event.currentTarget.blur();
  }

  async function refresh() {
    await queryClient.invalidateQueries({ queryKey: listSchedulesQueryKey() });
  }

  async function runAction(schedule: Schedule, name: "fire" | "pause" | "resume") {
    setError(undefined);
    try {
      await action.mutateAsync({ body: { action: name }, path: { scheduleName: schedule.name } });
      await refresh();
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  async function deleteItem(schedule: Schedule) {
    setError(undefined);
    try {
      await remove.mutateAsync({ path: { scheduleName: schedule.name } });
      await refresh();
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setConfirmDelete(undefined);
    }
  }

  return (
    <PageShell>
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className={pageTitleClass()}>Scheduled</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            Recurring and one-off work run by the connected Mecatl instance.
          </p>
        </div>
        {schedules.data?.supported && (
          <Button onClick={() => openEditor("new")} variant="action">
            <Plus />
            Schedule task
          </Button>
        )}
      </div>

      {error && (
        <Alert className="mt-5" variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {schedules.isPending ? (
        <LoadingState />
      ) : schedules.isError ? (
        <Alert className="mt-8" variant="destructive">
          <AlertDescription>{errorMessage(schedules.error)}</AlertDescription>
        </Alert>
      ) : !schedules.data.supported ? (
        <EmptyState
          text={schedules.data.reason}
          title="Scheduling is not wired on this deployment"
        />
      ) : schedules.data.items.length === 0 ? (
        <EmptyState
          action={() => openEditor("new")}
          text="Set up recurring or one-off work and Mecatl will run it unattended."
          title="Nothing scheduled yet"
        />
      ) : (
        <>
          <div className="mt-7 flex flex-wrap items-center gap-3">
            <ToggleGroup
              aria-label="Filter by status"
              className="rounded-full bg-muted p-1"
              onValueChange={(value) => value && setFilter(value as Filter)}
              type="single"
              value={filter}
            >
              {(["all", "scheduled", "paused"] as const).map((value) => (
                <ToggleGroupItem
                  className="min-h-11 rounded-full px-4 capitalize data-[state=on]:bg-background data-[state=on]:font-medium data-[state=on]:shadow-sm"
                  key={value}
                  value={value}
                >
                  {value}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
            <InputSearch
              aria-label="Filter scheduled tasks"
              className="w-full min-[500px]:max-w-64 min-[500px]:flex-1"
              inputRef={searchRef}
              onChange={setQuery}
              onKeyDown={onSearchKeyDown}
              placeholder="Filter by name or schedule"
              value={query}
            />
          </div>

          {items.length === 0 ? (
            <EmptyState
              action={query.trim() ? () => setQuery("") : undefined}
              actionLabel="Clear filter"
              text={
                query.trim()
                  ? `No scheduled tasks match “${query.trim()}”.`
                  : "No scheduled tasks match this filter."
              }
            />
          ) : (
            <ScheduleInventory
              busy={action.isPending || remove.isPending}
              items={items}
              mobileItems={mobileItems}
              onAction={(schedule, name) => void runAction(schedule, name)}
              onDelete={openDelete}
              onEdit={openEditor}
              sort={sort}
            />
          )}
        </>
      )}

      {editor && (
        <ScheduleForm
          onCancel={closeEditor}
          onSubmit={async (body) => {
            if (editor === "new") await create.mutateAsync({ body });
            else {
              const { name: _name, ...updateBody } = body;
              await update.mutateAsync({ body: updateBody, path: { scheduleName: editor.name } });
            }
            closeEditor();
            await refresh();
          }}
          schedule={editor === "new" ? undefined : editor}
        />
      )}

      <AlertDialog onOpenChange={(open) => !open && closeDelete()} open={Boolean(confirmDelete)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{confirmDelete?.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              The schedule stops firing. Past run transcripts stay in the session store.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className={buttonVariants({ variant: "destructive" })}
              disabled={remove.isPending}
              onClick={() => confirmDelete && void deleteItem(confirmDelete)}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageShell>
  );
}

function ScheduleInventory({
  busy,
  items,
  mobileItems,
  onAction,
  onDelete,
  onEdit,
  sort,
}: {
  busy: boolean;
  items: Schedule[];
  mobileItems: Schedule[];
  onAction: (schedule: Schedule, action: "fire" | "pause" | "resume") => void;
  onDelete: (schedule: Schedule, opener: HTMLButtonElement) => void;
  onEdit: (schedule: Schedule) => void;
  sort: TableSort<ScheduleInventoryOrder>;
}) {
  return (
    <>
      <div
        className="mt-4 overflow-hidden rounded-xl border bg-card max-[500px]:hidden"
        data-schedule-desktop-table
      >
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <SortableHead className="w-2/5" label="Name" sort={sort} sortKey="name" />
              <SortableHead label="Schedule" sort={sort} sortKey="schedule" />
              <SortableHead label="Next run" sort={sort} sortKey="nextRun" />
              <SortableHead label="Last run" sort={sort} sortKey="lastRun" />
              <SortableHead className="text-right" label="Runs" sort={sort} sortKey="fires" />
              <SortableHead label="Status" sort={sort} sortKey="status" />
              <TableHead aria-label="Actions" className="w-0" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((schedule) => (
              <TableRow className="has-aria-expanded:bg-muted/50" key={schedule.name}>
                <TableCell className="max-w-0 whitespace-normal">
                  <Link
                    className="block truncate font-medium hover:underline"
                    params={{ scheduleName: schedule.name }}
                    to="/workspace/schedules/$scheduleName"
                  >
                    {schedule.name}
                  </Link>
                  <p className="line-clamp-1 text-xs text-muted-foreground">{schedule.prompt}</p>
                </TableCell>
                <TableCell className="text-muted-foreground">
                  <span
                    className="block max-w-56 truncate"
                    title={
                      schedule.trigger.kind === "cron"
                        ? schedule.trigger.expression
                        : scheduleTriggerLabel(schedule)
                    }
                  >
                    {scheduleTriggerLabel(schedule)}
                  </span>
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {schedule.enabled && schedule.nextFireAt ? formatDate(schedule.nextFireAt) : "—"}
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {schedule.lastFireAt ? formatDate(schedule.lastFireAt) : "Never"}
                </TableCell>
                <TableCell className="text-right tabular-nums">{runCount(schedule)}</TableCell>
                <TableCell>
                  <ScheduleStatusBadge status={schedule.status} />
                </TableCell>
                <TableCell className="w-0">
                  <ScheduleActions
                    busy={busy}
                    onAction={(name) => onAction(schedule, name)}
                    onDelete={(opener) => onDelete(schedule, opener)}
                    onEdit={() => onEdit(schedule)}
                    schedule={schedule}
                  />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <div
        className="mt-4 divide-y overflow-hidden rounded-xl border bg-card min-[500px]:hidden"
        data-schedule-mobile-list
      >
        {mobileItems.map((schedule) => (
          <Link
            className="flex items-start gap-3 px-4 py-3"
            key={schedule.name}
            params={{ scheduleName: schedule.name }}
            to="/workspace/schedules/$scheduleName"
          >
            <ScheduleStatusDot status={schedule.status} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm font-medium">{schedule.name}</span>
              <span className="line-clamp-2 text-xs text-muted-foreground">{schedule.prompt}</span>
              <span className="block text-[11px] text-muted-foreground tabular-nums">
                {runCount(schedule)} {schedule.fireCount === 1 ? "run" : "runs"}
                {schedule.lastFireAt
                  ? ` · last ${formatDate(schedule.lastFireAt)}`
                  : " · never run"}
              </span>
            </span>
          </Link>
        ))}
      </div>
    </>
  );
}

function ScheduleActions({
  busy,
  onAction,
  onDelete,
  onEdit,
  schedule,
}: {
  busy: boolean;
  onAction: (action: "fire" | "pause" | "resume") => void;
  onDelete: (opener: HTMLButtonElement) => void;
  onEdit: () => void;
  schedule: Schedule;
}) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          aria-label={`Actions for ${schedule.name}`}
          ref={triggerRef}
          size="icon"
          variant="ghost"
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem disabled={busy || !canRunNow(schedule)} onSelect={() => onAction("fire")}>
          Run now
        </DropdownMenuItem>
        <DropdownMenuItem
          disabled={busy || !canTogglePause(schedule)}
          onSelect={() => onAction(schedule.enabled ? "pause" : "resume")}
        >
          {schedule.enabled ? "Pause" : "Resume"}
        </DropdownMenuItem>
        <DropdownMenuItem disabled={busy || !canEditSchedule(schedule)} onSelect={onEdit}>
          Edit
        </DropdownMenuItem>
        <DropdownMenuItem
          disabled={busy}
          onSelect={() => triggerRef.current && onDelete(triggerRef.current)}
          variant="destructive"
        >
          Delete
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function LoadingState() {
  return (
    <div className="mt-8 space-y-3" role="status">
      <span className="sr-only">Loading schedules…</span>
      <Skeleton className="h-10 w-full" />
      <Skeleton className="h-10 w-full" />
      <Skeleton className="h-10 w-full" />
    </div>
  );
}

function EmptyState({
  action,
  actionLabel = "Schedule task",
  text,
  title,
}: {
  action?: () => void;
  actionLabel?: string;
  text: string;
  title?: string;
}) {
  return (
    <Empty className="mt-8 min-h-64 border" role="status">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <CalendarClock />
        </EmptyMedia>
        {title && <EmptyTitle>{title}</EmptyTitle>}
        <EmptyDescription>{text}</EmptyDescription>
      </EmptyHeader>
      {action && (
        <EmptyContent>
          <Button onClick={action} variant="action">
            {actionLabel === "Schedule task" && <Plus />}
            {actionLabel}
          </Button>
        </EmptyContent>
      )}
    </Empty>
  );
}

function runCount(schedule: Schedule) {
  return schedule.maxFires > 0
    ? `${schedule.fireCount}/${schedule.maxFires}`
    : String(schedule.fireCount);
}
