import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { LINE_DIFF_MAX_LINES } from "@/lib/line-diff";
import {
  computeLineDiff,
  DIFF_LINE_CAP,
  EditDiffBlock,
  editSizeNote,
  isDiffTool,
  parseDiffArgs,
  parseEditArgs,
  parseWriteArgs,
  ToolDiff,
  WRITE_OVERWRITE_NOTE,
  WriteBlock,
  writeSizeNote,
} from "./tool-diff";

/**
 * The inline Edit/Write diff (the TUI's renderEditDiff / renderWriteDiff):
 * accurate -N/+N counts, the `replace all` and `overwrites if it exists`
 * notes in the header, red/green rows, and the 40-line cap with Show all.
 */
describe("computeLineDiff", () => {
  it("counts removed and added lines over a known pair", () => {
    const diff = computeLineDiff("a\nb\nc", "a\nB\nc\nd");
    expect(diff.removed).toBe(1);
    expect(diff.added).toBe(2);
    expect(diff.coarse).toBe(false);
    expect(diff.lines.map((line) => line.kind)).toEqual([
      "context",
      "removed",
      "added",
      "context",
      "added",
    ]);
  });

  it("treats the empty text as no lines (a pure insertion or deletion)", () => {
    expect(computeLineDiff("", "x\ny")).toMatchObject({ removed: 0, added: 2 });
    expect(computeLineDiff("x", "")).toMatchObject({ removed: 1, added: 0 });
  });

  it("falls back to a removed block then an added block above the LCS bound, still counting", () => {
    const before = Array.from({ length: LINE_DIFF_MAX_LINES }, (_, i) =>
      String(i),
    ).join("\n");
    const after = `${before}\nextra`;
    const diff = computeLineDiff(before, after);
    expect(diff.coarse).toBe(true);
    expect(diff.removed).toBe(LINE_DIFF_MAX_LINES);
    expect(diff.added).toBe(LINE_DIFF_MAX_LINES + 1);
    expect(diff.lines[0].kind).toBe("removed");
    expect(diff.lines.at(-1)?.kind).toBe("added");
  });
});

describe("arg parsing and size notes", () => {
  const editArgs = JSON.stringify({
    path: "src/a.ts",
    old_string: "one\ntwo",
    new_string: "one\n2\nthree",
    replace_all: true,
  });
  const writeArgs = JSON.stringify({ path: "b.md", content: "x\ny\nz" });

  it("parses Edit and Write args and refuses the rest", () => {
    expect(parseEditArgs(editArgs)).toEqual({
      kind: "edit",
      path: "src/a.ts",
      oldString: "one\ntwo",
      newString: "one\n2\nthree",
      replaceAll: true,
    });
    expect(parseWriteArgs(writeArgs)).toEqual({
      kind: "write",
      path: "b.md",
      content: "x\ny\nz",
    });
    expect(parseEditArgs(writeArgs)).toBeNull();
    expect(parseEditArgs("not json")).toBeNull();
    expect(parseDiffArgs("Read", '{"path":"a"}')).toBeNull();
    expect(parseDiffArgs("edit", editArgs)?.kind).toBe("edit");
    expect(parseDiffArgs("Write", writeArgs)?.kind).toBe("write");
  });

  it("names the diff tools", () => {
    expect(isDiffTool("Edit")).toBe(true);
    expect(isDiffTool("write")).toBe(true);
    expect(isDiffTool("Shell")).toBe(false);
  });

  it("phrases the header notes", () => {
    expect(
      editSizeNote(computeLineDiff("one\ntwo", "one\n2\nthree"), true),
    ).toBe("-1 +2 · replace all");
    expect(editSizeNote(computeLineDiff("a", "b"), false)).toBe("-1 +1");
    expect(writeSizeNote("x\ny\nz")).toBe(`3 lines · ${WRITE_OVERWRITE_NOTE}`);
    expect(writeSizeNote("x")).toBe(`1 line · ${WRITE_OVERWRITE_NOTE}`);
    expect(writeSizeNote("")).toBe(`0 lines · ${WRITE_OVERWRITE_NOTE}`);
  });
});

describe("EditDiffBlock", () => {
  it("renders the path with -N +N (replace all) and tinted rows", () => {
    const { container } = render(
      <EditDiffBlock
        path="src/a.ts"
        oldText={"one\ntwo"}
        newText={"one\n2\nthree"}
        replaceAll
      />,
    );
    const header = screen.getByTestId("diff-header");
    expect(header).toHaveTextContent("src/a.ts");
    expect(header).toHaveTextContent("-1 +2 · replace all");
    expect(container.querySelectorAll('[data-diff="removed"]')).toHaveLength(1);
    expect(container.querySelectorAll('[data-diff="added"]')).toHaveLength(2);
    expect(container.querySelectorAll('[data-diff="context"]')).toHaveLength(1);
  });

  it("omits the replace-all note for a single-occurrence edit", () => {
    render(<EditDiffBlock path="a" oldText="x" newText="y" />);
    expect(screen.getByTestId("diff-header")).toHaveTextContent("a -1 +1");
    expect(screen.queryByText(/replace all/)).toBeNull();
  });
});

describe("WriteBlock", () => {
  it("says the file is overwritten and renders every line as added", () => {
    const { container } = render(
      <WriteBlock path="b.md" content={"x\ny\nz"} />,
    );
    expect(screen.getByTestId("diff-header")).toHaveTextContent(
      `b.md 3 lines · ${WRITE_OVERWRITE_NOTE}`,
    );
    expect(container.querySelectorAll('[data-diff="added"]')).toHaveLength(3);
    expect(container.querySelectorAll('[data-diff="removed"]')).toHaveLength(0);
  });

  it("caps the rows at the line cap with a Show all toggle", () => {
    const total = DIFF_LINE_CAP + 10;
    const content = Array.from({ length: total }, (_, i) => `l${i}`).join("\n");
    const { container } = render(
      <WriteBlock path="big.txt" content={content} />,
    );
    expect(container.querySelectorAll("[data-diff]")).toHaveLength(
      DIFF_LINE_CAP,
    );
    const toggle = screen.getByRole("button", {
      name: `Show all ${total} lines`,
    });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(container.querySelectorAll("[data-diff]")).toHaveLength(total);
    expect(
      screen.getByRole("button", { name: `Show first ${DIFF_LINE_CAP} lines` }),
    ).toHaveAttribute("aria-expanded", "true");
  });

  it("offers no toggle when everything already fits", () => {
    render(<WriteBlock path="small.txt" content={"a\nb"} />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("ToolDiff", () => {
  it("picks the block by tool name and renders nothing for other tools", () => {
    const { container: edit } = render(
      <ToolDiff
        name="Edit"
        rawArgs={JSON.stringify({
          path: "a",
          old_string: "x",
          new_string: "y",
        })}
      />,
    );
    expect(edit.querySelector('[data-testid="edit-diff"]')).not.toBeNull();
    const { container: write } = render(
      <ToolDiff
        name="Write"
        rawArgs={JSON.stringify({ path: "b", content: "c" })}
      />,
    );
    expect(write.querySelector('[data-testid="write-block"]')).not.toBeNull();
    const { container: read } = render(
      <ToolDiff name="Read" rawArgs={JSON.stringify({ path: "b" })} />,
    );
    expect(read.innerHTML).toBe("");
    const { container: broken } = render(<ToolDiff name="Edit" rawArgs="{" />);
    expect(broken.innerHTML).toBe("");
  });
});
