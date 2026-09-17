"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { useMemoryEntryDetail } from "@/features/agent";
import { formatRelativeTime } from "@/lib/formatters";
import type { HarnessUserModelRevision } from "@/lib/harness/client";

/**
 * The value, provenance and revision history of one remembered fact — the
 * web analogue of the TUI's /usermodel `enter` step
 * (cmd/mecatui/ui/usermodel.go renderUserModelDetail). Every row is
 * daemon-derived: the revision's writer and origin replace the "Learned in
 * conversation" label the page used to hard-code.
 *
 * Read-only by construction (memory rule 8): the footer names the two tools
 * the agent curates memory with instead of offering an editor.
 */
export function MemoryFactDetail({
  entryKey,
  indexDescription,
}: {
  /** The raw store key (the route segment, decoded). */
  entryKey: string;
  /** The one-line description the index carried, shown until the detail
   *  read lands and as the fallback when the revision has none. */
  indexDescription: string;
}) {
  const { detail, isLoading, error, stale } = useMemoryEntryDetail(entryKey);
  const current = detail?.current ?? null;
  const description = current?.description || indexDescription;

  return (
    <div className="max-w-4xl space-y-8">
      <section
        aria-label="Value"
        className="rounded-xl border bg-card p-5 text-sm leading-relaxed"
      >
        {isLoading ? (
          <p className="text-muted-foreground">Loading value…</p>
        ) : error ? (
          <p className="text-destructive">Value unavailable: {error}</p>
        ) : stale || !current ? (
          <p className="text-muted-foreground">
            Value unavailable (the key no longer matches an entry).
          </p>
        ) : (
          <pre className="whitespace-pre-wrap break-words font-mono text-sm">
            {current.value || "(empty value)"}
          </pre>
        )}
        {description && (
          <p className="mt-3 whitespace-pre-wrap text-muted-foreground">
            {description}
          </p>
        )}
      </section>

      <div className="space-y-2">
        <h2 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Details
        </h2>
        <div className="divide-y rounded-lg border bg-background">
          <FactRow label="Key" mono>
            {entryKey}
          </FactRow>
          {current && <RevisionRows revision={current} />}
        </div>
      </div>

      {detail && <HistorySection detail={detail} />}

      <p className="text-xs text-muted-foreground">
        Read-only — ask the agent to use ForgetUserMemory or UndoUserMemory.
      </p>
    </div>
  );
}

/** The provenance rows of the current revision; an empty field has no row. */
function RevisionRows({ revision }: { revision: HarnessUserModelRevision }) {
  return (
    <>
      {revision.status && <FactRow label="Status">{revision.status}</FactRow>}
      {revision.version && (
        <FactRow label="Version" mono>
          {revision.version}
        </FactRow>
      )}
      {revision.writer && <FactRow label="Writer">{revision.writer}</FactRow>}
      {revision.origin && <FactRow label="Origin">{revision.origin}</FactRow>}
      {revision.sourceSessionId && (
        <FactRow label="Source session" mono>
          <Link
            href={`/workspace/chat/${encodeURIComponent(revision.sourceSessionId)}`}
            className="hover:underline"
          >
            {revision.sourceSessionId}
          </Link>
        </FactRow>
      )}
      {revision.sourceProposalId && (
        <FactRow label="Proposal" mono>
          <Link href="/workspace/settings/learning" className="hover:underline">
            {revision.sourceProposalId}
          </Link>
        </FactRow>
      )}
      {revision.updatedAtUnix > 0 && (
        <FactRow label="Updated">
          <time
            dateTime={isoInstant(revision.updatedAtUnix)}
            title={isoInstant(revision.updatedAtUnix)}
          >
            {formatRelativeTime(revision.updatedAtUnix * 1000)} ago
          </time>
        </FactRow>
      )}
    </>
  );
}

/**
 * The bounded prior revisions, in the order the daemon delivered them
 * (newest first). A store/driver that supplied only the legacy current
 * value says so — that is a different fact from "no prior revisions".
 */
function HistorySection({
  detail,
}: {
  detail: NonNullable<ReturnType<typeof useMemoryEntryDetail>["detail"]>;
}) {
  return (
    <section aria-label="History" className="space-y-2">
      <h2 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        History
      </h2>
      {detail.historyAvailable ? (
        <div className="rounded-lg border bg-background">
          <p className="px-4 py-3 text-sm text-muted-foreground">
            {detail.history.length}{" "}
            {detail.history.length === 1
              ? "bounded revision"
              : "bounded revisions"}
          </p>
          {detail.history.length > 0 && (
            <ul className="divide-y border-t">
              {detail.history.map((prior) => (
                <li
                  key={`${prior.version}:${prior.status}:${prior.updatedAtUnix}`}
                  className="px-4 py-3 font-mono text-sm"
                >
                  {prior.version || "?"} · {prior.status || "?"} ·{" "}
                  {prior.updatedAtUnix > 0 ? isoDate(prior.updatedAtUnix) : "—"}
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : (
        <p className="rounded-lg border bg-background px-4 py-3 text-sm text-muted-foreground">
          History unavailable from this store/driver.
        </p>
      )}
    </section>
  );
}

/** One label/value row in the Details group. */
function FactRow({
  label,
  mono = false,
  children,
}: {
  label: string;
  mono?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-3 px-4 py-3">
      <span className="text-sm">{label}</span>
      <span
        className={
          mono
            ? "break-all text-right font-mono text-sm text-muted-foreground"
            : "text-right text-sm text-muted-foreground"
        }
      >
        {children}
      </span>
    </div>
  );
}

function isoInstant(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toISOString();
}

function isoDate(unixSeconds: number): string {
  return isoInstant(unixSeconds).slice(0, 10);
}
