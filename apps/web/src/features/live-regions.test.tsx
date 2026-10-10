// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Loading, empty, and failure states announce themselves: a polite status for
 * loading and empty states, an alert for failures, and decorative glyphs
 * hidden from assistive technology. Covers the states the accessibility sweep
 * (#2208) added roles to outside Settings, which `settings-accessibility`
 * already covers.
 */

import type { SessionWorktreesResponse } from "@mecatl-studio/contracts";
import { client as apiClient } from "@mecatl-studio/contracts/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it } from "vitest";
import { routeTree } from "../routeTree.gen";
import { WorktreePickerView } from "./chat/worktree-picker-dialog";
import { ConfiguredSkillDetail } from "./knowledge/configured-skill-detail";
import { StateCard } from "./knowledge/state-card";
import { FactsAboutYou } from "./memory/facts-about-you";
import { MemoryFactDetail } from "./memory/memory-fact-detail";
import { ProviderDetail } from "./settings/provider-detail";
import { ShortcutReference } from "./shortcuts/shortcut-reference";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Answer = unknown | "pending" | { status: number };
const answers = new Map<string, Answer>();
const initialApiConfig = apiClient.getConfig();

afterEach(() => {
  cleanup();
  answers.clear();
  apiClient.setConfig(initialApiConfig);
});

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    headers: { "content-type": status < 400 ? "application/json" : "application/problem+json" },
    status,
  });
}

const fakeFetch: typeof globalThis.fetch = async (input) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  const answer = answers.get(url.pathname);
  if (answer === "pending") return new Promise<Response>(() => {});
  if (answer && typeof answer === "object" && "status" in answer) {
    const { status } = answer as { status: number };
    return json({ code: "", detail: "Failed", instance: "", status, title: "", type: "" }, status);
  }
  if (answer === undefined) return json({ code: "not_found", detail: url.pathname }, 404);
  return json(answer);
};

function mount(path: string, element: ReactNode) {
  apiClient.setConfig({ baseUrl: window.location.origin, fetch: fakeFetch });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  });
  return render(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>{element}</RouterContextProvider>
    </QueryClientProvider>,
  );
}

const online = { capabilities: { memory: true }, connection: "online" };

describe("skills states", () => {
  it("announces the inventory's state cards and hides their glyph", () => {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(
      <>
        <StateCard icon="skill" text="No skills here yet" />
        <StateCard error text="The skill inventory could not be loaded." />
      </>,
    );
    const [empty, failure] = [...host.children];
    expect(empty?.getAttribute("role")).toBe("status");
    expect(empty?.querySelector("svg")?.closest('[aria-hidden="true"]')).not.toBeNull();
    expect(failure?.getAttribute("role")).toBe("alert");
  });

  it("announces a skill detail's loading, failure, and missing states", async () => {
    answers.set("/api/v1/skills", "pending");
    mount("/workspace/skills/configured/x", <ConfiguredSkillDetail name="x" />);
    expect(screen.getByRole("status").textContent).toContain("Loading skill…");
    cleanup();

    answers.set("/api/v1/skills", { status: 500 });
    mount("/workspace/skills/configured/x", <ConfiguredSkillDetail name="x" />);
    expect((await screen.findByRole("alert")).textContent).toContain(
      "The skill inventory could not be loaded.",
    );
    cleanup();

    answers.set("/api/v1/skills", { items: [], reason: "", supported: true });
    mount("/workspace/skills/configured/x", <ConfiguredSkillDetail name="x" />);
    expect(
      (await screen.findByText("Skill not found")).closest("[role]")?.getAttribute("role"),
    ).toBe("status");
  });
});

describe("memory states", () => {
  it("announces a fact page that is still loading or cannot load its value", async () => {
    answers.set("/api/v1/runtime", online);
    answers.set("/api/v1/user-memory", "pending");
    mount("/workspace/memory", <MemoryFactDetail memoryId="style/answers" />);
    expect((await screen.findByRole("status")).textContent).toBe("Loading…");
    cleanup();

    answers.set("/api/v1/user-memory", {
      items: [{ description: "Prefers concise answers.", key: "style/answers" }],
      reason: "",
      sha256: "x",
      sizeBytes: "1",
      supported: true,
    });
    answers.set("/api/v1/user-memory/style%2Fanswers", { status: 500 });
    answers.set("/api/v1/user-memory/style/answers", { status: 500 });
    mount("/workspace/memory", <MemoryFactDetail memoryId="style/answers" />);
    expect((await screen.findByRole("alert")).textContent).toContain("Value unavailable");
  });

  it("announces Facts about you when nothing is remembered yet", async () => {
    answers.set("/api/v1/user-memory", {
      items: [],
      reason: "",
      sha256: "",
      sizeBytes: "0",
      supported: true,
    });
    mount("/workspace/settings/memory", <FactsAboutYou />);
    expect(
      (await screen.findByText(/hasn’t remembered anything about you yet/u)).getAttribute("role"),
    ).toBe("status");
  });
});

describe("shortcuts and provider pages", () => {
  it("announces the shortcuts page's feature note and names the back link without its arrow", async () => {
    answers.set("/api/v1/runtime", { capabilities: {}, connection: "unreachable" });
    mount("/workspace/shortcuts", <ShortcutReference />);
    expect(
      (
        await screen.findByText("Connect to an agent to see which features are turned on.")
      ).getAttribute("role"),
    ).toBe("status");
    expect(screen.getByRole("link", { name: "Settings" })).toBeTruthy();
  });

  it("announces a provider page's failure as an alert and names the back link without its arrow", async () => {
    answers.set("/api/v1/runtime", online);
    answers.set("/api/v1/settings/provider", { status: 500 });
    mount("/workspace/provider", <ProviderDetail providerId="anthropic" />);
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Provider details could not be loaded.",
    );
    expect(screen.getByRole("link", { name: "Providers" })).toBeTruthy();
  });
});

describe("worktree picker states", () => {
  const props = {
    busy: false,
    canClear: true,
    canFork: true,
    error: false,
    onClear: () => {},
    onFork: () => {},
    onRetry: () => {},
    onSelect: () => {},
    pending: false,
  };
  const worktrees = {
    items: [
      { bare: false, branch: "main", kind: "", label: "Main", revision: "abc", selector: "a" },
    ],
  } as SessionWorktreesResponse;

  it("announces a failed read as an alert and an empty list as a status", () => {
    render(<WorktreePickerView {...props} error />);
    expect(screen.getByRole("alert").textContent).toBe("Worktrees could not be read.");
    cleanup();
    render(<WorktreePickerView {...props} worktrees={{ items: [] }} />);
    expect(screen.getByRole("status").textContent).toBe("No eligible worktrees for this session.");
  });

  it("disables every choice while a successor is being created", () => {
    render(<WorktreePickerView {...props} busy selected="a" worktrees={worktrees} />);
    for (const radio of screen.getAllByRole("radio")) {
      expect((radio as HTMLButtonElement).disabled).toBe(true);
    }
  });
});
