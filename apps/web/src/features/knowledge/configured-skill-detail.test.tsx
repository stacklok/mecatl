// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { ConfiguredSkillDetail } from "./configured-skill-detail";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function testRouter() {
  return createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/skills"] }),
    routeTree,
  });
}

let root: Root | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

/** Renders the page; `inventoryGate`, when given, holds the inventory response until it resolves. */
async function renderDetail(name: string, inventory: unknown, inventoryGate?: Promise<void>) {
  const requested: string[] = [];
  vi.stubGlobal("fetch", async (request: Request) => {
    const path = new URL(request.url).pathname;
    requested.push(path);
    const isFiles = path.endsWith("/files");
    if (!isFiles) await inventoryGate;
    const body = isFiles
      ? {
          files: [
            {
              content: "# Deploy body",
              instructions: true,
              name: "SKILL.md",
              size: 13,
              unavailable: "",
            },
          ],
          omitted: 0,
        }
      : inventory;
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root?.render(
      <RouterContextProvider router={testRouter()}>
        <QueryClientProvider
          client={
            // staleTime as in main.tsx: data fetched a moment ago is not fetched again on mount.
            new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } })
          }
        >
          <ConfiguredSkillDetail name={name} />
        </QueryClientProvider>
      </RouterContextProvider>,
    ),
  );
  await act(async () => new Promise((resolve) => setTimeout(resolve, 20)));
  return { container, requested };
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

it("shows the summary as text and the skill's files, with no Manage section", async () => {
  const { container } = await renderDetail("deploy", inventory);
  expect(container.querySelector("h1")?.textContent).toBe("Deploy");
  expect(container.textContent).toContain("Summary");
  // The description is shown, and as text: markup in it is not interpreted.
  expect(container.textContent).toContain("<b>escaped</b> deploys");
  expect(container.querySelector("b")).toBeNull();
  expect(container.textContent).toContain("# Deploy body");
  // Edit, Disable and Delete wait for a write contract (#2184, #2185).
  expect(container.textContent).not.toContain("Manage");
  expect([...container.querySelectorAll("button")].some((b) => b.textContent === "Edit")).toBe(
    false,
  );
});

it("requests the files before the inventory has answered", async () => {
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const { container, requested } = await renderDetail("deploy", inventory, gate);
  // The inventory is still held back, so the page cannot be showing the skill yet.
  expect(container.textContent).toContain("Loading skill");
  expect(requested.some((path) => path.endsWith("/skills/deploy/files"))).toBe(true);
  await act(async () => {
    release();
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
  // Opening the page loaded the files once; showing them reused that request.
  expect(requested.filter((path) => path.endsWith("/files"))).toHaveLength(1);
  expect(container.textContent).toContain("# Deploy body");
});

it("renders not-found for a skill missing from the inventory", async () => {
  const { container } = await renderDetail("gone", inventory);
  expect(container.textContent).toContain("Skill not found");
});

it("shows the daemon's reason when skills are unsupported", async () => {
  const { container } = await renderDetail("deploy", {
    items: [],
    reason: "Not enabled.",
    supported: false,
  });
  expect(container.textContent).toContain("Not enabled.");
});
