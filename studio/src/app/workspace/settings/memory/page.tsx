"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo } from "react";
import {
  directed,
  SortableHead,
  useTableSort,
} from "@/components/sortable-head";
import {
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { type MemoryEntry, useAgentMemory } from "@/features/agent";
import { Note, SettingsCard } from "../_components/settings-card";
import { ConsolidateMemoryCard } from "./_components/consolidate-memory";
import { MemoryFootprint } from "./_components/memory-footprint";
import { MemoryStoresCard } from "./_components/memory-stores-card";

/**
 * Settings → Memory: the two memory switches, the facts the agent has
 * remembered about you (read-only — the agent curates them, and Studio never
 * composes memory content, memory rule 8), and the consolidation card. The
 * consolidation card gates itself on the agent's capabilities and project
 * memory can be consolidated with facts about you off, so it renders in
 * every state the facts card is in.
 */
export default function MemorySettingsPage() {
  return (
    <>
      <MemoryStoresCard />
      <FactsAboutYouCard />
      <ConsolidateMemoryCard />
    </>
  );
}

function FactsAboutYouCard() {
  const memory = useAgentMemory();
  const sort = useTableSort<"name" | "remembers">("name");

  const entries = useMemo(() => {
    const byName = (a: MemoryEntry, b: MemoryEntry) =>
      a.title.localeCompare(b.title);
    const primary = (a: MemoryEntry, b: MemoryEntry) =>
      sort.key === "remembers"
        ? (a.content || "").localeCompare(b.content || "")
        : byName(a, b);
    return [...memory.entries].sort(
      (a, b) => directed(sort.dir, primary(a, b)) || byName(a, b),
    );
  }, [memory.entries, sort.key, sort.dir]);

  let body: React.ReactNode;
  if (!memory.isSupported) {
    body = <Note>Facts about you is off, so there is nothing to show.</Note>;
  } else if (memory.isLoading) {
    body = <Note>Loading memory…</Note>;
  } else if (entries.length === 0) {
    body = (
      <Note>The agent hasn&apos;t remembered anything about you yet.</Note>
    );
  } else {
    body = (
      <div className="space-y-3">
        <MemoryFootprint store={memory.store} />
        <div className="overflow-hidden rounded-lg border">
          <Table>
            <TableHeader className="max-[499px]:hidden">
              <TableRow className="hover:bg-transparent">
                <SortableHead
                  label="Name"
                  sortKey="name"
                  sort={sort}
                  className="w-[280px] lg:w-[320px]"
                />
                <SortableHead
                  label="Remembers"
                  sortKey="remembers"
                  sort={sort}
                />
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.map((entry) => (
                <MemoryRow key={entry.id} entry={entry} />
              ))}
            </TableBody>
          </Table>
        </div>
      </div>
    );
  }

  return (
    <SettingsCard
      title="Facts about you"
      description="What the agent has remembered about you. Select a fact to see more."
    >
      {body}
    </SettingsCard>
  );
}

/** One fact per row; the whole row opens the dedicated detail page. */
function MemoryRow({ entry }: { entry: MemoryEntry }) {
  const router = useRouter();
  const href = `/workspace/memory/${encodeURIComponent(entry.id)}`;

  return (
    <TableRow className="cursor-pointer" onClick={() => router.push(href)}>
      <TableCell className="max-w-0 max-[499px]:w-full">
        <Link
          href={href}
          onClick={(e) => e.stopPropagation()}
          className="block truncate text-sm font-medium hover:underline"
        >
          {entry.title}
        </Link>
        {/* Mobile collapses to a single stacked cell, like the other tables. */}
        <p className="line-clamp-2 text-xs text-muted-foreground min-[500px]:hidden">
          {entry.content || "No description recorded."}
        </p>
      </TableCell>
      <TableCell className="max-w-0 max-[499px]:hidden">
        <p className="line-clamp-1 text-xs whitespace-normal text-muted-foreground">
          {entry.content || "No description recorded."}
        </p>
      </TableCell>
    </TableRow>
  );
}
