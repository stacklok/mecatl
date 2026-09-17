"use client";

import { diffLines, type LineDiffOp } from "@/lib/line-diff";
import { cn } from "@/lib/utils";

const GUTTER: Record<LineDiffOp["type"], string> = {
  same: " ",
  add: "+",
  del: "-",
};

const ROW_CLASS: Record<LineDiffOp["type"], string> = {
  same: "",
  add: "bg-success/10",
  del: "bg-destructive/10",
};

const GUTTER_CLASS: Record<LineDiffOp["type"], string> = {
  same: "text-muted-foreground/60",
  add: "text-success",
  del: "text-destructive",
};

/**
 * The before → after view of an Edit/Write ask: one row per line with a
 * +/- gutter, tinted for additions and deletions. Above the line bound the
 * diff steps aside for a labelled before/after pair.
 */
export function ArgsDiff({
  before,
  after,
  className,
}: {
  before: string;
  after: string;
  className?: string;
}) {
  const ops = diffLines(before, after);
  if (ops === null) {
    return (
      <div className={cn("flex flex-col gap-2 px-3 py-2", className)}>
        <p className="text-xs text-muted-foreground">
          Too many lines to compare side by side; showing before and after.
        </p>
        <DiffPairBlock label="Before" text={before} tone="del" />
        <DiffPairBlock label="After" text={after} tone="add" />
      </div>
    );
  }
  if (ops.length === 0) {
    return (
      <p className={cn("px-3 py-2 text-xs text-muted-foreground", className)}>
        (no text)
      </p>
    );
  }
  return (
    <div
      className={cn(
        "overflow-x-auto py-1 font-mono text-xs leading-relaxed",
        className,
      )}
    >
      {ops.map((op, index) => (
        <div
          // Ops are positional and immutable for a given before/after pair
          // (two identical rows are still distinct lines), so the index IS
          // the identity here.
          // biome-ignore lint/suspicious/noArrayIndexKey: positional diff rows
          key={`${index}-${op.type}`}
          data-diff={op.type}
          className={cn("flex min-w-max px-2", ROW_CLASS[op.type])}
        >
          <span
            className={cn(
              "w-4 shrink-0 select-none text-center",
              GUTTER_CLASS[op.type],
            )}
          >
            {GUTTER[op.type]}
          </span>
          <span className="whitespace-pre pl-1">{op.text}</span>
        </div>
      ))}
    </div>
  );
}

function DiffPairBlock({
  label,
  text,
  tone,
}: {
  label: string;
  text: string;
  tone: "add" | "del";
}) {
  return (
    <div>
      <p
        className={cn(
          "mb-1 text-[11px] font-medium uppercase tracking-wide",
          GUTTER_CLASS[tone],
        )}
      >
        {label}
      </p>
      <pre
        className={cn(
          "overflow-x-auto whitespace-pre rounded-md px-2 py-1 font-mono text-xs leading-relaxed",
          ROW_CLASS[tone],
        )}
      >
        {text || "(empty)"}
      </pre>
    </div>
  );
}
