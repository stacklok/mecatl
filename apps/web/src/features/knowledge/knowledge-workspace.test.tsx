// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { KnowledgeWorkspace } from "./knowledge-workspace";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, params }: { children: React.ReactNode; params?: { item: string } }) => (
    <a href={`/workspace/skills/configured/${params?.item}`}>{children}</a>
  ),
}));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

const skills = {
  items: [
    {
      activeVersion: "",
      agentOwned: false,
      description: "Zeta job",
      name: "zeta-job",
      ownerAgent: "",
    },
    {
      activeVersion: "",
      agentOwned: false,
      description: "Alpha job",
      name: "alpha_job",
      ownerAgent: "",
    },
  ],
  reason: "",
  supported: true,
};

async function render(runtime: unknown) {
  vi.stubGlobal("fetch", async (request: Request) => {
    const body = new URL(request.url).pathname === "/api/v1/skills" ? skills : runtime;
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
        <KnowledgeWorkspace onItemChange={() => {}} onViewChange={() => {}} view="configured" />
      </QueryClientProvider>,
    ),
  );
  await act(async () => new Promise((resolve) => setTimeout(resolve, 20)));
  return container;
}

it("lists skills by humanized name and re-sorts from the header", async () => {
  const container = await render({ capabilities: { learnedSkills: true, skills: true } });
  const names = () =>
    [...container.querySelectorAll("tbody a")].map((anchor) => anchor.textContent);
  expect(names()).toEqual(["Alpha Job", "Zeta Job"]);
  const nameHead = container.querySelector("th");
  expect(nameHead?.getAttribute("aria-sort")).toBe("ascending");
  await act(async () => nameHead?.querySelector("button")?.click());
  expect(nameHead?.getAttribute("aria-sort")).toBe("descending");
  expect(names()).toEqual(["Zeta Job", "Alpha Job"]);
});

it("hides the Learned pill when the daemon has no learned-skill inventory", async () => {
  const container = await render({ capabilities: { learnedSkills: false, skills: true } });
  const pills = [...container.querySelectorAll("button[aria-pressed]")].map((b) => b.textContent);
  expect(pills).toEqual(["All"]);
});

it("warns when the Skill tool is disabled", async () => {
  const container = await render({ capabilities: { learnedSkills: true, skills: false } });
  expect(container.querySelector("[role=status]")?.textContent).toContain("Skill tool is disabled");
});

const learned = (actions: Record<string, boolean>, revision = "r1") => ({
  actions: { activate: false, archive: false, reject: false, rollback: false, ...actions },
  body: "Body text",
  description: "Draft procedure",
  evidenceCount: 2,
  id: "skill-1",
  name: "deploy",
  ownerAgent: "agent",
  revision,
  state: "staged",
  supersedes: "",
  updatedAt: null,
  version: "v2",
});

async function renderLearned(handler: (request: Request, calls: string[]) => unknown) {
  const calls: string[] = [];
  vi.stubGlobal("fetch", async (request: Request) => {
    const { pathname } = new URL(request.url);
    calls.push(`${request.method} ${pathname}`);
    const result = handler(request, calls);
    if (result instanceof Response) return result;
    return new Response(JSON.stringify(result), {
      headers: { "Content-Type": "application/json" },
    });
  });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root?.render(
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <KnowledgeWorkspace
          item="skill-1"
          onItemChange={() => {}}
          onViewChange={() => {}}
          view="learned"
        />
      </QueryClientProvider>,
    ),
  );
  await act(async () => new Promise((resolve) => setTimeout(resolve, 30)));
  return calls;
}

const buttonLabels = () =>
  [...document.body.querySelectorAll("[role=dialog] button")].map((b) => b.textContent);

it("offers only the lifecycle actions the daemon reports", async () => {
  await renderLearned((request) => {
    const { pathname } = new URL(request.url);
    if (pathname.endsWith("/changes")) return { complete: true, items: [] };
    if (pathname === "/api/v1/runtime")
      return { capabilities: { learnedSkills: true, skills: true } };
    return {
      complete: true,
      items: [learned({ activate: true, reject: true })],
      reason: "",
      supported: true,
    };
  });
  expect(buttonLabels()).toEqual(expect.arrayContaining(["Reject", "Activate"]));
  expect(buttonLabels()).not.toContain("Archive");
  expect(buttonLabels()).not.toContain("Roll back to ");
});

it("re-reads the inventory and shows the error when an action conflicts", async () => {
  const calls = await renderLearned((request) => {
    const { pathname } = new URL(request.url);
    if (request.method === "POST")
      return new Response(JSON.stringify({ detail: "Revision is stale." }), {
        headers: { "Content-Type": "application/json" },
        status: 409,
      });
    if (pathname.endsWith("/changes")) return { complete: true, items: [] };
    if (pathname === "/api/v1/runtime")
      return { capabilities: { learnedSkills: true, skills: true } };
    return { complete: true, items: [learned({ activate: true })], reason: "", supported: true };
  });
  const click = async (label: string) =>
    act(async () => {
      [...document.body.querySelectorAll<HTMLButtonElement>("button")]
        .find((b) => b.textContent === label)
        ?.click();
    });
  await click("Activate");
  await click("Confirm");
  await act(async () => new Promise((resolve) => setTimeout(resolve, 30)));
  expect(document.body.querySelector("[role=alert]")?.textContent).toContain("Revision is stale.");
  expect(buttonLabels()).toContain("Activate"); // dialog stays open on the refreshed skill
  expect(calls.filter((call) => call === "GET /api/v1/learned-skills").length).toBeGreaterThan(1);
});

it("explains why learned skills are unavailable on a direct link", async () => {
  await renderLearned((request) => {
    const { pathname } = new URL(request.url);
    if (pathname === "/api/v1/runtime")
      return { capabilities: { learnedSkills: false, skills: true } };
    if (pathname.endsWith("/changes")) return { complete: true, items: [] };
    return {
      complete: true,
      items: [],
      reason: "Learned skills are not enabled here.",
      supported: false,
    };
  });
  expect(document.body.textContent).toContain("Learned skills are unavailable");
  expect(document.body.textContent).toContain("Learned skills are not enabled here.");
});

it("says when the changes ledger is truncated", async () => {
  const change = (id: number) => ({
    at: null,
    fromState: "",
    id: `c${id}`,
    name: "deploy",
    operation: "activate",
    skillId: "skill-1",
    toState: "",
    verdict: "",
    version: "v2",
  });
  await renderLearned((request) => {
    const { pathname } = new URL(request.url);
    if (pathname === "/api/v1/runtime")
      return { capabilities: { learnedSkills: true, skills: true } };
    if (pathname.endsWith("/changes"))
      return { complete: true, items: Array.from({ length: 25 }, (_, i) => change(i)) };
    return { complete: true, items: [learned({})], reason: "", supported: true };
  });
  expect(document.body.querySelectorAll("li").length).toBe(20);
  expect(document.body.textContent).toContain("Showing the 20 most recent lifecycle changes.");
});
