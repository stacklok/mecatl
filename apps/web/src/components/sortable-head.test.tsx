// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, render, renderHook, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Table, TableHeader, TableRow } from "@/components/ui/table";
import { directed, SortableHead, type TableSort, useTableSort } from "./sortable-head";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

type Key = "name" | "size";

function Header() {
  const sort = useTableSort<Key>("name");
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <SortableHead label="Name" sort={sort} sortKey="name" />
          <SortableHead label="Size" sort={sort} sortKey="size" />
        </TableRow>
      </TableHeader>
    </Table>
  );
}

const ariaSort = () =>
  screen.getAllByRole("columnheader").map((head) => head.getAttribute("aria-sort"));

describe("useTableSort", () => {
  it("starts on the default column and direction", () => {
    const { result } = renderHook(() => useTableSort<Key>("size", "desc"));
    expect(result.current.key).toBe("size");
    expect(result.current.dir).toBe("desc");
  });

  it("flips the active column and starts a new column ascending", () => {
    const { result } = renderHook(() => useTableSort<Key>("name"));
    act(() => result.current.toggle("name"));
    expect([result.current.key, result.current.dir]).toEqual(["name", "desc"]);
    act(() => result.current.toggle("size"));
    expect([result.current.key, result.current.dir]).toEqual(["size", "asc"]);
    act(() => result.current.toggle("size"));
    expect([result.current.key, result.current.dir]).toEqual(["size", "desc"]);
  });
});

describe("directed", () => {
  it("keeps a comparison ascending and negates it descending", () => {
    expect(directed("asc", -1)).toBe(-1);
    expect(directed("desc", -1)).toBe(1);
  });
});

describe("SortableHead", () => {
  it("reports the sorted column through aria-sort and re-sorts on click", async () => {
    const user = userEvent.setup();
    render(<Header />);
    expect(ariaSort()).toEqual(["ascending", "none"]);
    await user.click(screen.getByRole("button", { name: "Name" }));
    expect(ariaSort()).toEqual(["descending", "none"]);
    await user.click(screen.getByRole("button", { name: "Size" }));
    expect(ariaSort()).toEqual(["none", "ascending"]);
  });

  it("shows the direction on the active column and a muted both-ways icon elsewhere", () => {
    render(<Header />);
    const name = screen.getByRole("button", { name: "Name" });
    const size = screen.getByRole("button", { name: "Size" });
    expect(name.className).toContain("text-foreground");
    expect(name.querySelector("svg")?.getAttribute("class")).toContain("lucide-arrow-up");
    expect(size.querySelector("svg")?.getAttribute("class")).toContain("lucide-chevrons-up-down");
    expect(size.querySelector("svg")?.getAttribute("class")).toContain("text-muted-foreground/60");
    expect(size.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  // Carried over from the retired components/ui/sortable-head.test.tsx: the
  // same aria-sort states and the same one call per header click, expressed
  // through the TableSort the new head reads instead of `direction`/`onSort`.
  function renderStatic(sort: TableSort<Key>) {
    return render(
      <Table>
        <TableHeader>
          <TableRow>
            <SortableHead label="Skill" sort={sort} sortKey="name" />
          </TableRow>
        </TableHeader>
      </Table>,
    );
  }

  it("reports its sort state through aria-sort", () => {
    for (const [sort, expected] of [
      [{ dir: "asc", key: "size" }, "none"],
      [{ dir: "asc", key: "name" }, "ascending"],
      [{ dir: "desc", key: "name" }, "descending"],
    ] as const) {
      const { container, unmount } = renderStatic({ ...sort, toggle: vi.fn() });
      expect(container.querySelector("th")?.getAttribute("aria-sort")).toBe(expected);
      unmount();
    }
  });

  it("calls toggle with its column when the header button is clicked", async () => {
    const user = userEvent.setup();
    const toggle = vi.fn();
    renderStatic({ dir: "asc", key: "size", toggle });
    await user.click(screen.getByRole("button", { name: "Skill" }));
    expect(toggle).toHaveBeenCalledOnce();
    expect(toggle).toHaveBeenCalledWith("name");
  });
});
