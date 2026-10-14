// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { ApprovalDetailPanel } from "./approval-detail-panel";
import { ApprovalPanel, type ApprovalRequest } from "./approval-panel";
import { COMMAND_LINE_CAP } from "./ask-args-view";
import { argsHaveInvisibleCharacters, hasInvisibleCharacters } from "./invisible-characters";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  cleanup();
  window.getSelection()?.removeAllRanges();
});

/** Every code point #2259 names: the bidirectional controls, then the zero-width characters. */
const INVISIBLE_CODE_POINTS = [
  0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200b, 0x200c, 0x200d,
  0xfeff,
];

function code(codePoint: number): string {
  return `U+${codePoint.toString(16).toUpperCase().padStart(4, "0")}`;
}

function ask(args: string, tool = "Shell"): ApprovalRequest {
  return { args, askId: "ask-a", reason: "Needs a shell", tool };
}

function renderCard(args: string, tool = "Shell"): HTMLElement {
  render(<ApprovalPanel approval={ask(args, tool)} disabled={false} onRespond={() => {}} />);
  return screen.getByRole("region", { name: `Permission required: ${tool}` });
}

function onlyPre(element: HTMLElement): HTMLPreElement {
  const pres = element.querySelectorAll("pre");
  expect(pres).toHaveLength(1);
  return pres[0] as HTMLPreElement;
}

function lines(count: number, first = 1): string {
  return Array.from({ length: count }, (_, index) => `echo step ${first + index}`).join("\n");
}

function classes(element: Element | null): string[] {
  return element?.getAttribute("class")?.split(" ") ?? [];
}

function buttonNames(element: HTMLElement): string[] {
  return within(element)
    .getAllByRole("button")
    .map((button) => button.textContent ?? "");
}

/** What a reader selecting the whole element gets, as the browser's copy reads it. */
function selectAll(element: Element): string {
  const range = document.createRange();
  range.selectNodeContents(element);
  const selection = window.getSelection();
  if (!selection) throw new Error("no selection");
  selection.removeAllRanges();
  selection.addRange(range);
  return selection.toString();
}

/** The badge for one code point: visible label, accessible name, and the character as its text. */
function expectBadge(scope: HTMLElement, codePoint: number) {
  const badges = within(scope).getAllByRole("img", {
    name: `invisible character ${code(codePoint)}`,
  });
  for (const badge of badges) {
    expect(badge.getAttribute("data-label")).toBe(`⟨${code(codePoint)}⟩`);
    expect(classes(badge)).toContain("before:content-[attr(data-label)]");
    expect(classes(badge)).toContain("bg-warning/15");
    expect(badge.textContent).toBe(String.fromCodePoint(codePoint));
  }
  return badges;
}

