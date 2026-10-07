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

it("shows summary, read-only manage, and an honest files state", async () => {
  const container = await renderDetail("deploy", inventory);
  expect(container.textContent).toContain("v3");
  expect(container.querySelector("b")).toBeNull(); // description is text, not markup
  const tab = (label: string) =>
    [...container.querySelectorAll<HTMLButtonElement>("[role=tab]")].find(
      (button) => button.textContent === label,
    );
  await act(async () => tab("Manage")?.click());
  expect(container.textContent).toContain("Read-only");
  expect(container.querySelector("input, textarea")).toBeNull();
  await act(async () => tab("Files")?.click());
  expect(container.textContent).toContain("No files to show");
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
