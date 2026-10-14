// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { afterEach, describe, expect, it } from "vitest";
import { selectedTranscriptText } from "./selected-transcript-text";

afterEach(() => {
  document.body.replaceChildren();
});

function setup() {
  const root = document.createElement("section");
  root.innerHTML =
    '<p>Ran <span class="sr-only" id="a">completed</span><span class="sr-only" id="b">Tool: Bash</span> ls</p><span class="sr-only" id="c">elsewhere</span>';
  document.body.append(root);
  const range = document.createRange();
  range.selectNodeContents(root.querySelector("p") as Node);
  const selection = window.getSelection() as Selection;
  selection.removeAllRanges();
  selection.addRange(range);
  return { root, selection };
}

const hiddenOf = (root: Element, id: string) =>
  Boolean((root.querySelector(`#${id}`) as HTMLElement).hidden);

describe("selectedTranscriptText", () => {
  it("hides only the screen-reader spans the selection crosses while reading, then restores them", () => {
    const { root, selection } = setup();
    const seen: Record<string, boolean> = {};
    // happy-dom's partial containment is an OR of the two edges, so stand in for a browser.
    selection.containsNode = (node) => (node as HTMLElement).id !== "c";
    selection.toString = () => {
      for (const id of ["a", "b", "c"]) seen[id] = hiddenOf(root, id);
      return "  Ran ls  ";
    };
    expect(selectedTranscriptText(selection, root)).toBe("Ran ls");
    expect(seen).toEqual({ a: true, b: true, c: false });
    for (const id of ["a", "b", "c"]) expect(hiddenOf(root, id)).toBe(false);
  });

  it("restores the spans when reading the selection throws", () => {
    const { root, selection } = setup();
    selection.toString = () => {
      throw new Error("boom");
    };
    expect(() => selectedTranscriptText(selection, root)).toThrow("boom");
    expect(hiddenOf(root, "a")).toBe(false);
  });
});
