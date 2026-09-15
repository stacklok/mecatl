"use client";

import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { useCallback, useState } from "react";
import { TableHead } from "@/components/ui/table";
import { cn } from "@/lib/utils";

export type SortDir = "asc" | "desc";

export interface TableSort<K extends string> {
  key: K;
  dir: SortDir;
  toggle: (key: K) => void;
}

/**
 * Column-header sort state for a table: clicking a new column sorts ascending,
 * clicking the active column flips direction.
 */
export function useTableSort<K extends string>(
  defaultKey: K,
  defaultDir: SortDir = "asc",
): TableSort<K> {
  const [key, setKey] = useState<K>(defaultKey);
  const [dir, setDir] = useState<SortDir>(defaultDir);
  const toggle = useCallback(
    (next: K) => {
      if (next === key) {
        setDir((d) => (d === "asc" ? "desc" : "asc"));
        return;
      }
      setKey(next);
      setDir("asc");
    },
    [key],
  );
  return { key, dir, toggle };
}

/** Applies the sort direction to a comparator result. */
export function directed(dir: SortDir, cmp: number): number {
  return dir === "asc" ? cmp : -cmp;
}

/** A table header cell that drives a TableSort when clicked. */
export function SortableHead<K extends string>({
  label,
  sortKey,
  sort,
  className,
}: {
  label: string;
  sortKey: K;
  sort: TableSort<K>;
  className?: string;
}) {
  const active = sort.key === sortKey;
  const Icon = active
    ? sort.dir === "asc"
      ? ArrowUp
      : ArrowDown
    : ChevronsUpDown;
  return (
    <TableHead className={className}>
      <button
        type="button"
        onClick={() => sort.toggle(sortKey)}
        className={cn(
          "inline-flex items-center gap-1 transition-colors hover:text-foreground",
          active && "text-foreground",
        )}
      >
        {label}
        <Icon
          className={cn("size-3.5", !active && "text-muted-foreground/60")}
          aria-hidden="true"
        />
      </button>
    </TableHead>
  );
}
