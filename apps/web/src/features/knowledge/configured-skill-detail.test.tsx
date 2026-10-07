// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConfiguredSkillDetail } from "./configured-skill-detail";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a href="/">{children}</a>,
}));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

async function renderDetail(name: string, inventory: unknown) {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify(inventory), { headers: { "Content-Type": "application/json" } }),
  );
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root?.render(
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <ConfiguredSkillDetail name={name} />
      </QueryClientProvider>,
    ),
  );
  await act(async () => new Promise((resolve) => setTimeout(resolve, 20)));
  return container;
}

const inventory = {
  items: [
    {
      activeVersion: "v3",
      agentOwned: false,
      description: "<b>escaped</b> deploys",
      name: "deploy",
      ownerAgent: "",
    },
  ],
  reason: "",
  supported: true,
};

it("shows summary, inert manage controls, and the metadata-only files note", async () => {
  const container = await renderDetail("deploy", inventory);
  expect(container.querySelector("h1")?.textContent).toBe("Deploy");
  expect(container.textContent).toContain("Summary");
  expect(container.querySelector("b")).toBeNull(); // description is text, not markup
  const buttons = [...container.querySelectorAll<HTMLButtonElement>("button")];
  const edit = buttons.find((button) => button.textContent === "Edit");
  expect(edit?.disabled).toBe(true);
  expect(
    buttons.find((button) => button.getAttribute("aria-label")?.includes("More actions"))?.disabled,
  ).toBe(true);
  expect(container.textContent).toContain("Managed by the Mecatl deployment");
  expect(container.textContent).toContain("metadata-only");
});

it("renders not-found for a skill missing from the inventory", async () => {
  const container = await renderDetail("gone", inventory);
  expect(container.textContent).toContain("Skill not found");
});

it("shows the daemon's reason when skills are unsupported", async () => {
  const container = await renderDetail("deploy", {
    items: [],
    reason: "Not enabled.",
    supported: false,
  });
  expect(container.textContent).toContain("Not enabled.");
});
