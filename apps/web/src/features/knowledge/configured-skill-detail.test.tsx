// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConfiguredSkillDetail, SHOW_MANAGE_PLACEHOLDER } from "./configured-skill-detail";

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
  vi.stubGlobal("fetch", async (request: Request) => {
    const body = new URL(request.url).pathname.endsWith("/files")
      ? {
          files: [{ content: "# Deploy body", name: "SKILL.md", size: 13, unavailable: "" }],
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
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <ConfiguredSkillDetail name={name} />
      </QueryClientProvider>,
    ),
  );
  await act(async () => new Promise((resolve) => setTimeout(resolve, 20)));
  // The Files request starts only once the inventory has rendered the page.
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

it("shows summary and the skill's files, with Manage hidden by default", async () => {
  const container = await renderDetail("deploy", inventory);
  expect(container.querySelector("h1")?.textContent).toBe("Deploy");
  expect(container.textContent).toContain("Summary");
  expect(container.querySelector("b")).toBeNull(); // description is text, not markup
  expect(container.textContent).toContain("# Deploy body");
  // The Manage placeholder is behind the default-off SHOW_MANAGE_PLACEHOLDER flag.
  expect(SHOW_MANAGE_PLACEHOLDER).toBe(false);
  expect(container.textContent).not.toContain("Manage");
  expect(container.textContent).not.toContain("Managed by the Mecatl deployment");
  expect([...container.querySelectorAll("button")].some((b) => b.textContent === "Edit")).toBe(
    false,
  );
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
