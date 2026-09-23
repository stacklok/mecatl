// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import {
  actOnScheduleMutation,
  createScheduleMutation,
  deleteScheduleMutation,
  listScheduleFiresOptions,
  listScheduleFiresQueryKey,
  listSchedulesOptions,
  listSchedulesQueryKey,
  updateScheduleMutation,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CalendarClock, ChevronDown, History, Pause, Play, Plus, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
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
import { canRunNow, canTogglePause, timezoneLabel } from "./schedule-detail";
import { ScheduleForm } from "./schedule-form";

type Schedule = ListSchedulesResponse["items"][number];
type Filter = "all" | "paused" | "scheduled";

export function SchedulesWorkspace({ scheduleName }: { scheduleName?: string }) {
  const queryClient = useQueryClient();
  const schedules = useQuery(listSchedulesOptions());
  const create = useMutation(createScheduleMutation());
  const update = useMutation(updateScheduleMutation());
  const remove = useMutation(deleteScheduleMutation());
  const action = useMutation(actOnScheduleMutation());
  const [filter, setFilter] = useState<Filter>("all");
  const [editor, setEditor] = useState<Schedule | "new">();
  const [confirmDelete, setConfirmDelete] = useState<Schedule>();
  /** The `?schedule=` name whose editor has already been opened. */
  const openedFor = useRef<string | undefined>(undefined);
  const [error, setError] = useState<string>();

  const items = (schedules.data?.items ?? []).filter((item) =>
    filter === "all"
      ? true
      : filter === "paused"
        ? item.status === "paused"
        : item.status !== "paused",
  );

  useEffect(() => {
    if (!scheduleName || !schedules.isSuccess) return;
    if (filter !== "all") {
      setFilter("all");
      return;
    }
    const target = document.getElementById(scheduleTargetId(scheduleName));
    target?.scrollIntoView({ behavior: "smooth", block: "center" });
    target?.focus({ preventScroll: true });
    // `?schedule=<name>` opens that schedule's editor. Opening is one-shot per
    // name: closing the editor must not reopen it on the next render.
    if (openedFor.current === scheduleName) return;
    const match = (schedules.data?.items ?? []).find((item) => item.name === scheduleName);
    if (match) {
      openedFor.current = scheduleName;
      setEditor(match);
    }
  }, [filter, scheduleName, schedules.data, schedules.isSuccess]);

  async function refresh() {
    await queryClient.invalidateQueries({ queryKey: listSchedulesQueryKey() });
  }

  async function runAction(schedule: Schedule, name: "fire" | "pause" | "resume") {
    setError(undefined);
    try {
      await action.mutateAsync({ body: { action: name }, path: { scheduleName: schedule.name } });
      await Promise.all([
        refresh(),
        // A manual fire adds a run; an open history must not keep showing
        // the list from before it.
        name === "fire"
          ? queryClient.invalidateQueries({
              queryKey: listScheduleFiresQueryKey({ path: { scheduleName: schedule.name } }),
            })
          : undefined,
      ]);
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
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-6xl px-4 py-7 sm:px-8 sm:py-10">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-3xl font-semibold tracking-tight">Scheduled</h1>
            <p className="mt-2 text-sm text-muted-foreground">
              Recurring and one-off work run by the connected Mecatl instance.
            </p>
          </div>
          {schedules.data?.supported && (
            <Button onClick={() => setEditor("new")} variant="action">
              <Plus />
              Schedule task
            </Button>
          )}
        </div>

        {error && (
          <p className="mt-5 rounded-lg bg-destructive/10 p-3 text-sm text-destructive">{error}</p>
        )}

        {schedules.isPending ? (
          <EmptyState text="Loading schedules…" />
        ) : schedules.isError ? (
          <EmptyState text={errorMessage(schedules.error)} />
        ) : !schedules.data.supported ? (
          <EmptyState
            text={schedules.data.reason}
            title="Scheduling is not wired on this deployment"
          />
        ) : schedules.data.items.length === 0 ? (
          <EmptyState
            action={() => setEditor("new")}
            text="Set up recurring or one-off work and Mecatl will run it unattended."
            title="Nothing scheduled yet"
          />
        ) : (
          <>
            <div className="mt-7 inline-flex rounded-full bg-muted p-1">
              {(["all", "scheduled", "paused"] as const).map((value) => (
                <button
                  className={`h-8 rounded-full px-4 text-sm capitalize ${filter === value ? "bg-background font-medium shadow-sm" : "text-muted-foreground"}`}
                  key={value}
                  onClick={() => setFilter(value)}
                  type="button"
                >
                  {value}
                </button>
              ))}
            </div>

            {items.length === 0 ? (
              <EmptyState text="No scheduled tasks match this filter." />
            ) : (
              <div className="mt-4 divide-y overflow-hidden rounded-xl border bg-card">
                {items.map((schedule) => (
                  <ScheduleItem
                    busy={action.isPending || remove.isPending}
                    key={schedule.name}
                    onAction={(name) => void runAction(schedule, name)}
                    onDelete={() => setConfirmDelete(schedule)}
                    onEdit={() => setEditor(schedule)}
                    schedule={schedule}
                    selected={schedule.name === scheduleName}
                  />
                ))}
              </div>
            )}
          </>
        )}
      </div>

      {editor && (
        <ScheduleForm
          onCancel={() => setEditor(undefined)}
          onSubmit={async (body) => {
            if (editor === "new") await create.mutateAsync({ body });
            else {
              const { name: _name, ...updateBody } = body;
              await update.mutateAsync({ body: updateBody, path: { scheduleName: editor.name } });
            }
            setEditor(undefined);
            await refresh();
          }}
          schedule={editor === "new" ? undefined : editor}
        />
      )}

      <AlertDialog
        onOpenChange={(open) => !open && setConfirmDelete(undefined)}
        open={Boolean(confirmDelete)}
      >
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
              disabled={remove.isPending}
              onClick={() => confirmDelete && void deleteItem(confirmDelete)}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function ScheduleItem({
  busy,
  onAction,
  onDelete,
  onEdit,
  schedule,
  selected,
}: {
  busy: boolean;
  onAction: (action: "fire" | "pause" | "resume") => void;
  onDelete: () => void;
  onEdit: () => void;
  schedule: Schedule;
  selected: boolean;
}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  useEffect(() => {
    if (selected) setHistoryOpen(true);
  }, [selected]);
  return (
    <article
      className={selected ? "bg-brand/5 p-4 ring-2 ring-inset ring-brand/35 sm:p-5" : "p-4 sm:p-5"}
      id={scheduleTargetId(schedule.name)}
      tabIndex={-1}
    >
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="font-semibold">
              <Link
                className="hover:text-brand hover:underline"
                params={{ scheduleName: schedule.name }}
                to="/workspace/schedules/$scheduleName"
              >
                {schedule.name}
              </Link>
            </h2>
            <StatusBadge status={schedule.status} />
            <Badge variant="outline">
              {schedule.mode === "acceptEdits" ? "accept edits" : schedule.mode}
            </Badge>
            {schedule.mutating && <Badge variant="warning">writes enabled</Badge>}
          </div>
          <p className="mt-1 line-clamp-2 text-sm text-muted-foreground">{schedule.prompt}</p>
          <div className="mt-3 flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted-foreground">
            <span>{triggerLabel(schedule)}</span>
            <span>
              {schedule.nextFireAt && schedule.enabled
                ? `Next ${formatDate(schedule.nextFireAt)}`
                : "No next run"}
            </span>
            <span>
              {schedule.fireCount} run{schedule.fireCount === 1 ? "" : "s"}
            </span>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={busy || !canRunNow(schedule)}
            onClick={() => onAction("fire")}
            size="sm"
            variant="outline"
          >
            <Play />
            Run now
          </Button>
          <Button
            disabled={busy || !canTogglePause(schedule)}
            onClick={() => onAction(schedule.enabled ? "pause" : "resume")}
            size="sm"
            variant="outline"
          >
            {schedule.enabled ? <Pause /> : <Play />}
            {schedule.enabled ? "Pause" : "Resume"}
          </Button>
          <Button disabled={busy} onClick={onEdit} size="sm" variant="ghost">
            Edit
          </Button>
          <Button
            aria-label={`Delete ${schedule.name}`}
            disabled={busy}
            onClick={onDelete}
            size="icon"
            variant="ghost"
          >
            <Trash2 />
          </Button>
        </div>
      </div>
      <button
        className="mt-4 flex items-center gap-2 text-xs font-medium text-muted-foreground hover:text-foreground"
        onClick={() => setHistoryOpen((open) => !open)}
        type="button"
      >
        <History className="size-3.5" />
        <span>Run history</span>
        <ChevronDown
          className={`size-3.5 transition-transform ${historyOpen ? "rotate-180" : ""}`}
        />
      </button>
      {historyOpen && <ScheduleHistory name={schedule.name} />}
    </article>
  );
}

function scheduleTargetId(name: string) {
  return `schedule-${encodeURIComponent(name)}`;
}

function ScheduleHistory({ name }: { name: string }) {
  const fires = useQuery(listScheduleFiresOptions({ path: { scheduleName: name } }));
  if (fires.isPending)
    return <p className="mt-3 text-xs text-muted-foreground">Reading run history…</p>;
  if (fires.isError)
    return <p className="mt-3 text-xs text-destructive">{errorMessage(fires.error)}</p>;
  if (fires.data.items.length === 0)
    return <p className="mt-3 text-xs text-muted-foreground">This task has not run yet.</p>;
  return (
    <div className="mt-3 overflow-x-auto rounded-lg border">
      <table className="w-full text-left text-xs">
        <thead className="bg-muted/50 text-muted-foreground">
          <tr>
            <th className="px-3 py-2 font-medium">Ran</th>
            <th className="px-3 py-2 font-medium">Outcome</th>
            <th className="px-3 py-2 font-medium">Session</th>
          </tr>
        </thead>
        <tbody className="divide-y">
          {fires.data.items.map((fire) => (
            <tr key={fire.id}>
              <td className="px-3 py-2 whitespace-nowrap">
                {fire.firedAt ? formatDate(fire.firedAt) : "—"}
              </td>
              <td className="px-3 py-2">
                {fire.inFlight ? "In flight" : fire.error || fire.stop || "Completed"}
              </td>
              <td className="px-3 py-2 font-mono text-muted-foreground">{fire.sessionId || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function EmptyState({
  action,
  text,
  title,
}: {
  action?: () => void;
  text: string;
  title?: string;
}) {
  return (
    <div className="mt-8 flex min-h-64 flex-col items-center justify-center rounded-xl border border-dashed p-8 text-center">
      <span className="flex size-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <CalendarClock className="size-5" />
      </span>
      {title && <h2 className="mt-4 font-semibold">{title}</h2>}
      <p className="mt-2 max-w-md text-sm text-muted-foreground">{text}</p>
      {action && (
        <Button className="mt-5" onClick={action} variant="action">
          <Plus />
          Schedule task
        </Button>
      )}
    </div>
  );
}

function StatusBadge({ status }: { status: Schedule["status"] }) {
  const variant =
    status === "running"
      ? "success"
      : status === "paused"
        ? "muted"
        : status === "claimed"
          ? "warning"
          : "info";
  return <Badge variant={variant}>{status}</Badge>;
}

function triggerLabel(schedule: Schedule) {
  return schedule.trigger.kind === "cron"
    ? `${schedule.trigger.expression} · ${timezoneLabel(schedule.trigger.timezone)}`
    : `Once ${formatDate(schedule.trigger.at)}`;
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
