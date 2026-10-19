// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import {
  CollapsibleTableRows,
  NestedTableRow,
  NestedTableRowTrigger,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "./table";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

function slot(container: HTMLElement, name: string) {
  return container.querySelector(`[data-slot="${name}"]`) as HTMLElement;
}

function Nested({ isCollapsible = true }: { isCollapsible?: boolean }) {
  const [isExpanded, setExpanded] = useState(false);
  return (
    <Table>
      <TableBody>
        <NestedTableRow
          colSpan={2}
          isCollapsible={isCollapsible}
          isExpanded={isExpanded}
          onExpandedChange={setExpanded}
        >
          <TableRow>
            <TableCell>
              <NestedTableRowTrigger collapseLabel="Hide runs" expandLabel="Show runs" />
            </TableCell>
            <TableCell>Parent</TableCell>
          </TableRow>
          <CollapsibleTableRows colWidths={["2rem", undefined]}>
            <TableRow>
              <TableCell>Child</TableCell>
            </TableRow>
          </CollapsibleTableRows>
        </NestedTableRow>
      </TableBody>
    </Table>
  );
}

describe("Table", () => {
  it("styles the scroll container separately from the table", () => {
    const { container } = render(
      <Table className="min-w-[42rem]" containerClassName="rounded-xl border">
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow>
            <TableCell>Row</TableCell>
          </TableRow>
        </TableBody>
      </Table>,
    );
    const scroller = slot(container, "table-container");
    expect(scroller.className).toContain("overflow-x-auto");
    expect(scroller.className).toContain("rounded-xl");
    expect(scroller.className).not.toContain("min-w-[42rem]");
    expect(slot(container, "table").className).toContain("min-w-[42rem]");
    expect(slot(container, "table-head").className).toContain("px-4");
    expect(slot(container, "table-cell").className).toContain("px-4 py-3");
  });

  it("reveals and hides nested rows from the row trigger", async () => {
    const user = userEvent.setup();
    const { container } = render(<Nested />);
    const trigger = screen.getByRole("button", { name: "Show runs" });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    const rows = slot(container, "table-collapsible-rows");
    expect(rows.querySelector("td")?.getAttribute("colspan")).toBe("2");
    expect(rows.querySelector("[inert]")).not.toBeNull();
    expect([...rows.querySelectorAll("col")].map((col) => col.style.width)).toEqual(["2rem", ""]);

    await user.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(trigger.getAttribute("aria-label")).toBe("Hide runs");
    expect(rows.className).toContain("border-b");
    expect(rows.querySelector("[inert]")).toBeNull();
    expect(rows.textContent).toBe("Child");

    await user.click(trigger);
    expect(trigger.getAttribute("aria-label")).toBe("Show runs");
    expect(rows.querySelector("[inert]")).not.toBeNull();
  });

  it("renders neither trigger nor sub-rows for a row without children", () => {
    const { container } = render(<Nested isCollapsible={false} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(slot(container, "table-collapsible-rows")).toBeNull();
    expect(container.textContent).toBe("Parent");
  });

  it("rejects nested-row parts outside a NestedTableRow", () => {
    const original = console.error;
    console.error = () => {};
    try {
      expect(() => render(<NestedTableRowTrigger />)).toThrow(
        "NestedTableRowTrigger must be used inside <NestedTableRow>",
      );
    } finally {
      console.error = original;
    }
  });
});
