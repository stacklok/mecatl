"use client";

import { Ellipsis, Loader2 } from "lucide-react";
import { useParams, useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useState } from "react";
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
import { Badge } from "@/components/ui/badge";
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
import { TranscriptDialog } from "@/features/agent/components/transcript-dialog";
import {
  describeCron,
  formatRelativeTime,
  formatUntilTime,
} from "@/lib/formatters";
import { listScheduleFires } from "@/lib/harness/client";
import type { ScheduleFireRow, ScheduleRow } from "@/lib/protocol";
import { toolProfileLabel } from "@/lib/tool-profile";
import { pageTitleClass } from "@/lib/typography";
import { EditScheduleDialog } from "../_components/edit-schedule-dialog";
import { ScheduleStatusBadge } from "../_components/schedule-badges";
import { permissionModeLabel } from "../_components/schedule-form";

/**
 * The route segment is the schedule name. `useParams` hands back the encoded
 * segment, so matching tolerates both forms rather than assuming one.
 */
function segmentMatches(raw: string): (row: ScheduleRow) => boolean {
  let decoded = raw;
  try {
    decoded = decodeURIComponent(raw);
  } catch {
    // Malformed escape: the raw form is the only candidate.
  }
  return (row) => row.name === decoded || row.name === raw;
}

function formatInstant(ms: number | null): string {
  if (ms === null) return "—";
  return new Date(ms).toLocaleString();
}

