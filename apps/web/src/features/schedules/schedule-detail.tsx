// SPDX-License-Identifier: Apache-2.0

import type {
  ListScheduleFiresResponse,
  ListSchedulesResponse,
} from "@mecatl-studio/contracts/generated";
import {
  actOnScheduleMutation,
  deleteScheduleMutation,
  listScheduleFiresOptions,
  listSchedulesOptions,
  listSchedulesQueryKey,
  updateScheduleMutation,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, ChevronDown, ChevronUp, Clock3, Pause, Play, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
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
import { ScheduleForm } from "./schedule-form";

type Schedule = ListSchedulesResponse["items"][number];
type ScheduleFire = ListScheduleFiresResponse["items"][number];
export type FireSortKey = "duration" | "fired" | "outcome";
type SortDirection = "asc" | "desc";

export function ScheduleDetail({ scheduleName }: { scheduleName: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const schedules = useQuery(listSchedulesOptions());
  const fires = useQuery({
    ...listScheduleFiresOptions({ path: { scheduleName } }),
    enabled: schedules.data?.supported === true,
    refetchInterval: (query) =>
      query.state.data?.items.some((fire) => fire.inFlight) ? 5_000 : false,
  });
  const update = useMutation(updateScheduleMutation());
  const remove = useMutation(deleteScheduleMutation());
  const action = useMutation(actOnScheduleMutation());
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string>();
  const schedule = schedules.data?.items.find((item) => item.name === scheduleName);

  async function refresh() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: listSchedulesQueryKey() }),
      fires.refetch(),
    ]);
  }

  async function runAction(name: "fire" | "pause" | "resume") {
    setError(undefined);
    try {
      await action.mutateAsync({ body: { action: name }, path: { scheduleName } });
      await refresh();
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  async function deleteItem() {
    setError(undefined);
    try {
      await remove.mutateAsync({ path: { scheduleName } });
      await queryClient.invalidateQueries({ queryKey: listSchedulesQueryKey() });
      await navigate({ search: { schedule: undefined }, to: "/workspace/schedules" });
    } catch (caught) {
      setError(errorMessage(caught));
      setConfirmDelete(false);
    }
  }

  if (schedules.isPending) return <DetailState text="Loading scheduled task…" />;
  if (schedules.isError) return <DetailState text={errorMessage(schedules.error)} />;
  if (!schedules.data.supported)
    return <DetailState text={schedules.data.reason} title="Scheduling unavailable" />;
  if (!schedule)
    return (
      <DetailState
        text="This scheduled task no longer exists or is not visible to you."
        title="Scheduled task not found"
      />
    );

  const busy = action.isPending || remove.isPending || update.isPending;
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-5xl px-4 py-7 sm:px-8 sm:py-10">
        <Button asChild size="sm" variant="outline">
          <Link search={{ schedule: undefined }} to="/workspace/schedules">
            <ArrowLeft />
            Back to scheduled
          </Link>
        </Button>

        <div className="mt-7 flex flex-col justify-between gap-4 sm:flex-row sm:items-start">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="break-words text-3xl font-semibold tracking-tight sm:text-4xl">
                {schedule.name}
              </h1>
              <StatusBadge status={schedule.status} />
              <Badge variant="outline">{modeLabel(schedule.mode)}</Badge>
              {schedule.mutating && <Badge variant="warning">writes enabled</Badge>}
            </div>
            <p className="mt-3 max-w-3xl whitespace-pre-wrap text-sm leading-6 text-muted-foreground">
              {schedule.prompt}
            </p>
          </div>
          <div className="flex shrink-0 flex-wrap gap-2">
            <Button
              disabled={busy || !canRunNow(schedule)}
              onClick={() => void runAction("fire")}
              size="sm"
              variant="outline"
            >
              <Play />
              Run now
            </Button>
            <Button
              disabled={busy || !canTogglePause(schedule)}
              onClick={() => void runAction(schedule.enabled ? "pause" : "resume")}
              size="sm"
              variant="outline"
            >
              {schedule.enabled ? <Pause /> : <Play />}
              {schedule.enabled ? "Pause" : "Resume"}
            </Button>
            <Button disabled={busy} onClick={() => setEditing(true)} size="sm" variant="ghost">
              Edit
            </Button>
            <Button
              aria-label={`Delete ${schedule.name}`}
              disabled={busy}
              onClick={() => setConfirmDelete(true)}
              size="icon"
              variant="ghost"
            >
              <Trash2 />
            </Button>
          </div>
        </div>

        {error && (
          <p className="mt-5 rounded-lg bg-destructive/10 p-3 text-sm text-destructive">{error}</p>
        )}

        <div className="mt-8 grid gap-5 sm:grid-cols-2">
          <FactGroup title="Execution">
            <Fact label="Mode" value={modeLabel(schedule.mode)} />
            <Fact label="Write access" value={schedule.mutating ? "Writes allowed" : "Read-only"} />
            <Fact label="Owner" value={schedule.owner || "Deployment default"} />
            <Fact
              label="Tools"
              value={schedule.profile === "noFilesystem" ? "No filesystem" : "All"}
            />
            <Fact
              label="Model"
              value={
                [schedule.providerId, schedule.modelId].filter(Boolean).join(" / ") ||
                "Deployment default"
              }
            />
          </FactGroup>
          <FactGroup title="Trigger">
            <Fact
              label="Type"
              value={schedule.trigger.kind === "cron" ? "Recurring" : "One time"}
            />
            {schedule.trigger.kind === "cron" ? (
              <>
                <Fact label="Cron expression" value={schedule.trigger.expression} />
                <Fact label="Timezone" value={timezoneLabel(schedule.trigger.timezone)} />
                <Fact
                  label="Maximum runs"
                  value={schedule.maxFires ? String(schedule.maxFires) : "Unlimited"}
                />
              </>
            ) : (
              <>
                <Fact label="Runs at" value={formatDate(schedule.trigger.at)} />
                <Fact
                  label="Retry"
                  value={
                    schedule.oneShotRetry
                      ? schedule.oneShotMaxRetries
                        ? `Up to ${schedule.oneShotMaxRetries} times`
                        : "Enabled without a fixed limit"
                      : "Disabled"
                  }
                />
              </>
            )}
            <Fact
              label="Next run"
              value={schedule.nextFireAt ? formatDate(schedule.nextFireAt) : "None"}
            />
            <Fact
              label="Last run"
              value={schedule.lastFireAt ? formatDate(schedule.lastFireAt) : "Never"}
            />
            <Fact label="Runs" value={String(schedule.fireCount)} />
          </FactGroup>
        </div>

        <section className="mt-8">
          <div className="flex items-center gap-2">
            <Clock3 className="size-4 text-muted-foreground" />
            <h2 className="font-semibold">Run history</h2>
          </div>
          <ScheduleFireHistory
            error={fires.isError ? errorMessage(fires.error) : undefined}
            fires={fires.data?.items}
            loading={fires.isPending}
          />
        </section>
      </div>

      {editing && (
        <ScheduleForm
          onCancel={() => setEditing(false)}
          onSubmit={async (body) => {
            const { name: _name, ...updateBody } = body;
            await update.mutateAsync({ body: updateBody, path: { scheduleName } });
            setEditing(false);
            await refresh();
          }}
          schedule={schedule}
        />
      )}

      <AlertDialog onOpenChange={setConfirmDelete} open={confirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{schedule.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              The schedule stops firing. Past run transcripts stay in the session store.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction disabled={remove.isPending} onClick={() => void deleteItem()}>
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function ScheduleFireHistory({
  error,
  fires,
  loading,
}: {
  error?: string;
  fires?: ScheduleFire[];
  loading: boolean;
}) {
  const [sortKey, setSortKey] = useState<FireSortKey>("fired");
  const [direction, setDirection] = useState<SortDirection>("desc");
  const sorted = useMemo(
    () => sortScheduleFires(fires ?? [], sortKey, direction),
    [direction, fires, sortKey],
  );

  function changeSort(key: FireSortKey) {
    if (sortKey === key) setDirection((value) => (value === "asc" ? "desc" : "asc"));
    else {
      setSortKey(key);
      setDirection(key === "fired" ? "desc" : "asc");
    }
  }

  if (loading) return <HistoryState text="Reading run history…" />;
  if (error) return <HistoryState destructive text={error} />;
  if (sorted.length === 0) return <HistoryState text="This task has not run yet." />;

  return (
    <div className="mt-3 overflow-x-auto rounded-xl border bg-card">
      <table className="w-full min-w-[42rem] text-left text-sm">
        <thead className="bg-muted/50 text-xs text-muted-foreground">
          <tr>
            <SortHeader
              direction={direction}
              label="Ran"
              name="fired"
              onSort={changeSort}
              selected={sortKey === "fired"}
            />
            <SortHeader
              direction={direction}
              label="Duration"
              name="duration"
              onSort={changeSort}
              selected={sortKey === "duration"}
            />
            <SortHeader
              direction={direction}
              label="Outcome"
              name="outcome"
              onSort={changeSort}
              selected={sortKey === "outcome"}
            />
            <th className="px-4 py-3 text-right font-medium">Session</th>
          </tr>
        </thead>
        <tbody className="divide-y">
          {sorted.map((fire) => (
            <tr key={fire.id}>
              <td className="px-4 py-3 whitespace-nowrap">
                {fire.firedAt ? formatDate(fire.firedAt) : "—"}
              </td>
              <td className="px-4 py-3 whitespace-nowrap text-muted-foreground">
                {formatDuration(fireDurationMs(fire))}
              </td>
              <td className="px-4 py-3">
                <div className="flex max-w-md flex-col items-start gap-1">
                  <Badge variant={fire.inFlight ? "success" : fire.error ? "warning" : "muted"}>
                    {fireOutcome(fire)}
                  </Badge>
                  {fire.error && <span className="text-xs text-destructive">{fire.error}</span>}
                </div>
              </td>
              <td className="px-4 py-3 text-right">
                {fire.sessionId ? (
                  <Button asChild size="sm" variant="ghost">
                    <Link search={{ sessionId: fire.sessionId }} to="/workspace/chat">
                      View transcript
                    </Link>
                  </Button>
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SortHeader({
  direction,
  label,
  name,
  onSort,
  selected,
}: {
  direction: SortDirection;
  label: string;
  name: FireSortKey;
  onSort: (key: FireSortKey) => void;
  selected: boolean;
}) {
  return (
    <th
      aria-sort={selected ? (direction === "asc" ? "ascending" : "descending") : "none"}
      className="px-4 py-3 font-medium"
    >
      <button
        className="inline-flex items-center gap-1 hover:text-foreground"
        onClick={() => onSort(name)}
        type="button"
      >
        {label}
        {selected &&
          (direction === "asc" ? (
            <ChevronUp className="size-3" />
          ) : (
            <ChevronDown className="size-3" />
          ))}
      </button>
    </th>
  );
}

/**
 * Whether "Run now" is offered: not while a fire is running, and not for a
 * paused or completed schedule, which has nothing scheduled to run.
 */
export function canRunNow(schedule: Pick<Schedule, "status">) {
  return (
    schedule.status !== "running" && schedule.status !== "paused" && schedule.status !== "completed"
  );
}

/**
 * A completed schedule has no next fire: resuming it would schedule nothing,
 * so neither Pause nor Resume applies.
 */
export function canTogglePause(schedule: Pick<Schedule, "status">) {
  return schedule.status !== "completed";
}

/** The daemon runs a cron schedule with no time zone in UTC. */
export function timezoneLabel(timezone: string) {
  return timezone || "UTC";
}

export function sortScheduleFires(
  fires: readonly ScheduleFire[],
  key: FireSortKey,
  direction: SortDirection,
  now = Date.now(),
) {
  const multiplier = direction === "asc" ? 1 : -1;
  return [...fires].sort((left, right) => {
    let comparison: number;
    if (key === "duration")
      comparison = (fireDurationMs(left, now) ?? -1) - (fireDurationMs(right, now) ?? -1);
    else if (key === "outcome") comparison = fireOutcome(left).localeCompare(fireOutcome(right));
    else comparison = dateValue(left.firedAt) - dateValue(right.firedAt);
    return comparison * multiplier || dateValue(right.firedAt) - dateValue(left.firedAt);
  });
}

export function fireDurationMs(fire: ScheduleFire, now = Date.now()) {
  const started = dateValue(fire.startedAt);
  if (!started) return null;
  const end = fire.inFlight
    ? now
    : dateValue(fire.progressAt) || dateValue(fire.deadline) || started;
  return Math.max(0, end - started);
}

function fireOutcome(fire: ScheduleFire) {
  if (fire.inFlight) return fire.startedAt ? "In flight" : "Claimed";
  return fire.error ? "Failed" : fire.stop || "Completed";
}

function dateValue(value: string | null) {
  if (!value) return 0;
  const result = Date.parse(value);
  return Number.isNaN(result) ? 0 : result;
}

function formatDuration(milliseconds: number | null) {
  if (milliseconds === null) return "—";
  const seconds = milliseconds / 1_000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${Math.round(seconds - minutes * 60)}s`;
}

function FactGroup({ children, title }: { children: React.ReactNode; title: string }) {
  return (
    <section>
      <h2 className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {title}
      </h2>
      <dl className="divide-y rounded-xl border bg-card">{children}</dl>
    </section>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-3">
      <dt className="text-sm">{label}</dt>
      <dd className="min-w-0 text-right text-sm text-muted-foreground">{value}</dd>
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

function DetailState({ text, title }: { text: string; title?: string }) {
  return (
    <div className="flex h-full items-center justify-center p-6 text-center">
      <div className="max-w-md rounded-xl border border-dashed p-8">
        {title && <h1 className="font-semibold">{title}</h1>}
        <p className="mt-2 text-sm text-muted-foreground">{text}</p>
        <Button asChild className="mt-5" size="sm" variant="outline">
          <Link search={{ schedule: undefined }} to="/workspace/schedules">
            Back to scheduled
          </Link>
        </Button>
      </div>
    </div>
  );
}

function HistoryState({ destructive, text }: { destructive?: boolean; text: string }) {
  return (
    <p
      className={`mt-3 rounded-xl border border-dashed p-6 text-sm ${destructive ? "text-destructive" : "text-muted-foreground"}`}
    >
      {text}
    </p>
  );
}

function modeLabel(mode: Schedule["mode"]) {
  return mode === "acceptEdits" ? "Accept edits" : mode === "plan" ? "Plan" : "Default";
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
