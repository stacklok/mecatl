/**
 * A bounded line diff for the approval card's Edit/Write preview: the
 * before/after texts of an ask are compared line by line (longest common
 * subsequence), so the operator reads what changes rather than two blobs.
 */

export type LineDiffOp = { type: "same" | "add" | "del"; text: string };

/**
 * Inputs whose combined line count exceeds this return `null` from
 * `diffLines`: the O(n·m) alignment is not worth running on a multi-thousand
 * line rewrite, and the caller shows a before/after pair instead.
 */
export const LINE_DIFF_MAX_LINES = 2000;

/** Splits on "\n"; the empty text has NO lines (a deletion, not one blank line). */
function splitLines(text: string): string[] {
  if (text === "") return [];
  return text.split("\n");
}

/**
 * Line-level diff of `before` → `after`. Returns the ordered ops (`same`
 * lines interleaved with `del`/`add` runs), or `null` when the inputs
 * together exceed `LINE_DIFF_MAX_LINES`.
 */
export function diffLines(before: string, after: string): LineDiffOp[] | null {
  const a = splitLines(before);
  const b = splitLines(after);
  if (a.length + b.length > LINE_DIFF_MAX_LINES) return null;

  // Trim the common prefix and suffix first: most edits touch a small window
  // of a large block, and the DP table only needs to cover that window.
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) {
    start += 1;
  }
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA -= 1;
    endB -= 1;
  }

  const ops: LineDiffOp[] = [];
  for (let i = 0; i < start; i += 1) ops.push({ type: "same", text: a[i] });
  ops.push(...lcsOps(a.slice(start, endA), b.slice(start, endB)));
  for (let i = endA; i < a.length; i += 1) {
    ops.push({ type: "same", text: a[i] });
  }
  return ops;
}

/** Classic LCS table over the trimmed middle, walked back into ops. */
function lcsOps(a: string[], b: string[]): LineDiffOp[] {
  const n = a.length;
  const m = b.length;
  if (n === 0) return b.map((text) => ({ type: "add", text }));
  if (m === 0) return a.map((text) => ({ type: "del", text }));

  // table[i][j] = LCS length of a[i..] and b[j..], stored flat.
  const width = m + 1;
  const table = new Uint16Array((n + 1) * width);
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      table[i * width + j] =
        a[i] === b[j]
          ? table[(i + 1) * width + j + 1] + 1
          : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
    }
  }

  const ops: LineDiffOp[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      ops.push({ type: "same", text: a[i] });
      i += 1;
      j += 1;
    } else if (table[(i + 1) * width + j] >= table[i * width + j + 1]) {
      ops.push({ type: "del", text: a[i] });
      i += 1;
    } else {
      ops.push({ type: "add", text: b[j] });
      j += 1;
    }
  }
  while (i < n) {
    ops.push({ type: "del", text: a[i] });
    i += 1;
  }
  while (j < m) {
    ops.push({ type: "add", text: b[j] });
    j += 1;
  }
  return ops;
}
