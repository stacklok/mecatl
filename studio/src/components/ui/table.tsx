// SPDX-License-Identifier: Apache-2.0

"use client";

import type * as React from "react";
import { createContext, type ReactNode, useContext, useMemo } from "react";
import { Button } from "@/components/ui/button";
import { ChevronIndicator } from "@/components/ui/chevron-indicator";
import { cn } from "@/lib/utils";

function Table({
  className,
  containerClassName,
  ...props
}: React.ComponentProps<"table"> & { containerClassName?: string }) {
  return (
    <div
      data-slot="table-container"
      className={cn("relative w-full overflow-x-auto", containerClassName)}
    >
      <table
        data-slot="table"
        className={cn("w-full caption-bottom text-sm", className)}
        {...props}
      />
    </div>
  );
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
  return (
    <thead
      data-slot="table-header"
      className={cn("[&_tr]:border-b", className)}
      {...props}
    />
  );
}

function TableBody({ className, ...props }: React.ComponentProps<"tbody">) {
  return (
    <tbody
      data-slot="table-body"
      className={cn("[&_tr:last-child]:border-0", className)}
      {...props}
    />
  );
}

function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) {
  return (
    <tfoot
      data-slot="table-footer"
      className={cn(
        "bg-muted/50 border-t font-medium [&>tr]:last:border-b-0",
        className,
      )}
      {...props}
    />
  );
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      className={cn(
        "hover:bg-muted/50 data-[state=selected]:bg-muted border-b transition-colors",
        className,
      )}
      {...props}
    />
  );
}

function TableHead({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        "text-foreground h-10 px-4 text-left align-middle font-medium whitespace-nowrap [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        className,
      )}
      {...props}
    />
  );
}

function TableCell({ className, ...props }: React.ComponentProps<"td">) {
  return (
    <td
      data-slot="table-cell"
      className={cn(
        "px-4 py-3 align-middle whitespace-nowrap [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        className,
      )}
      {...props}
    />
  );
}

function TableCaption({
  className,
  ...props
}: React.ComponentProps<"caption">) {
  return (
    <caption
      data-slot="table-caption"
      className={cn("text-muted-foreground mt-4 text-sm", className)}
      {...props}
    />
  );
}

// Compound primitive for a parent TableRow that can reveal collapsible
// sub-rows beneath it. State is controlled by the parent; children
// (`NestedTableRowTrigger`, `CollapsibleTableRows`) auto-wire via context.
// When `isCollapsible` is false, both children render nothing — call sites
// can use this component uniformly for rows with or without sub-rows.

type NestedTableRowContextValue = {
  isExpanded: boolean;
  onExpandedChange: (open: boolean) => void;
  isCollapsible: boolean;
  colSpan: number;
};

const NestedTableRowContext = createContext<NestedTableRowContextValue | null>(
  null,
);

function useNestedTableRow(componentName: string): NestedTableRowContextValue {
  const ctx = useContext(NestedTableRowContext);
  if (!ctx) {
    throw new Error(`${componentName} must be used inside <NestedTableRow>`);
  }
  return ctx;
}

function NestedTableRow({
  isExpanded,
  onExpandedChange,
  isCollapsible = true,
  colSpan,
  children,
}: {
  isExpanded: boolean;
  onExpandedChange: (open: boolean) => void;
  isCollapsible?: boolean;
  colSpan: number;
  children: ReactNode;
}) {
  const value = useMemo(
    () => ({ isExpanded, onExpandedChange, isCollapsible, colSpan }),
    [isExpanded, onExpandedChange, isCollapsible, colSpan],
  );
  return (
    <NestedTableRowContext.Provider value={value}>
      {children}
    </NestedTableRowContext.Provider>
  );
}

function NestedTableRowTrigger({
  expandLabel = "Expand",
  collapseLabel = "Collapse",
  className,
  ...props
}: {
  expandLabel?: string;
  collapseLabel?: string;
} & Omit<
  React.ComponentProps<typeof Button>,
  "aria-label" | "aria-expanded" | "onClick"
>) {
  const { isExpanded, onExpandedChange, isCollapsible } = useNestedTableRow(
    "NestedTableRowTrigger",
  );
  if (!isCollapsible) return null;
  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn("size-6", className)}
      aria-expanded={isExpanded}
      aria-label={isExpanded ? collapseLabel : expandLabel}
      onClick={() => onExpandedChange(!isExpanded)}
      {...props}
    >
      <ChevronIndicator isOpen={isExpanded} className="size-3" />
    </Button>
  );
}

// Intentionally narrow: animated sub-rows that appear under an expandable
// parent row. If arbitrary (non-tabular) detail content is needed later, the
// animated <tr><td colSpan> scaffolding can be extracted as a generic
// primitive and this component rebuilt to compose it around the nested
// <table>/<tbody>. Existing call sites stay unchanged.
function CollapsibleTableRows({
  colWidths,
  children,
  className,
  ...props
}: React.ComponentProps<"tr"> & {
  colWidths?: ReadonlyArray<string | undefined>;
}) {
  const { isExpanded, isCollapsible, colSpan } = useNestedTableRow(
    "CollapsibleTableRows",
  );
  if (!isCollapsible) return null;
  return (
    <tr
      data-slot="table-collapsible-rows"
      className={cn(
        "hover:bg-transparent transition-colors",
        isExpanded && "border-b",
        className,
      )}
      {...props}
    >
      <td colSpan={colSpan} className="p-0">
        <div
          className="grid transition-[grid-template-rows] duration-200 ease-out motion-reduce:transition-none"
          style={{ gridTemplateRows: isExpanded ? "1fr" : "0fr" }}
        >
          <div className="min-h-0 overflow-hidden" inert={!isExpanded}>
            <table className="w-full table-fixed">
              {colWidths && (
                <colgroup>
                  {colWidths.map((w, i) => (
                    <col
                      // biome-ignore lint/suspicious/noArrayIndexKey: column positions are fixed
                      key={i}
                      style={w ? { width: w } : undefined}
                    />
                  ))}
                </colgroup>
              )}
              <tbody>{children}</tbody>
            </table>
          </div>
        </div>
      </td>
    </tr>
  );
}

export {
  CollapsibleTableRows,
  NestedTableRow,
  NestedTableRowTrigger,
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableFooter,
  TableHead,
  TableHeader,
  TableRow,
};
