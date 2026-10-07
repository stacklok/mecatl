// SPDX-License-Identifier: Apache-2.0

import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { cn } from "../../lib/utils";
import { TableHead } from "./table";

export type SortDirection = "asc" | "desc";

/** A column header that reports clicks and exposes its state through `aria-sort`. */
export function SortableHead({
  className,
  direction,
  label,
  onSort,
}: {
  className?: string;
  /** The active direction, or undefined when this column is not the sorted one. */
  direction?: SortDirection;
  label: string;
  onSort: () => void;
}) {
  const Icon = direction === "asc" ? ArrowUp : direction === "desc" ? ArrowDown : ChevronsUpDown;
  const ariaSort = { asc: "ascending", desc: "descending" } as const;
  return (
    <TableHead aria-sort={direction ? ariaSort[direction] : "none"} className={className}>
      <button
        className={cn(
          "inline-flex items-center gap-1 hover:text-foreground",
          !direction && "text-muted-foreground",
        )}
        onClick={onSort}
        type="button"
      >
        {label}
        <Icon aria-hidden="true" className="size-3.5" />
      </button>
    </TableHead>
  );
}
