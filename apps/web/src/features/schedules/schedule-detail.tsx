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
import { CalendarClock, Ellipsis } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { SortableHead, type SortDir } from "@/components/sortable-head";
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
import { Badge } from "../../components/ui/badge";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../components/ui/table";
import { errorMessage } from "../../lib/error-message";
import { describeCron, formatDate } from "../../lib/formatters";
import { pageTitleClass } from "../../lib/typography";
import { canEditSchedule, ScheduleForm } from "./schedule-form";
import { ScheduleStatusBadge } from "./schedule-status";

type Schedule = ListSchedulesResponse["items"][number];
/**
 * TERM: A schedule fire is one claimed or started execution attempt and its
 * resulting session, when one exists. Avoid: run, which is also used for an
 * agent run inside a session.
 */
type ScheduleFire = ListScheduleFiresResponse["items"][number];
export type FireSortKey = "fired" | "outcome";

export const scheduleDetailPolling = { refetchIntervalInBackground: false } as const;

export function scheduleDetailPollInterval(
  schedules: readonly Pick<Schedule, "name" | "status">[] | undefined,
  scheduleName: string,
) {
  const status = schedules?.find((item) => item.name === scheduleName)?.status;
  return status === "running" || status === "claimed" ? 5_000 : false;
}

export function scheduleFirePollInterval(
  status: Schedule["status"] | undefined,
  fires: readonly Pick<ScheduleFire, "inFlight">[] | undefined,
) {
  return status === "running" || status === "claimed" || fires?.some((fire) => fire.inFlight)
    ? 5_000
    : false;
}

/**
 * DECISION: Detail reads caller-filtered schedules and fires through the
 * authenticated BFF. Direct browser access to the daemon or SDK was rejected
 * because the BFF owns capability negotiation and caller identity.
 *
 * DECISION: Match the prototype's navigation contract by linking a fire's
 * session into the chat workspace. Transcript presentation belongs to chat;
 * a schedule-specific dialog was rejected as a duplicate transcript surface.
 */
