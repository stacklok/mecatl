// SPDX-License-Identifier: Apache-2.0

import type { GetUserMemoryResponse } from "@mecatl-studio/contracts/generated";
import { getUserMemoryOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PageShell } from "../../components/shell/page-shell";
import { Badge } from "../../components/ui/badge";
import { ConsolidateMemoryCard } from "../memory/consolidate-memory-card";
import { FactsAboutYou } from "../memory/facts-about-you";

/**
 * Settings → Memory below the stores card: the remembered facts, then the
 * consolidation review, which hides itself when the agent reports no
 * consolidation target at all.
 */
export function MemorySettings() {
  return (
    <>
      <FactsAboutYou />
      <ConsolidateMemoryCard />
    </>
  );
}

/** A stable, reloadable detail page for a single exact memory key. */
export function MemoryFactDetail({ memoryKey }: { memoryKey: string }) {
  const query = useQuery(getUserMemoryOptions({ path: { memoryKey } }));
  return (
    <PageShell className="max-w-3xl">
      <Link
        className="inline-flex min-h-11 items-center rounded-lg text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-brand"
        params={{ section: "memory" }}
        search={{ item: undefined }}
        to="/workspace/settings/$section"
      >
        ← Memory
      </Link>
      <h1 className="mt-5 text-3xl font-semibold tracking-tight">Memory fact</h1>
      <div className="mt-7 rounded-2xl border bg-card p-5 sm:p-6">
        {query.isPending ? (
          <p className="text-sm text-muted-foreground">Loading memory…</p>
        ) : query.isError ? (
          <p className="text-sm text-destructive">{errorMessage(query.error)}</p>
        ) : (
          <MemoryDetail detail={query.data} />
        )}
      </div>
    </PageShell>
  );
}

function MemoryDetail({ detail }: { detail: GetUserMemoryResponse }) {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="break-all text-lg font-semibold">{detail.current.key}</h2>
        {detail.current.status && <Badge variant="success">{detail.current.status}</Badge>}
      </div>
      <p className="mt-2 text-sm text-muted-foreground">{detail.current.description}</p>
      <p className="mt-4 whitespace-pre-wrap break-words rounded-lg border bg-card p-4 text-sm leading-6">
        {detail.current.value || "No value recorded."}
      </p>
      <dl className="mt-4 grid gap-2 text-xs text-muted-foreground">
        <div>
          <dt className="inline font-medium text-foreground">Origin: </dt>
          <dd className="inline">{detail.current.origin || "unknown"}</dd>
        </div>
        <div>
          <dt className="inline font-medium text-foreground">Writer: </dt>
          <dd className="inline">{detail.current.writer || "unknown"}</dd>
        </div>
        {detail.current.updatedAt && (
          <div>
            <dt className="inline font-medium text-foreground">Updated: </dt>
            <dd className="inline">{formatDate(detail.current.updatedAt)}</dd>
          </div>
        )}
      </dl>
      {detail.historyAvailable && detail.history.length > 0 && (
        <details className="mt-4 rounded-lg border p-3">
          <summary className="min-h-11 cursor-pointer py-3 text-sm font-medium focus-visible:outline-2 focus-visible:outline-brand">
            Revision history ({detail.history.length})
          </summary>
          <ul className="mt-3 space-y-2">
            {detail.history.map((revision) => (
              <li
                className="rounded bg-muted p-3 text-xs"
                key={`${revision.key}-${revision.version}`}
              >
                <span className="font-mono">{revision.version}</span>
                <p className="mt-1 whitespace-pre-wrap break-words">{revision.value}</p>
              </li>
            ))}
          </ul>
        </details>
      )}
    </>
  );
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