describe("approval card long command (#2259)", () => {
  it("shows a 60-line command's line count and expands the whole command on demand", () => {
    const command = lines(60);
    const card = renderCard(JSON.stringify({ command }));
    const pre = onlyPre(card);
    expect(pre.textContent).toBe(lines(COMMAND_LINE_CAP));
    expect(classes(pre)).toEqual(expect.arrayContaining(["max-h-56", "overflow-y-auto"]));
    expect(within(card).getByText("60 lines")).toBeTruthy();
    const toggle = within(card).getByRole("button", { name: "Show 48 more lines" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-controls")).toBe(pre.id);
    expect(buttonNames(card)).toEqual([
      "Raw arguments",
      "Show 48 more lines",
      "Allow once",
      "Always allow",
      "Deny",
    ]);

    fireEvent.click(toggle);
    expect(onlyPre(card).textContent).toBe(command);
    // Expanded, the whole command shows with no scroll cap anywhere around it.
    expect(onlyPre(card).closest(".max-h-56")).toBeNull();
    expect(toggle.textContent).toBe("Show first 12 lines");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(card).getByText("60 lines")).toBeTruthy();

    fireEvent.click(toggle);
    expect(onlyPre(card).textContent).toBe(lines(COMMAND_LINE_CAP));
    expect(toggle.textContent).toBe("Show 48 more lines");
  });

  it("counts a single hidden line in the singular", () => {
    const card = renderCard(JSON.stringify({ command: lines(COMMAND_LINE_CAP + 1) }));
    expect(within(card).getByText("13 lines")).toBeTruthy();
    fireEvent.click(within(card).getByRole("button", { name: "Show 1 more line" }));
    expect(onlyPre(card).textContent).toBe(lines(COMMAND_LINE_CAP + 1));
  });

  it("keeps a long command's options visible below its toggle", () => {
    const card = renderCard(JSON.stringify({ command: lines(60), timeout_ms: 120000 }));
    expect(within(card).getByRole("button", { name: "Show 48 more lines" })).toBeTruthy();
    const terms = [...card.querySelectorAll("dt")].map((term) => [
      term.textContent,
      term.nextElementSibling?.textContent,
    ]);
    expect(terms).toEqual([["Timeout", "120000 ms"]]);
    expect(card.querySelector("dl")?.closest(".max-h-56")).toBeNull();
  });

  it("offers the whole command when its wrapped lines overflow the box", async () => {
    // Happy DOM has no layout: size the capped box as a browser would.
    const sized = ["clientHeight", "scrollHeight"] as const;
    for (const property of sized) {
      Object.defineProperty(HTMLPreElement.prototype, property, {
        configurable: true,
        get(this: HTMLPreElement) {
          return property === "scrollHeight" && this.classList.contains("max-h-56") ? 960 : 224;
        },
      });
    }
    try {
      const command = `echo ${"0123456789".repeat(300)}`;
      const card = renderCard(JSON.stringify({ command }));
      const toggle = await within(card).findByRole("button", { name: "Show full command" });
      expect(within(card).getByText("1 line")).toBeTruthy();
      expect(onlyPre(card).textContent).toBe(command);
      fireEvent.click(toggle);
      expect(onlyPre(card).closest(".max-h-56")).toBeNull();
      expect(toggle.textContent).toBe("Show less");
      fireEvent.click(toggle);
      expect(classes(onlyPre(card))).toContain("max-h-56");
      expect(toggle.textContent).toBe("Show full command");
    } finally {
      for (const property of sized) {
        Reflect.deleteProperty(HTMLPreElement.prototype, property);
      }
    }
  });

  it("shows a long command whole in the detail panel, invisible characters included", () => {
    const command = `${lines(49)}\necho step${String.fromCodePoint(0x200b)}50\n${lines(10, 51)}`;
    render(
      <ApprovalDetailPanel
        approval={ask(JSON.stringify({ command }))}
        disabled={false}
        onRespond={() => {}}
        scope={createRef<HTMLElement>()}
        uncertain={false}
      />,
    );
    const pre = onlyPre(document.body);
    expect(pre.textContent).toBe(command);
    expectBadge(pre, 0x200b);
    expect(screen.getByText("Contains invisible characters")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /more line|Show first|full command/ })).toBeNull();
  });
});