function formatDurationMs(ms: number): string {
  if (ms < 0) return "—";
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${Math.round(seconds - minutes * 60)}s`;
}

/**
 * A terminal fire record carries no end timestamp, so the best available end
 * is the last progress heartbeat, then the deadline; an in-flight fire is
 * still accruing and reads against now. Null = never started.
 */
function fireDurationMs(fire: ScheduleFireRow): number | null {
  if (fire.startedAt === null) return null;
  const end = fire.inFlight
    ? Date.now()
    : (fire.progressAt ?? fire.deadline ?? fire.startedAt);
  return end - fire.startedAt;
}

function fireDuration(fire: ScheduleFireRow): string {
  const ms = fireDurationMs(fire);
  return ms === null ? "—" : formatDurationMs(ms);
}

/**
 * The fire count against its cap: "None yet", "3", "3 of 10", or
 * "10 of 10 — limit reached" once a capped cron has used up its fires (the
 * daemon then reports no next fire, and this says why).
 */
function describeRuns(row: ScheduleRow): string {
  if (row.fireCount === 0) return "None yet";
  if (row.maxFires <= 0) return String(row.fireCount);
  const base = `${row.fireCount} of ${row.maxFires}`;
  return row.fireCount >= row.maxFires ? `${base} — limit reached` : base;
}

/** The text the Outcome column effectively shows, for sorting. */
function fireOutcomeKey(fire: ScheduleFireRow): string {
  if (fire.inFlight) return fire.startedAt === null ? "claimed" : "in flight";
  return fire.err ? `error: ${fire.err}` : (fire.stop ?? "");
}

export default function ScheduleDetailPage() {
  const params = useParams<{ scheduleId: string }>();
  const router = useRouter();
  const cron = useAgentCron();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [editing, setEditing] = useState(false);
  const [firing, setFiring] = useState(false);
  const [fires, setFires] = useState<ScheduleFireRow[] | null>(null);
  const [firesError, setFiresError] = useState<string | null>(null);
  const [transcript, setTranscript] = useState<{
    sessionId: string;
    label: string;
  } | null>(null);

  const fireSort = useTableSort<"fired" | "duration" | "outcome">(
    "fired",
    "desc",
  );
  const sortedFires = useMemo(() => {
    if (!fires) return [];
    const primary = (a: ScheduleFireRow, b: ScheduleFireRow) => {
      switch (fireSort.key) {
        case "duration":
          return (fireDurationMs(a) ?? -1) - (fireDurationMs(b) ?? -1);
        case "outcome":
          return fireOutcomeKey(a).localeCompare(fireOutcomeKey(b));
        default:
          return (a.firedAt ?? 0) - (b.firedAt ?? 0);
      }
    };
    // Newest-first is the direction-independent tiebreak.
    return [...fires].sort(
      (a, b) =>
        directed(fireSort.dir, primary(a, b)) ||
        (b.firedAt ?? 0) - (a.firedAt ?? 0),
    );
  }, [fires, fireSort.key, fireSort.dir]);

  const row = cron.rows.find(segmentMatches(params.scheduleId));
  const name = row?.name;

  const loadFires = useCallback(
    async (signal?: AbortSignal) => {
      if (!name) return;
      try {
        const next = await listScheduleFires(name, signal);
        if (signal?.aborted) return;
        setFires(next);
        setFiresError(null);
      } catch (caught) {
        if (signal?.aborted) return;
        setFires([]);
        setFiresError(
          caught instanceof Error ? caught.message : String(caught),
        );
      }
    },
    [name],
  );

  useEffect(() => {
    if (!cron.harnessLive || !name) return;
    const controller = new AbortController();
    void loadFires(controller.signal);
    return () => controller.abort();
  }, [cron.harnessLive, name, loadFires]);

  // A live fire changes shape without user input (claimed → running →
  // terminal), so the page re-reads while one is in flight.
  const anyLive =
    row?.fireStage !== undefined && row.fireStage !== "idle"
      ? true
      : (fires?.some((fire) => fire.inFlight) ?? false);
  useEffect(() => {
    if (!cron.harnessLive || !anyLive) return;
    const timer = setInterval(() => {
      void loadFires();
      void cron.refresh();
    }, 5000);
    return () => clearInterval(timer);
  }, [cron.harnessLive, anyLive, loadFires, cron.refresh]);

  const back = () => router.push("/workspace/schedules");

  if (!row) {
    // The registry may still be loading; only treat as missing once settled.
    if (cron.isLoading) {
      return (
        <div className="flex min-h-[60vh] items-center justify-center text-sm text-muted-foreground">
          Loading…
        </div>
      );
    }
    return (
      <div className="flex min-h-[60vh] flex-col items-center justify-center px-4">
        <div className="w-full max-w-md rounded-xl border bg-card p-8 text-center">
          <h1 className="text-lg font-semibold">Scheduled task not found</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            {cron.notWired ??
              "This scheduled task no longer exists — it may have been deleted."}
          </p>
          <Button className="mt-6" onClick={back}>
            Back to Scheduled
          </Button>
        </div>
      </div>
    );
  }

  const fireNow = async () => {
    setFiring(true);
    try {
      // FireNow is synchronous on the daemon: this await lasts the whole run.
      await cron.runJob(row.name);
    } catch {
      // The refusal already landed in cron.error, rendered verbatim below.
    } finally {
      setFiring(false);
      await loadFires();
    }
  };

  const act = async (action: () => Promise<void>) => {
    try {
      await action();
    } catch {
      // Refusal surfaces via cron.error.
    }
    await loadFires();
  };

  return (
    <div className="h-full overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
      <div className="space-y-6">
        <Button
          variant="outline"
          size="sm"
          className="w-fit self-start rounded-full h-9 px-4 gap-1"
          onClick={back}
        >
          <span aria-hidden="true">‹</span>
          Back
        </Button>

        {/* Header: title + one compact badge row, matching the skill detail page.
            All actions live in the ⋯ menu on the right. */}
        <div className="space-y-3">
          <div className="flex items-start justify-between gap-3">
            <h1
              className={pageTitleClass(
                "text-[44px] leading-[1.05] max-[499px]:text-3xl",
              )}
            >
              {row.name}
            </h1>
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="outline"
                  size="icon"
                  className="mt-2 size-9 shrink-0 rounded-full"
                  aria-label={`Actions for ${row.name}`}
                >
                  <Ellipsis className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  disabled={firing}
                  onClick={() => void fireNow()}
                >
                  Run now
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() =>
                    void act(() =>
                      row.enabled
                        ? cron.pauseJob(row.name)
                        : cron.resumeJob(row.name),
                    )
                  }
                >
                  {row.enabled ? "Pause" : "Resume"}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setEditing(true)}>
                  Edit
                </DropdownMenuItem>
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => setConfirmDelete(true)}
                >
                  Delete
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <ScheduleStatusBadge row={row} />
            <Badge variant="outline">{permissionModeLabel(row.mode)}</Badge>
            {row.mutating && <Badge variant="warning">mutating</Badge>}
            {row.profile === "no-fs" && (
              <Badge variant="outline">no filesystem</Badge>
            )}
            {firing && (
              <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                <Loader2 className="size-3.5 animate-spin" />
                Running…
              </span>
            )}
          </div>
        </div>

        {cron.error && (
          <p className="max-w-4xl whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive">
            {cron.error}
          </p>
        )}

        <div className="max-w-4xl space-y-8">
          {/* The prompt is the schedule's instruction — it leads, unlabelled. */}
          <p className="whitespace-pre-wrap rounded-xl border bg-card p-5 text-sm leading-relaxed">
            {row.prompt}
          </p>

          <div className="grid gap-6 sm:grid-cols-2">
            <FactGroup label="Details">
              <FactRow label="Mode">{permissionModeLabel(row.mode)}</FactRow>
              <FactRow label="Write access">
                {row.mutating ? "Writes allowed" : "Read-only"}
              </FactRow>
              <FactRow label="Tools">
                {toolProfileLabel(row.profile === "no-fs" ? "no-fs" : "")}
              </FactRow>
              {row.owner && <FactRow label="Owner">{row.owner}</FactRow>}
            </FactGroup>

            <FactGroup label="Frequency">
              <FactRow label="Repeat" suppressHydrationWarning>
                {row.cron
                  ? describeCron(row.cron)
                  : `Once at ${formatInstant(row.oneShotAt)}`}
              </FactRow>
              {row.cron && (
                <FactRow label="Timezone">
                  {row.timezone || "Daemon-local time"}
                </FactRow>
              )}
              <FactRow label="Next run" suppressHydrationWarning>
                <span title={formatInstant(row.nextFireAt)}>
                  {row.enabled && row.nextFireAt !== null
                    ? `in ${formatUntilTime(row.nextFireAt)}`
                    : "—"}
                </span>
              </FactRow>
              <FactRow label="Last run" suppressHydrationWarning>
                <span
                  title={
                    row.lastFireAt ? formatInstant(row.lastFireAt) : undefined
                  }
                >
                  {row.lastFireAt
                    ? `${formatRelativeTime(row.lastFireAt)} ago`
                    : "Never"}
                </span>
              </FactRow>
              {/* The TUI inspect's fire_count + max_fires, as one fact. */}
              <FactRow label="Runs">{describeRuns(row)}</FactRow>
              {!row.cron && (
                <FactRow label="Retry">
                  {row.oneShotRetry
                    ? `Up to ${row.oneShotMaxRetries || "unlimited"} on failure`
                    : "None"}
                </FactRow>
              )}
            </FactGroup>
          </div>

          <section className="space-y-2">
            <h2 className="text-sm font-semibold">Run log</h2>
            {firesError ? (
              <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive">
                {firesError}
              </p>
            ) : fires === null ? (
              <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-8 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
                Reading the run log…
              </div>
            ) : fires.length === 0 ? (
              <p className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                This schedule has not run yet.
              </p>
            ) : (
              <div className="overflow-hidden rounded-lg border">
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <SortableHead
                        label="Ran"
                        sortKey="fired"
                        sort={fireSort}
                      />
                      <SortableHead
                        label="Duration"
                        sortKey="duration"
                        sort={fireSort}
                      />
                      <SortableHead
                        label="Outcome"
                        sortKey="outcome"
                        sort={fireSort}
                      />
                      <TableHead className="text-right">Session</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {sortedFires.map((fire) => (
                      <TableRow key={fire.id}>
                        <TableCell
                          suppressHydrationWarning
                          className="whitespace-nowrap text-sm tabular-nums"
                        >
                          {formatInstant(fire.firedAt)}
                        </TableCell>
                        <TableCell
                          suppressHydrationWarning
                          className="whitespace-nowrap text-sm text-muted-foreground tabular-nums"
                        >
                          {fireDuration(fire)}
                        </TableCell>
                        <TableCell>
                          {fire.inFlight ? (
                            <Badge variant="success">
                              <span
                                aria-hidden="true"
                                className="size-1.5 animate-pulse rounded-full bg-current"
                              />
                              {fire.startedAt === null
                                ? "claimed"
                                : "in flight"}
                            </Badge>
                          ) : (
                            <div className="flex flex-col gap-1">
                              <Badge
                                variant={fire.err ? "destructive" : "secondary"}
                              >
                                {fire.stop}
                              </Badge>
                              {fire.err && (
                                <span className="max-w-[24rem] whitespace-pre-wrap text-xs text-destructive">
                                  {fire.err}
                                </span>
                              )}
                            </div>
                          )}
                        </TableCell>
                        <TableCell className="text-right">
                          {fire.sessionId ? (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 rounded-full text-xs"
                              onClick={() =>
                                setTranscript({
                                  sessionId: fire.sessionId,
                                  label: `${row.name} — ${formatInstant(fire.firedAt)}`,
                                })
                              }
                            >
                              View transcript
                            </Button>
                          ) : (
                            <span className="text-xs text-muted-foreground">
                              —
                            </span>
                          )}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </section>
        </div>

        {editing && (
          <EditScheduleDialog
            row={row}
            updateFromDraft={cron.updateFromDraft}
            renameAndUpdateFromDraft={cron.renameAndUpdateFromDraft}
            onRenamed={(name) =>
              router.push(`/workspace/schedules/${encodeURIComponent(name)}`)
            }
            onClose={() => setEditing(false)}
          />
        )}

        {transcript && (
          <TranscriptDialog
            sessionId={transcript.sessionId}
            label={transcript.label}
            onClose={() => setTranscript(null)}
          />
        )}

        <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete {row.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                The schedule stops firing. Past run transcripts stay in the
                session store.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  void cron.deleteJob(row.name).catch(() => {
                    // Refusal surfaces via cron.error on the list page.
                  });
                  setConfirmDelete(false);
                  back();
                }}
              >
                Delete
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </div>
  );
}

/** Eyebrow-labelled group of read-only facts; editing stays in the Edit dialog. */
function FactGroup({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-2">
      <h2 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </h2>
      <div className="divide-y rounded-lg border bg-background">{children}</div>
    </section>
  );
}

/** One label-left/value-right row of a fact group. */
function FactRow({
  label,
  children,
  suppressHydrationWarning,
}: {
  label: string;
  children: React.ReactNode;
  suppressHydrationWarning?: boolean;
}) {
  return (
    <div className="flex items-center justify-between gap-3 px-4 py-3">
      <span className="shrink-0 text-sm">{label}</span>
      <span
        suppressHydrationWarning={suppressHydrationWarning}
        className="min-w-0 text-right text-sm text-muted-foreground tabular-nums"
      >
        {children}
      </span>
    </div>
  );
}
