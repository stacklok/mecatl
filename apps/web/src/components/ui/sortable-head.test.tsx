// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { SortableHead } from "./sortable-head";
import { Table, TableHeader, TableRow } from "./table";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => document.body.replaceChildren());

async function renderHead(direction: "asc" | "desc" | undefined, onSort = vi.fn()) {
  const container = document.createElement("div");
  document.body.append(container);
  await act(async () =>
    createRoot(container).render(
      <Table>
        <TableHeader>
          <TableRow>
            <SortableHead direction={direction} label="Skill" onSort={onSort} />
          </TableRow>
        </TableHeader>
      </Table>,
    ),
  );
  return container;
}

it("reports its sort state through aria-sort", async () => {
  for (const [direction, expected] of [
    [undefined, "none"],
    ["asc", "ascending"],
    ["desc", "descending"],
  ] as const) {
    const container = await renderHead(direction);
    expect(container.querySelector("th")?.getAttribute("aria-sort")).toBe(expected);
    container.remove();
  }
});

it("calls onSort when the header button is clicked", async () => {
  const onSort = vi.fn();
  const container = await renderHead(undefined, onSort);
  await act(async () => container.querySelector("button")?.click());
  expect(onSort).toHaveBeenCalledOnce();
});
