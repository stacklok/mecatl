// SPDX-License-Identifier: Apache-2.0

import { computeLineDiff } from "../chat/edit-diff";

export function DiffBlock({ rows }: { rows: SkillDiffRow[] }) {
  return (
    <pre className="mt-3 max-h-[34rem] overflow-auto rounded-xl border bg-card py-2 font-mono text-xs">
      {rows.map((row, index) => (
        <span
          className={`block min-h-5 whitespace-pre-wrap px-4 ${diffRowClass(row.kind)}`}
          // biome-ignore lint/suspicious/noArrayIndexKey: diff rows are positional
          key={index}
        >
          {row.kind === "metadata" ? row.text || " " : `${diffRowMarker(row.kind)} ${row.text}`}
        </span>
      ))}
    </pre>
  );
}

type DiffRowKind = "addition" | "deletion" | "metadata" | "context";

export interface SkillDiffRow {
  kind: DiffRowKind;
  /** The line content without its diff marker (metadata rows carry their full header). */
  text: string;
}

export function diffLineKind(line: string): DiffRowKind {
  if (line.startsWith("+") && !line.startsWith("+++")) return "addition";
  if (line.startsWith("-") && !line.startsWith("---")) return "deletion";
  if (line.startsWith("@@") || line.startsWith("+++") || line.startsWith("---")) return "metadata";
  return "context";
}

/** The current (to-version) text, used to find where the old text ends in the daemon's diff. */
export interface SkillDiffTarget {
  body?: string;
  description?: string;
}

const DIFF_HEADER = /^--- ([^\n]*)\n\+\+\+ ([^\n]*)\n@@ description @@\n/;
const BODY_MARKER = "\n@@ body @@\n";

/**
 * Turns the daemon's learned-skill version diff into display rows.
 *
 * The daemon does not send a line diff. It sends each field whole:
 *
 *     --- <from>\n+++ <to>\n@@ description @@\n-<old description>\n+<new description>
 *     \n@@ body @@\n-<old body>\n+<new body>\n
 *
 * so only the first line of each old/new text carries a marker, and a body line
 * such as a markdown bullet (`- item`) would read as a deletion. This recovers
 * the old and new texts and computes a real line diff between them. Text that
 * does not have that shape is classified line by line as a unified diff.
 */
export function skillDiffRows(diff: string, target: SkillDiffTarget = {}): SkillDiffRow[] {
  const parsed = parseDaemonSkillDiff(diff, target);
  if (!parsed) return diff.split("\n").map(unifiedDiffRow);
  return [
    { kind: "metadata", text: `--- ${parsed.from}` },
    { kind: "metadata", text: `+++ ${parsed.to}` },
    { kind: "metadata", text: "@@ description @@" },
    ...lineDiffRows(parsed.description.before, parsed.description.after),
    { kind: "metadata", text: "@@ body @@" },
    ...lineDiffRows(parsed.body.before, parsed.body.after),
  ];
}

/** True when the rows record at least one added or removed line. */
export function hasSkillDiffChanges(rows: SkillDiffRow[]) {
  return rows.some((row) => row.kind === "addition" || row.kind === "deletion");
}

function unifiedDiffRow(line: string): SkillDiffRow {
  const kind = diffLineKind(line);
  if (kind === "metadata") return { kind, text: line };
  if (kind === "context") return { kind, text: line.startsWith(" ") ? line.slice(1) : line };
  return { kind, text: line.slice(1) };
}

function lineDiffRows(before: string, after: string): SkillDiffRow[] {
  return computeLineDiff(before, after).lines.map((line) => ({
    kind: line.kind === "added" ? "addition" : line.kind === "removed" ? "deletion" : "context",
    text: line.text,
  }));
}

interface TextPair {
  after: string;
  before: string;
}

function parseDaemonSkillDiff(diff: string, target: SkillDiffTarget) {
  const header = DIFF_HEADER.exec(diff);
  if (!header) return null;
  const rest = diff.slice(header[0].length);
  const bodyAt = rest.indexOf(BODY_MARKER);
  if (bodyAt < 0) return null;
  let bodySection = rest.slice(bodyAt + BODY_MARKER.length);
  // The format terminates the new body with one newline of its own.
  if (bodySection.endsWith("\n")) bodySection = bodySection.slice(0, -1);
  const description = splitOldNew(rest.slice(0, bodyAt), target.description);
  const body = splitOldNew(bodySection, target.body);
  if (!description || !body) return null;
  return { body, description, from: header[1] ?? "", to: header[2] ?? "" };
}

/**
 * Splits `-<old>\n+<new>` into its two texts. The old text may itself contain a
 * line starting with `+`, so every `\n+` is a candidate boundary; the one whose
 * remainder is the known current text wins (exactly, else as a prefix when the
 * daemon truncated a long body), falling back to the first boundary.
 */
function splitOldNew(section: string, current: string | undefined): TextPair | null {
  if (!section.startsWith("-")) return null;
  const text = section.slice(1);
  const boundaries: number[] = [];
  for (let at = text.indexOf("\n+"); at >= 0; at = text.indexOf("\n+", at + 1)) {
    boundaries.push(at);
  }
  const first = boundaries[0];
  if (first === undefined) return null;
  const after = (at: number) => text.slice(at + 2);
  let chosen = first;
  if (current !== undefined) {
    const exact = boundaries.find((at) => after(at) === current);
    const prefix = boundaries.find((at) => current.startsWith(after(at).replace(/�$/, "")));
    chosen = exact ?? prefix ?? first;
  }
  return { after: after(chosen), before: text.slice(0, chosen) };
}

function diffRowMarker(kind: DiffRowKind) {
  if (kind === "addition") return "+";
  if (kind === "deletion") return "-";
  return " ";
}

function diffRowClass(kind: DiffRowKind) {
  if (kind === "addition") return "bg-success/10 text-foreground";
  if (kind === "deletion") return "bg-destructive/10 text-foreground";
  if (kind === "metadata") return "text-muted-foreground";
  return "";
}
