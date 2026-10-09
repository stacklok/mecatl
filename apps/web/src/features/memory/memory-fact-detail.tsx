// SPDX-License-Identifier: Apache-2.0

import type { MemoryDetailResponse } from "@mecatl-studio/contracts";
import {
  getRuntimeOptions,
  getUserMemoryOptions,
  listUserMemoryOptions,
} from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { PageShell } from "../../components/shell/page-shell";
import { Button } from "../../components/ui/button";
import { pageTitleClass } from "../../lib/typography";
import { formatRelativeTime } from "../chat/latest-chat";

type MemoryRevision = MemoryDetailResponse["current"];

/**
 * The memory fact detail page (`/workspace/memory?item=<key>`), outside the
 * Settings layout like the skill and schedule details. The list it backs out to
 * stays under Settings → Memory.
 *
 * SPEC: the index gates the page. A still-loading index reads "Loading…", an
 * unsupported store says memory is off with the daemon's reason, and a key the
 * settled index lacks (or the detail read answers 404 for) is "Memory not
 * found", recoverable through "Back to Memory". When the agent can't be reached
 * the not-found card says so instead of claiming the fact is gone.
 *
 * DECISION: read-only by construction. The agent curates memory itself, so the
 * footer names the two tools that change it instead of offering an editor.
 */
export function MemoryFactDetail({ memoryId }: { memoryId: string }) {
  const runtime = useQuery(getRuntimeOptions());
  const connected = runtime.data?.connection === "online";
  const list = useQuery({ ...listUserMemoryOptions(), enabled: connected });
  const detail = useQuery({
    ...getUserMemoryOptions({ path: { memoryKey: memoryId } }),
    enabled: connected && memoryId !== "",
  });

  const supported = list.data?.supported ?? true;
  const entry = list.data?.supported
    ? list.data.items.find((item) => item.key === memoryId)
    : undefined;

  if (!entry || isNotFound(detail.error)) {
    if (runtime.isPending || (connected && list.isPending)) {
      return (
        <div className="flex min-h-[40vh] items-center justify-center text-sm text-muted-foreground">
          Loading…
        </div>
      );
    }
    if (!supported) {
      return (
        <div className="flex min-h-[40vh] flex-col items-center justify-center gap-2 px-6 text-center">
          <p className="text-sm font-medium">Memory is turned off for this agent.</p>
          {list.data?.reason && <p className="text-sm text-muted-foreground">{list.data.reason}</p>}
        </div>
      );
    }
    const reachable = connected && !list.isError;
    return (
      <div className="flex min-h-[60vh] flex-col items-center justify-center px-4">
        <div className="w-full max-w-md rounded-xl border bg-card p-8 text-center">
          <h1 className="text-lg font-semibold">Memory not found</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            {reachable ? (
              <>
                No remembered fact has the key <code className="font-mono text-xs">{memoryId}</code>{" "}
                — the agent may have forgotten or renamed it.
              </>
            ) : (
              "The agent can't be reached, so this fact can't be looked up right now."
            )}
          </p>
          <Button asChild className="mt-6">
            <Link
              params={{ section: "memory" }}
              search={{ item: undefined }}
              to="/workspace/settings/$section"
            >
              Back to Memory
            </Link>
          </Button>
        </div>
      </div>
    );
  }

  const current = detail.data?.current ?? null;
  const description = current?.description || entry.description;

  return (
    <PageShell className="space-y-5">
      <Button
        asChild
        className="h-9 w-fit gap-1 self-start rounded-full px-4"
        size="sm"
        variant="outline"
      >
        <Link
          params={{ section: "memory" }}
          search={{ item: undefined }}
          to="/workspace/settings/$section"
        >
          <span aria-hidden="true">‹</span>
          Back
        </Link>
      </Button>

      <h1 className={pageTitleClass("break-all text-[44px] leading-[1.05] max-[499px]:text-3xl")}>
        {entry.key}
      </h1>

      <div className="max-w-4xl space-y-8">
        <section
          aria-label="Value"
          className="rounded-xl border bg-card p-5 text-sm leading-relaxed"
        >
          {detail.isPending ? (
            <p className="text-muted-foreground">Loading value…</p>
          ) : detail.isError ? (
            <p className="text-destructive">Value unavailable: {errorMessage(detail.error)}</p>
          ) : (
            <pre className="whitespace-pre-wrap break-words font-mono text-sm">
              {current?.value || "(empty value)"}
            </pre>
          )}
          {description && (
            <p className="mt-3 whitespace-pre-wrap text-muted-foreground">{description}</p>
          )}
        </section>

        <div className="space-y-2">
          <h2 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            Details
          </h2>
          <div className="divide-y rounded-lg border bg-background">
            <FactRow label="Key" mono>
              {memoryId}
            </FactRow>
            {current && <RevisionRows revision={current} />}
          </div>
        </div>

        {detail.data && <HistorySection detail={detail.data} />}

        <p className="text-xs text-muted-foreground">
          Read-only — ask the agent to use ForgetUserMemory or UndoUserMemory.
        </p>
      </div>
    </PageShell>
  );
}

/** The provenance rows of the current revision; an empty field has no row. */
function RevisionRows({ revision }: { revision: MemoryRevision }) {
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
            className="hover:underline"
            search={{ sessionId: revision.sourceSessionId }}
            to="/workspace/chat"
          >
            {revision.sourceSessionId}
          </Link>
        </FactRow>
      )}
      {revision.updatedAt && (
        <FactRow label="Updated">
          <time dateTime={revision.updatedAt} title={revision.updatedAt}>
            {formatRelativeTime(revision.updatedAt)} ago
          </time>
        </FactRow>
      )}
    </>
  );
}

/**
 * The bounded prior revisions, in the order the daemon delivered them (newest
 * first). A store/driver that supplied only the current value says so, which is
 * a different fact from "no prior revisions".
 */
function HistorySection({ detail }: { detail: MemoryDetailResponse }) {
  return (
    <section aria-label="History" className="space-y-2">
      <h2 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        History
      </h2>
      {detail.historyAvailable ? (
        <div className="rounded-lg border bg-background">
          <p className="px-4 py-3 text-sm text-muted-foreground">
            {detail.history.length}{" "}
            {detail.history.length === 1 ? "bounded revision" : "bounded revisions"}
          </p>
          {detail.history.length > 0 && (
            <ul className="divide-y border-t">
              {detail.history.map((prior) => (
                <li
                  className="px-4 py-3 font-mono text-sm"
                  key={`${prior.version}:${prior.status}:${prior.updatedAt}`}
                >
                  {prior.version || "?"} · {prior.status || "?"} ·{" "}
                  {prior.updatedAt ? prior.updatedAt.slice(0, 10) : "—"}
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
  children,
  label,
  mono = false,
}: {
  children: ReactNode;
  label: string;
  mono?: boolean;
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

function isNotFound(error: unknown) {
  return typeof error === "object" && error !== null && "status" in error && error.status === 404;
}

function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "The request could not be completed.";
}
