// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { KnowledgeWorkspace } from "./knowledge-workspace";

/**
 * Pins the configured-skills table's sorting as the user sees it: the default
 * column and direction, which column a header click sorts by and in which
 * direction, the name tiebreak, keyboard activation, and `aria-sort`. It drives
 * the rendered table only, so it holds whichever sort primitive backs it.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

const skill = (name: string, description: string) => ({
  activeVersion: "",
  agentOwned: false,
  description,
  name,
  ownerAgent: "",
});

const inventory = {
  items: [
    skill("zeta-job", "Same"),
    skill("skill-10", "same"),
    skill("alpha_job", "beta"),
    skill("skill-2", "Alpha"),
  ],
  reason: "",
  supported: true,
};

async function renderSkills() {
  vi.stubGlobal("fetch", async (request: Request) => {
    const body =
      new URL(request.url).pathname === "/api/v1/skills"
        ? inventory
        : { capabilities: { learnedSkills: true, skills: true } };
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/skills"] }),
    routeTree,
  });
  render(
    <RouterContextProvider router={router}>
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <KnowledgeWorkspace onItemChange={() => {}} onViewChange={() => {}} view="configured" />
      </QueryClientProvider>
    </RouterContextProvider>,
  );
  return await screen.findByRole("table");
}

function tableNames(table: HTMLElement) {
  return [...table.querySelectorAll("tbody a")].map((anchor) => anchor.textContent);
}

function ariaSort(table: HTMLElement) {
  return within(table)
    .getAllByRole("columnheader")
    .map((head) => [head.textContent, head.getAttribute("aria-sort")]);
}

describe("configured skills sorting", () => {
  it("starts by name ascending, case-insensitively and with numeric order", async () => {
    const table = await renderSkills();
    expect(ariaSort(table)).toEqual([
      ["Name", "ascending"],
      ["Description", "none"],
    ]);
    expect(tableNames(table)).toEqual(["Alpha Job", "Skill 2", "Skill 10", "Zeta Job"]);
  });

  it("flips the active column and starts another column ascending", async () => {
    const user = userEvent.setup();
    const table = await renderSkills();
    const name = within(table).getByRole("button", { name: "Name" });
    const description = within(table).getByRole("button", { name: "Description" });

    await user.click(name);
    expect(ariaSort(table)).toEqual([
      ["Name", "descending"],
      ["Description", "none"],
    ]);
    expect(tableNames(table)).toEqual(["Zeta Job", "Skill 10", "Skill 2", "Alpha Job"]);

    await user.click(description);
    expect(ariaSort(table)).toEqual([
      ["Name", "none"],
      ["Description", "ascending"],
    ]);
    // Alpha < beta < same = Same; the equal descriptions fall back to name order.
    expect(tableNames(table)).toEqual(["Skill 2", "Alpha Job", "Skill 10", "Zeta Job"]);

    await user.click(description);
    expect(ariaSort(table)).toEqual([
      ["Name", "none"],
      ["Description", "descending"],
    ]);
    // The name tiebreak does not follow the direction.
    expect(tableNames(table)).toEqual(["Skill 10", "Zeta Job", "Alpha Job", "Skill 2"]);

    await user.click(name);
    expect(ariaSort(table)).toEqual([
      ["Name", "ascending"],
      ["Description", "none"],
    ]);
    expect(tableNames(table)).toEqual(["Alpha Job", "Skill 2", "Skill 10", "Zeta Job"]);
  });

  it("sorts from the keyboard through the header buttons", async () => {
    const user = userEvent.setup();
    const table = await renderSkills();
    const name = within(table).getByRole("button", { name: "Name" });
    expect(name.getAttribute("type")).toBe("button");
    expect(name.closest("th")).not.toBeNull();

    name.focus();
    await user.keyboard("{Enter}");
    expect(ariaSort(table)[0]).toEqual(["Name", "descending"]);
    await user.keyboard(" ");
    expect(ariaSort(table)[0]).toEqual(["Name", "ascending"]);
    await user.tab();
    expect(document.activeElement?.textContent).toBe("Description");
    await user.keyboard("{Enter}");
    expect(ariaSort(table)[1]).toEqual(["Description", "ascending"]);
  });

  it("keeps the phone list in name order whatever the table sort", async () => {
    const user = userEvent.setup();
    const table = await renderSkills();
    const phoneList = () =>
      [...document.querySelectorAll(".min-\\[500px\\]\\:hidden > a span:first-child")].map(
        (span) => span.textContent,
      );
    // The phone list orders by plain localeCompare, without the table's numeric collation.
    expect(phoneList()).toEqual(["Alpha Job", "Skill 10", "Skill 2", "Zeta Job"]);
    await user.click(within(table).getByRole("button", { name: "Description" }));
    await user.click(within(table).getByRole("button", { name: "Description" }));
    expect(phoneList()).toEqual(["Alpha Job", "Skill 10", "Skill 2", "Zeta Job"]);
  });

  it("sorts what the filter leaves", async () => {
    const user = userEvent.setup();
    const table = await renderSkills();
    await user.click(within(table).getByRole("button", { name: "Name" }));
    await user.type(screen.getByRole("textbox", { name: "Filter skills" }), "job");
    await waitFor(() => expect(tableNames(table)).toEqual(["Zeta Job", "Alpha Job"]));
  });
});
