// SPDX-License-Identifier: Apache-2.0

/**
 * The selected text without the screen-reader-only spans ("completed", "Tool:
 * Bash") the selection crosses. `Selection.toString()` reads rendered text, so
 * hiding those spans for the duration of one synchronous read keeps them out
 * without a paint in between.
 */
export function selectedTranscriptText(selection: Selection, root: Element): string {
  const hidden = [...root.querySelectorAll<HTMLElement>(".sr-only")].filter((element) =>
    selection.containsNode(element, true),
  );
  for (const element of hidden) element.hidden = true;
  try {
    return selection.toString().trim();
  } finally {
    for (const element of hidden) element.hidden = false;
  }
}