describe("approval card invisible characters (#2259)", () => {
  it.each(INVISIBLE_CODE_POINTS.map((codePoint) => [code(codePoint), codePoint]))(
    "shows %s as a named badge in the command, raw, and JSON views",
    (_name, codePoint) => {
      const character = String.fromCodePoint(codePoint);
      const command = `echo one${character}two`;
      const args = JSON.stringify({ command });
      const card = renderCard(args);
      const pre = onlyPre(card);
      expect(expectBadge(pre, codePoint)).toHaveLength(1);
      expect(pre.textContent).toBe(command);

      fireEvent.click(within(card).getByRole("button", { name: "Raw arguments" }));
      const raw = onlyPre(card);
      expect(expectBadge(raw, codePoint)).toHaveLength(1);
      expect(raw.textContent).toBe(args);
      cleanup();

      const readArgs = JSON.stringify({ path: `notes${character}.md` });
      const pretty = onlyPre(renderCard(readArgs, "Read"));
      expect(expectBadge(pretty, codePoint)).toHaveLength(1);
      expect(pretty.textContent).toBe(`{\n  "path": "notes${character}.md"\n}`);
    },
  );

  it("shows a JSON-escaped character decoded in the command view and as sent in the raw view", () => {
    const args = '{"command":"echo one\\u202etwo"}';
    const card = renderCard(args);
    const pre = onlyPre(card);
    expectBadge(pre, 0x202e);
    expect(pre.textContent).toBe(`echo one${String.fromCodePoint(0x202e)}two`);
    expect(within(card).getByText("Contains invisible characters")).toBeTruthy();

    fireEvent.click(within(card).getByRole("button", { name: "Raw arguments" }));
    const raw = onlyPre(card);
    expect(raw.textContent).toBe(args);
    expect(within(raw).queryAllByRole("img")).toEqual([]);
    expect(within(card).getByText("Contains invisible characters")).toBeTruthy();
  });

  it("badges a command's temporary-storage scope too", () => {
    const scope = `system${String.fromCodePoint(0xfeff)}`;
    const card = renderCard(JSON.stringify({ command: "make", temp_scope: scope }));
    const value = card.querySelector("dd") as HTMLElement;
    expectBadge(value, 0xfeff);
    expect(value.textContent).toBe(scope);
  });

  it("notes invisible characters with a chip only when the arguments carry one", () => {
    const plain = renderCard(JSON.stringify({ command: "make test" }));
    expect(within(plain).queryByText("Contains invisible characters")).toBeNull();
    cleanup();

    const card = renderCard(
      JSON.stringify({ command: `make${String.fromCodePoint(0x2066)} test` }),
    );
    const chip = within(card).getByText("Contains invisible characters");
    expect(chip.getAttribute("data-slot")).toBe("badge");
    expect(classes(chip)).toEqual(expect.arrayContaining(["bg-warning/15", "text-warning"]));
    // The chip sits beside the toggle, before the arguments it describes.
    expect(chip.parentElement).toBe(
      within(card).getByRole("button", { name: "Raw arguments" }).parentElement,
    );
    fireEvent.click(within(card).getByRole("button", { name: "Raw arguments" }));
    expect(within(card).getByText("Contains invisible characters")).toBeTruthy();
  });

  it("keeps the chip for a character in a line the card has not expanded yet", () => {
    const command = `${lines(49)}\necho step${String.fromCodePoint(0x202d)}50\n${lines(10, 51)}`;
    const card = renderCard(JSON.stringify({ command }));
    expect(within(card).queryAllByRole("img")).toEqual([]);
    expect(within(card).getByText("Contains invisible characters")).toBeTruthy();
    fireEvent.click(within(card).getByRole("button", { name: "Show 48 more lines" }));
    expectBadge(onlyPre(card), 0x202d);
  });

  it("notes an Edit's invisible characters without changing its diff", () => {
    const args = JSON.stringify({
      new_string: `after${String.fromCodePoint(0x200c)}`,
      old_string: "before",
      path: "notes.txt",
    });
    const card = renderCard(args, "Edit");
    expect(within(card).getByText("Contains invisible characters")).toBeTruthy();
    const diff = within(card).getByTestId("edit-diff");
    expect(within(diff).queryAllByRole("img")).toEqual([]);
    fireEvent.click(within(card).getByRole("button", { name: "Raw arguments" }));
    expectBadge(onlyPre(card), 0x200c);
  });

  it("copies the exact original text, never the badge labels", () => {
    const character = (codePoint: number) => String.fromCodePoint(codePoint);
    const command = `echo one${character(0x200b)}two${character(0x202e)}three\n${character(0x2066)}four${character(0x2069)}${character(0xfeff)}`;
    const args = JSON.stringify({ command });
    const card = renderCard(args);
    const pre = onlyPre(card);
    expect(within(pre).getAllByRole("img")).toHaveLength(5);
    expect(selectAll(pre)).toBe(command);
    expect(pre.textContent).not.toContain("U+");

    fireEvent.click(within(card).getByRole("button", { name: "Raw arguments" }));
    expect(selectAll(onlyPre(card))).toBe(args);
    cleanup();

    const readArgs = JSON.stringify({ path: `a${character(0x202a)}b` });
    const pretty = onlyPre(renderCard(readArgs, "Read"));
    expect(selectAll(pretty)).toBe(JSON.stringify(JSON.parse(readArgs), null, 2));
  });
});

describe("invisible character detection", () => {
  it.each(INVISIBLE_CODE_POINTS.map((codePoint) => [code(codePoint), codePoint]))(
    "finds %s",
    (_name, codePoint) => {
      expect(hasInvisibleCharacters(`a${String.fromCodePoint(codePoint)}b`)).toBe(true);
    },
  );

  it.each(
    [0x200a, 0x200e, 0x2029, 0x202f, 0x2065, 0x206a, 0xfefe, 0xff00].map((codePoint) => [
      code(codePoint),
      codePoint,
    ]),
  )("leaves the neighbouring %s alone", (_name, codePoint) => {
    expect(hasInvisibleCharacters(`a${String.fromCodePoint(codePoint)}b`)).toBe(false);
  });

  it("reads arguments as sent and as their JSON decodes", () => {
    expect(argsHaveInvisibleCharacters(`{"command":"a${String.fromCodePoint(0x200d)}b"}`)).toBe(
      true,
    );
    expect(argsHaveInvisibleCharacters('{"command":"a\\u200db"}')).toBe(true);
    expect(argsHaveInvisibleCharacters('{"comm\\ufeffand":"ls"}')).toBe(true);
    expect(argsHaveInvisibleCharacters('{"command":"ls -la"}')).toBe(false);
    // Unparsed arguments show as sent, where an escape is plain visible text.
    expect(argsHaveInvisibleCharacters('{broken "\\u202e"')).toBe(false);
    expect(argsHaveInvisibleCharacters(`{"command":"a\\u202e${"x".repeat(70_000)}"}`)).toBe(false);
    expect(argsHaveInvisibleCharacters(`{"command":"a\u202e${"x".repeat(70_000)}"}`)).toBe(true);
  });
});
