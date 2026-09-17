import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  COMPOSER_DEFAULT_MIN_ROWS,
  COMPOSER_MAX_ROWS,
  COMPOSER_MIN_ROWS_VAR,
} from "./composer-frame";

/**
 * The composer's growth policy lives in the stylesheet, not in React: the
 * TipTap editor rests at three rows, grows with content to eight, then
 * scrolls inside itself; the docked mobile composer pins one row. jsdom lays
 * nothing out, so this reads `globals.css` mechanically and checks the rule
 * says what `composer-frame.ts` (the wrapper's custom property) assumes.
 */

const css = readFileSync(
  resolve(import.meta.dirname, "../../globals.css"),
  "utf8",
);

/** The declarations of the first `selector { … }` block after `from`. */
function ruleBody(selector: string, from = 0): string {
  const at = css.indexOf(`${selector} {`, from);
  if (at < 0) throw new Error(`no rule for ${selector}`);
  const open = css.indexOf("{", at);
  const close = css.indexOf("}", open);
  return css.slice(open + 1, close);
}

/** Every `@media (max-width: 499px) { … }` block, braces balanced. */
function mobileBlocks(): string[] {
  const blocks: string[] = [];
  const needle = "@media (max-width: 499px) {";
  let from = 0;
  for (;;) {
    const at = css.indexOf(needle, from);
    if (at < 0) return blocks;
    let depth = 0;
    let i = css.indexOf("{", at);
    for (; i < css.length; i++) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}" && --depth === 0) break;
    }
    blocks.push(css.slice(at, i + 1));
    from = i + 1;
  }
}

describe(".composer-editor .ProseMirror growth policy", () => {
  const body = ruleBody(".composer-editor .ProseMirror");

  it("rests at the default row count read from the wrapper's custom property", () => {
    expect(body).toContain(
      `min-height: calc(var(${COMPOSER_MIN_ROWS_VAR}, ${COMPOSER_DEFAULT_MIN_ROWS}) * 1.5rem)`,
    );
  });

  it("caps growth at eight rows and scrolls inside itself past that", () => {
    expect(body).toContain(`max-height: calc(${COMPOSER_MAX_ROWS} * 1.5rem)`);
    expect(body).toContain("overflow-y: auto");
  });

  it("maps rows 1:1 onto the line-height the calc multiplies", () => {
    expect(body).toContain("line-height: 1.5rem");
  });
});

describe("the docked mobile composer", () => {
  it("pins one resting row on the editor element itself, inside the ≤499px media block", () => {
    const pin = mobileBlocks().find((block) =>
      block.includes(".composer-editor .ProseMirror"),
    );
    expect(
      pin,
      "a mobile media block scoping the composer editor",
    ).toBeDefined();
    expect(pin).toContain(`${COMPOSER_MIN_ROWS_VAR}: 1`);
  });
});
