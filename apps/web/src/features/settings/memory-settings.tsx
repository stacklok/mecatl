// SPDX-License-Identifier: Apache-2.0

import type { GetUserMemoryResponse } from "@mecatl-studio/contracts/generated";
import { getUserMemoryOptions, listUserMemoryOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Badge } from "../../components/ui/badge";
import { Modal, StateCard } from "../knowledge/knowledge-workspace";
import { MemoryConsolidation } from "../knowledge/memory-consolidation";

/** Settings → Memory: the facts the agent has remembered about this user. */
export function MemorySettings({
  onSelect,
  selectedKey,
}: {
  onSelect: (key?: string) => void;
  selectedKey?: string;
}) {
  const query = useQuery(listUserMemoryOptions());
  if (query.isPending) return <StateCard text="Loading memory…" />;
  if (query.isError) return <StateCard error text={errorMessage(query.error)} />;
  if (!query.data.supported)
    return <StateCard text={query.data.reason} title="Memory is disabled" />;
  if (query.data.items.length === 0)
    return (
      <StateCard
        icon="memory"
        text="The agent has not stored any durable facts for this user yet."
        title="Nothing remembered yet"
      />
    );
  return (
    <>
      <MemoryConsolidation />
      <div className="mb-3 flex flex-wrap gap-3 text-xs text-muted-foreground">
        <span>
          {query.data.items.length} remembered fact{query.data.items.length === 1 ? "" : "s"}
        </span>
        <span>{query.data.sizeBytes} bytes</span>
      </div>
      <div className="divide-y overflow-hidden rounded-xl border bg-card">
        {query.data.items.map((entry) => (
          <button
            className="block w-full p-4 text-left hover:bg-muted/40"
            key={entry.key}
            onClick={() => onSelect(entry.key)}
            type="button"
          >
            <span className="font-medium">{entry.key}</span>
            <span className="mt-1 block text-sm text-muted-foreground">
              {entry.description || "No description recorded."}
            </span>
          </button>
        ))}
      </div>
      {selectedKey && <MemoryDialog memoryKey={selectedKey} onClose={() => onSelect(undefined)} />}
    </>
  );
}

function MemoryDialog({ memoryKey, onClose }: { memoryKey: string; onClose: () => void }) {
  const query = useQuery(getUserMemoryOptions({ path: { memoryKey } }));
  return (
    <Modal onClose={onClose}>
      {query.isPending ? (
        <p className="text-sm text-muted-foreground">Loading memory…</p>
      ) : query.isError ? (
        <p className="text-sm text-destructive">{errorMessage(query.error)}</p>
      ) : (
        <MemoryDetail detail={query.data} />
      )}
    </Modal>
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
      <p className="mt-4 whitespace-pre-wrap rounded-lg border bg-card p-4 text-sm leading-6">
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
          <summary className="cursor-pointer text-sm font-medium">
            Revision history ({detail.history.length})
          </summary>
          <ul className="mt-3 space-y-2">
            {detail.history.map((revision) => (
              <li
                className="rounded bg-muted p-3 text-xs"
                key={`${revision.key}-${revision.version}`}
              >
                <span className="font-mono">{revision.version}</span>
                <p className="mt-1 whitespace-pre-wrap">{revision.value}</p>
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
