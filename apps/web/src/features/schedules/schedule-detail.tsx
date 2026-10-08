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
import { Ellipsis } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { PageShell } from "../../components/shell/page-shell";
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { SortableHead, type SortDirection } from "../../components/ui/sortable-head";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../components/ui/table";
import { pageTitleClass } from "../../lib/typography";
import { TranscriptDialog } from "../chat/transcript-dialog";
import { describeCron } from "./cron-builder";
import { canEditSchedule, ScheduleForm } from "./schedule-form";

type Schedule = ListSchedulesResponse["items"][number];
/**
 * TERM: A schedule fire is one claimed or started execution attempt and its
 * resulting session, when one exists. Avoid: run, which is also used for an
 * agent run inside a session.
 */
type ScheduleFire = ListScheduleFiresResponse["items"][number];
export type FireSortKey = "duration" | "fired" | "outcome";

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
 * SPEC: A fire transcript opened here is an inspection surface: navigation may
 * fetch transcript and activity but must not offer actions that start, mutate,
 * retry, approve, rename, fork, clear, or delete its scheduled session.
 *
 * DECISION: Match prototype PR #45 by opening a dedicated read-only transcript
 * dialog from the run log. Ordinary chat and route state were rejected because
 * this inspection must expose no composer or session mutation controls and must
 * preserve schedule-detail context.
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
  const [transcript, setTranscript] = useState<{ sessionId: string; title: string }>();
  const [error, setError] = useState<string>();
  const transcriptOpener = useRef<HTMLButtonElement | null>(null);
  const actionMenuRef = useRef<HTMLButtonElement | null>(null);

  function openTranscript(fire: ScheduleFire, opener: HTMLButtonElement) {
    transcriptOpener.current = opener;
    setTranscript({
      sessionId: fire.sessionId,
      title: `${scheduleName} — ${fire.firedAt ? formatDate(fire.firedAt) : "scheduled execution"}`,
    });
  }

  function closeTranscript() {
    setTranscript(undefined);
    queueMicrotask(() => transcriptOpener.current?.focus());
  }

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
              onClick={() => void runAction("fire")}
            >
              Run now
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={busy || !canTogglePause(schedule)}
              onClick={() => void runAction(schedule.enabled ? "pause" : "resume")}
            >
              {schedule.enabled ? "Pause" : "Resume"}
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={busy || !canEditSchedule(schedule)}
              onClick={() => setEditing(true)}
            >
              Edit
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={busy}
              onClick={() => setConfirmDelete(true)}
              variant="destructive"
            >
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <StatusBadge status={schedule.status} />
        <Badge variant="outline">{modeLabel(schedule.mode)}</Badge>
        {schedule.mutating && <Badge variant="warning">writes enabled</Badge>}
        {schedule.profile === "noFilesystem" && <Badge variant="outline">no filesystem</Badge>}
      </div>

      {error && (
        <p className="mt-5 rounded-lg bg-destructive/10 p-3 text-sm text-foreground">{error}</p>
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
          onViewTranscript={openTranscript}
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

      {transcript && (
        <TranscriptDialog
          onOpenChange={(open) => !open && closeTranscript()}
          open
          sessionId={transcript.sessionId}
          title={transcript.title}
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
            <AlertDialogAction disabled={remove.isPending} onClick={() => void deleteItem()}>
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
  onViewTranscript,
}: {
  error?: string;
  fires?: ScheduleFire[];
  loading: boolean;
  onViewTranscript: (fire: ScheduleFire, opener: HTMLButtonElement) => void;
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
    <>
      <div className="mt-3 overflow-hidden rounded-xl border bg-card max-[499px]:hidden">
        <Table className="min-w-[42rem]">
          <TableHeader className="bg-muted/50 text-xs text-muted-foreground">
            <TableRow className="hover:bg-transparent">
              {(
                [
                  ["fired", "Ran"],
                  ["duration", "Duration"],
                  ["outcome", "Outcome"],
                ] as const
              ).map(([key, label]) => (
                <SortableHead
                  className="px-4"
                  direction={sortKey === key ? direction : undefined}
                  key={key}
                  label={label}
                  onSort={() => changeSort(key)}
                />
              ))}
              <TableHead className="px-4 text-right">Session</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((fire) => (
              <TableRow key={fire.id}>
                <TableCell className="px-4 py-3 whitespace-nowrap">
                  {fire.firedAt ? formatDate(fire.firedAt) : "—"}
                </TableCell>
                <TableCell className="px-4 py-3 whitespace-nowrap text-muted-foreground">
                  {formatDuration(fireDurationMs(fire))}
                </TableCell>
                <TableCell className="px-4 py-3 whitespace-normal">
                  <FireOutcome fire={fire} />
                </TableCell>
                <TableCell className="px-4 py-3 text-right">
                  {fire.sessionId ? (
                    <Button
                      onClick={(event) => onViewTranscript(fire, event.currentTarget)}
                      size="sm"
                      variant="ghost"
                    >
                      View transcript
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
                <p className="text-xs text-muted-foreground">
                  Duration {formatDuration(fireDurationMs(fire))}
                </p>
              </div>
              <FireOutcome fire={fire} compact />
            </div>
            {fire.sessionId && (
              <Button
                onClick={(event) => onViewTranscript(fire, event.currentTarget)}
                size="sm"
                variant="outline"
              >
                View transcript
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
        : outcome === "Timed out"
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
  if (fire.inFlight) return Math.max(0, now - started);

  // DECISION: A terminal deadline is not a completion time. Show an unknown
  // duration unless progress records an end, except for deadline-exceeded runs.
  const progress = dateValue(fire.progressAt);
  if (progress) return Math.max(0, progress - started);
  if (fireOutcome(fire) !== "Timed out") return null;
  const deadline = dateValue(fire.deadline);
  return deadline ? Math.max(0, deadline - started) : null;
}

export function fireOutcome(fire: ScheduleFire) {
  if (fire.inFlight) return fire.startedAt ? "In flight" : "Claimed";
  if (fire.error) return "Failed";
  const stop = fire.stop.trim().toLocaleLowerCase().replaceAll("-", "_").replaceAll(" ", "_");
  if (stop.includes("cancel")) return "Cancelled";
  if (stop.includes("timeout") || stop.includes("timed_out") || stop.includes("deadline"))
    return "Timed out";
  return "Completed";
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
          <Link to="/workspace/schedules">Back to scheduled</Link>
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