export function ScheduleDetail({ scheduleName }: { scheduleName: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const schedules = useQuery({
    ...listSchedulesOptions(),
    ...scheduleDetailPolling,
    refetchInterval: (query) => scheduleDetailPollInterval(query.state.data?.items, scheduleName),
  });
  const schedule = schedules.data?.items.find((item) => item.name === scheduleName);
  const fires = useQuery({
    ...listScheduleFiresOptions({ path: { scheduleName } }),
    ...scheduleDetailPolling,
    enabled: schedules.data?.supported === true,
    refetchInterval: (query) => scheduleFirePollInterval(schedule?.status, query.state.data?.items),
  });
  const update = useMutation(updateScheduleMutation());
  const remove = useMutation(deleteScheduleMutation());
  const action = useMutation(actOnScheduleMutation());
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string>();
  const actionMenuRef = useRef<HTMLButtonElement | null>(null);

  function closeDelete() {
    setConfirmDelete(false);
    queueMicrotask(() => actionMenuRef.current?.focus());
  }

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
      await navigate({ to: "/workspace/schedules" });
    } catch (caught) {
      setError(errorMessage(caught));
      closeDelete();
    }
  }

  if (schedules.isPending) return <DetailState loading text="Loading scheduled task…" />;
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
    <PageShell className="max-w-5xl">
      <Button asChild className="rounded-full" size="sm" variant="outline">
        <Link to="/workspace/schedules">
          <span aria-hidden="true">‹</span>
          Back to scheduled
        </Link>
      </Button>

      <div className="mt-7 flex items-start justify-between gap-4">
        <h1 className={pageTitleClass("min-w-0 break-words")}>{schedule.name}</h1>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button
              aria-label={`Actions for ${schedule.name}`}
              className="shrink-0 rounded-full"
              ref={actionMenuRef}
              size="icon"
              variant="outline"
            >
              <Ellipsis />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              disabled={busy || !canRunNow(schedule)}
              onSelect={() => void runAction("fire")}
            >
              Run now
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={busy || !canTogglePause(schedule)}
              onSelect={() => void runAction(schedule.enabled ? "pause" : "resume")}
            >
              {schedule.enabled ? "Pause" : "Resume"}
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={busy || !canEditSchedule(schedule)}
              onSelect={() => setEditing(true)}
            >
              Edit
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={busy}
              onSelect={() => setConfirmDelete(true)}
              variant="destructive"
            >
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <ScheduleStatusBadge status={schedule.status} />
        <Badge variant="outline">{modeLabel(schedule.mode)}</Badge>
        {schedule.mutating && <Badge variant="warning">writes enabled</Badge>}
        {schedule.profile === "noFilesystem" && <Badge variant="outline">no filesystem</Badge>}
      </div>

      {error && (
        <Alert className="mt-5" variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <p className="mt-8 max-w-3xl whitespace-pre-wrap rounded-xl border bg-card p-5 text-sm leading-6">
        {schedule.prompt}
      </p>

      <div className="mt-8 grid gap-5 sm:grid-cols-2">
        <FactGroup title="Details">
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
        <FactGroup title="Frequency">
          <Fact
            label="Repeat"
            title={
              schedule.trigger.kind === "cron"
                ? `${schedule.trigger.expression} (${timezoneLabel(schedule.trigger.timezone)})`
                : undefined
            }
            value={
              schedule.trigger.kind === "cron"
                ? describeCron(schedule.trigger.expression)
                : `Once at ${formatDate(schedule.trigger.at)}`
            }
          />
          {schedule.trigger.kind === "cron" && (
            <Fact label="Timezone" value={timezoneLabel(schedule.trigger.timezone)} />
          )}
          <Fact
            label="Next run"
            title={schedule.nextFireAt ? formatDate(schedule.nextFireAt) : undefined}
            value={describeNextRun(schedule)}
          />
          <Fact
            label="Last run"
            title={schedule.lastFireAt ? formatDate(schedule.lastFireAt) : undefined}
            value={describeLastRun(schedule)}
          />
          <Fact label="Runs" value={describeRuns(schedule)} />
          {schedule.trigger.kind === "once" && (
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
          )}
        </FactGroup>
      </div>

      <section className="mt-8">
        <h2 className="font-semibold">Run history</h2>
        <ScheduleFireHistory
          error={fires.isError ? errorMessage(fires.error) : undefined}
          fires={fires.data?.items}
          loading={fires.isPending}
        />
      </section>

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

      <AlertDialog onOpenChange={(open) => !open && closeDelete()} open={confirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{schedule.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              The schedule stops firing. Past run transcripts stay in the session store.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className={buttonVariants({ variant: "destructive" })}
              disabled={remove.isPending}
              onClick={() => void deleteItem()}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageShell>
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
  const [direction, setDirection] = useState<SortDir>("desc");
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
    <>
      <div className="mt-3 overflow-hidden rounded-xl border bg-card max-[500px]:hidden">
        <Table className="min-w-[42rem]">
          <TableHeader className="bg-muted/50 text-xs text-muted-foreground">
            <TableRow className="hover:bg-transparent">
              {(
                [
                  ["fired", "Ran"],
                  ["outcome", "Outcome"],
                ] as const
              ).map(([key, label]) => (
                <SortableHead
                  key={key}
                  label={label}
                  sort={{ dir: direction, key: sortKey, toggle: changeSort }}
                  sortKey={key}
                />
              ))}
              <TableHead className="text-right">Session</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((fire) => (
              <TableRow key={fire.id}>
                <TableCell className="whitespace-nowrap">
                  {fire.firedAt ? formatDate(fire.firedAt) : "—"}
                </TableCell>
                <TableCell className="whitespace-normal">
                  <FireOutcome fire={fire} />
                </TableCell>
                <TableCell className="text-right">
                  {fire.sessionId ? (
                    <Button asChild size="sm" variant="ghost">
                      <Link search={{ sessionId: fire.sessionId }} to="/workspace/chat">
                        View transcript
                      </Link>
                    </Button>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <div
        className="mt-3 divide-y overflow-hidden rounded-xl border bg-card min-[500px]:hidden"
        data-schedule-fire-mobile-list
      >
        {sorted.map((fire) => (
          <article className="space-y-3 p-4" key={fire.id}>
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="text-sm font-medium">
                  {fire.firedAt ? formatDate(fire.firedAt) : "Time unavailable"}
                </p>
              </div>
              <FireOutcome fire={fire} compact />
            </div>
            {fire.sessionId && (
              <Button asChild size="sm" variant="outline">
                <Link search={{ sessionId: fire.sessionId }} to="/workspace/chat">
                  View transcript
                </Link>
              </Button>
            )}
          </article>
        ))}
      </div>
    </>
  );
}

function FireOutcome({ compact, fire }: { compact?: boolean; fire: ScheduleFire }) {
  const outcome = fireOutcome(fire);
  const variant =
    outcome === "In flight" || outcome === "Claimed"
      ? "success"
      : outcome === "Failed"
        ? "destructive"
        : outcome === "Timed out" || outcome === "Limit reached"
          ? "warning"
          : "muted";
  const rawStop = fire.stop.trim();
  return (
    <div className={`flex flex-col gap-1 ${compact ? "items-end text-right" : "items-start"}`}>
      <Badge variant={variant}>{outcome}</Badge>
      {!fire.inFlight && rawStop && rawStop.toLocaleLowerCase() !== outcome.toLocaleLowerCase() && (
        <span className="text-xs text-muted-foreground">Stop: {rawStop}</span>
      )}
      {fire.error && <span className="max-w-md text-xs text-destructive">{fire.error}</span>}
    </div>
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

/** Bare span between two instants ("5m", "2h", "3d"); a due-or-past gap reads "<1m". */
function formatSpan(milliseconds: number) {
  const minutes = Math.floor(milliseconds / 60_000);
  if (minutes < 1) return "<1m";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h` : `${Math.floor(hours / 24)}d`;
}

/** A paused or completed schedule will not fire, so it has no next run to promise. */
export function describeNextRun(
  schedule: Pick<Schedule, "enabled" | "nextFireAt">,
  now = Date.now(),
) {
  const at = Date.parse(schedule.nextFireAt);
  if (!schedule.enabled || Number.isNaN(at)) return "—";
  return `in ${formatSpan(at - now)}`;
}

export function describeLastRun(schedule: Pick<Schedule, "lastFireAt">, now = Date.now()) {
  const at = Date.parse(schedule.lastFireAt);
  if (Number.isNaN(at)) return "Never";
  return `${formatSpan(now - at)} ago`;
}

export function describeRuns(schedule: Pick<Schedule, "fireCount" | "maxFires">) {
  if (schedule.fireCount === 0) return "None yet";
  if (schedule.maxFires <= 0) return String(schedule.fireCount);
  const base = `${schedule.fireCount} of ${schedule.maxFires}`;
  return schedule.fireCount >= schedule.maxFires ? `${base} — limit reached` : base;
}

export function sortScheduleFires(
  fires: readonly ScheduleFire[],
  key: FireSortKey,
  direction: SortDir,
) {
  const multiplier = direction === "asc" ? 1 : -1;
  return [...fires].sort((left, right) => {
    let comparison: number;
    if (key === "outcome") comparison = fireOutcome(left).localeCompare(fireOutcome(right));
    else comparison = dateValue(left.firedAt) - dateValue(right.firedAt);
    return comparison * multiplier || dateValue(right.firedAt) - dateValue(left.firedAt);
  });
}

/**
 * DECISION: The outcome is read from the recorded `session.StopReason`, not by
 * matching substrings. A timeout is recorded with both `timeout` and an error
 * (`scheduler_fire.go`), so the stop reason is checked before the error. A
 * limit stop is neither a success nor a failure, and an unknown future reason
 * reads "Stopped" rather than claiming completion.
 */
const COMPLETED_STOPS = new Set([
  "",
  "end_turn",
  "structured_output",
  "plan_approved",
  "plan_iterate",
]);
const LIMIT_STOPS = new Set(["max_turns", "max_tool_calls", "budget"]);
const FAILED_STOPS = new Set(["error", "max_consecutive_failures"]);

export function fireOutcome(fire: ScheduleFire) {
  if (fire.inFlight) return fire.startedAt ? "In flight" : "Claimed";
  const stop = fire.stop.trim().toLocaleLowerCase();
  if (stop === "timeout") return "Timed out";
  if (stop === "cancelled") return "Cancelled";
  if (fire.error || FAILED_STOPS.has(stop)) return "Failed";
  if (LIMIT_STOPS.has(stop)) return "Limit reached";
  return COMPLETED_STOPS.has(stop) ? "Completed" : "Stopped";
}

function dateValue(value: string | null) {
  if (!value) return 0;
  const result = Date.parse(value);
  return Number.isNaN(result) ? 0 : result;
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

function Fact({ label, title, value }: { label: string; title?: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-3">
      <dt className="text-sm">{label}</dt>
      <dd className="min-w-0 text-right text-sm text-muted-foreground" title={title}>
        {value}
      </dd>
    </div>
  );
}

function DetailState({
  loading,
  text,
  title,
}: {
  loading?: boolean;
  text: string;
  title?: string;
}) {
  return (
    <div className="flex h-full items-center justify-center p-6">
      <Empty className="max-w-md flex-none border" role={loading ? "status" : "alert"}>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <CalendarClock />
          </EmptyMedia>
          {title && <EmptyTitle>{title}</EmptyTitle>}
          <EmptyDescription>{text}</EmptyDescription>
        </EmptyHeader>
        {!loading && (
          <EmptyContent>
            <Button asChild size="sm" variant="outline">
              <Link to="/workspace/schedules">Back to scheduled</Link>
            </Button>
          </EmptyContent>
        )}
      </Empty>
    </div>
  );
}

function HistoryState({ destructive, text }: { destructive?: boolean; text: string }) {
  if (destructive) {
    return (
      <Alert className="mt-3" variant="destructive">
        <AlertDescription>{text}</AlertDescription>
      </Alert>
    );
  }
  return (
    <Empty className="mt-3 border" role="status">
      <EmptyHeader>
        <EmptyDescription>{text}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  );
}

function modeLabel(mode: Schedule["mode"]) {
  return mode === "acceptEdits" ? "Accept edits" : mode === "plan" ? "Plan" : "Default";
}
