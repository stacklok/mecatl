// SPDX-License-Identifier: Apache-2.0

import type { UserMemoryResponse } from "@mecatl-studio/contracts";
import { listUserMemoryOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { SortableHead, type SortDir, useTableSort } from "@/components/sortable-head";
import { Table, TableBody, TableCell, TableHeader, TableRow } from "../../components/ui/table";
import { Note, SettingsCard } from "../settings/settings-card";

type FactSortKey = "name" | "remembers";
type MemoryEntry = UserMemoryResponse["items"][number];

const NO_DESCRIPTION = "No description recorded.";

/**
 * Settings → Memory → Facts about you: what the agent has remembered about
 * this user, as a sortable table whose rows open the fact's detail page. The
 * agent writes these facts; Studio only reads them.
 */
export function FactsAboutYou() {
  const query = useQuery(listUserMemoryOptions());
  const sort = useTableSort<FactSortKey>("name");

  let body: React.ReactNode;
  if (query.isPending) {
    body = <Note role="status">Loading memory…</Note>;
  } else if (query.isError) {
    body = <Note role="alert">{errorMessage(query.error)}</Note>;
  } else if (!query.data.supported) {
    body = <Note>Facts about you is off, so there is nothing to show.</Note>;
  } else if (query.data.items.length === 0) {
    body = <Note>The agent hasn&rsquo;t remembered anything about you yet.</Note>;
  } else {
    const rows = sortFacts(query.data.items, sort.key, sort.dir);
    body = (
      <div className="space-y-3">
        <p className="text-xs text-muted-foreground" data-testid="memory-footprint">
          {footprint(rows.length)}
        </p>
        <div className="overflow-hidden rounded-lg border">
          <Table>
            <TableHeader className="max-[499px]:hidden">
              <TableRow className="hover:bg-transparent">
                <SortableHead
                  className="w-[280px] px-4 lg:w-[320px]"
                  label="Name"
                  sort={sort}
                  sortKey="name"
                />
                <SortableHead className="px-4" label="Remembers" sort={sort} sortKey="remembers" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((entry) => (
                <TableRow key={entry.key}>
                  <TableCell className="max-w-0 px-4 py-3 max-[499px]:w-full">
                    <Link
                      className="block truncate text-sm font-medium hover:underline"
                      search={{ item: entry.key }}
                      to="/workspace/memory"
                    >
                      {entry.key}
                    </Link>
                    {/* Mobile collapses to one stacked cell. */}
                    <p className="line-clamp-2 text-xs text-muted-foreground min-[500px]:hidden">
                      {entry.description || NO_DESCRIPTION}
                    </p>
                  </TableCell>
                  <TableCell className="max-w-0 px-4 py-3 max-[499px]:hidden">
                    <p className="line-clamp-1 whitespace-normal text-xs text-muted-foreground">
                      {entry.description || NO_DESCRIPTION}
                    </p>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </div>
    );
  }

  return <SettingsCard title="Facts about you">{body}</SettingsCard>;
}

function footprint(count: number) {
  return `${count} ${count === 1 ? "fact" : "facts"} remembered`;
}

function sortFacts(items: MemoryEntry[], key: FactSortKey, direction: SortDir) {
  const byName = (a: MemoryEntry, b: MemoryEntry) => a.key.localeCompare(b.key);
  const primary = (a: MemoryEntry, b: MemoryEntry) =>
    key === "remembers" ? (a.description || "").localeCompare(b.description || "") : byName(a, b);
  const sign = direction === "asc" ? 1 : -1;
  return [...items].sort((a, b) => sign * primary(a, b) || byName(a, b));
}

function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "Memory could not be loaded.";
}
