import { describe, expect, it } from "vitest";
import {
  ATTACH_FILE_MENU_ID,
  fileMenuRows,
  isAttachFileItem,
  isFileMenuItem,
  isPathLikeQuery,
  pathMentionText,
} from "./composer-file-mention";

/**
 * The `@` menu's file rows (TUI file-mention parity, web-shaped): an
 * "Attach a file…" row while the query could still spell "file", a
 * "Mention path" row for a path-shaped token, neither for an agent query.
 */
describe("fileMenuRows", () => {
  it("offers Attach a file… for an empty query and every prefix of 'file'", () => {
    for (const query of ["", "f", "fi", "FIL", "file"]) {
      const rows = fileMenuRows(query);
      expect(rows[0]).toMatchObject({
        id: ATTACH_FILE_MENU_ID,
        primary: "Attach a file…",
      });
      expect(rows).toHaveLength(1);
    }
  });

  it("offers Mention path for a path-shaped token, inserting the query verbatim", () => {
    for (const query of ["src/", "main.go", "./x", "~/notes", "a/b.c"]) {
      const rows = fileMenuRows(query);
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({
        label: `@${query}`,
        primary: `Mention path @${query}`,
      });
      expect(pathMentionText(rows[0])).toBe(`@${query}`);
    }
  });

  it("offers nothing for an agent-shaped query", () => {
    expect(fileMenuRows("rev")).toEqual([]);
    expect(fileMenuRows("planner")).toEqual([]);
  });

  it("can offer both when a path token still spells 'file' (e.g. 'file.')", () => {
    const rows = fileMenuRows("fi");
    expect(rows.map((row) => isAttachFileItem(row))).toEqual([true]);
    const both = fileMenuRows("file.");
    expect(both.map((row) => isFileMenuItem(row))).toEqual([true]);
    expect(pathMentionText(both[0])).toBe("@file.");
  });
});

describe("isPathLikeQuery", () => {
  it("needs a slash or a dot and no whitespace", () => {
    expect(isPathLikeQuery("src/")).toBe(true);
    expect(isPathLikeQuery("x.ts")).toBe(true);
    expect(isPathLikeQuery("rev")).toBe(false);
    expect(isPathLikeQuery("")).toBe(false);
    expect(isPathLikeQuery("a b.c")).toBe(false);
  });
});

describe("row predicates", () => {
  it("recognise the two file rows and nothing else", () => {
    const agent = { id: "reviewer" };
    expect(isAttachFileItem({ id: ATTACH_FILE_MENU_ID })).toBe(true);
    expect(isAttachFileItem(agent)).toBe(false);
    expect(pathMentionText(agent)).toBeNull();
    expect(isFileMenuItem(agent)).toBe(false);
    expect(isFileMenuItem({ id: ATTACH_FILE_MENU_ID })).toBe(true);
    expect(isFileMenuItem({ id: "__path:src/" })).toBe(true);
  });
});
