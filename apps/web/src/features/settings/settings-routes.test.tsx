// SPDX-License-Identifier: Apache-2.0

import {
  getRuntimeQueryKey,
  getUserMemoryQueryKey,
  listUserMemoryQueryKey,
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { authLoginUrl } from "../auth/auth-gate";
import { MemoryFactDetail, MemorySettings } from "./memory-settings";
import { isSettingsSection, settingsGroups } from "./settings-sections";
import { SettingsWorkspace } from "./settings-workspace";

async function load(path: string) {
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  });
  await router.load();
  return router;
}

function redirectLocation(router: Awaited<ReturnType<typeof load>>) {
  const result = router._serverResult;
  expect(result?.type).toBe("redirect");
  if (result?.type !== "redirect") throw new Error("Expected redirect");
  const location = result.redirect.headers.get("location");
  if (!location) throw new Error("Redirect has no location");
  return location;
}

describe("settings routes", () => {
  it("opens each settings section directly and preserves navigation", async () => {
    const sections = [
      "profile",
      "appearance",
      "agent",
      "permissions",
      "providers",
      "models",
      "mcp-tools",
      "storage",
      "memory",
      "learning",
      "diagnostics",
      "labs",
      "about",
    ];
    for (const section of sections) {
      expect(isSettingsSection(section)).toBe(true);
      const router = await load(`/workspace/settings/${section}`);
      expect(router.state.location.pathname).toBe(`/workspace/settings/${section}`);
      expect(router.state.matches.at(-1)?.routeId).toBe("/workspace/settings_/$section");
      expect(router.state.matches.at(-1)?.params).toMatchObject({ section });
    }
    expect(settingsGroups.flatMap((group) => group.items.map((item) => item.value))).toEqual(
      sections,
    );
    const navigation = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <RouterContextProvider router={await load("/workspace/settings/permissions")}>
          <SettingsWorkspace section="permissions" />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
    for (const section of sections) expect(navigation).toContain(`value="${section}"`);
    expect(navigation).toMatch(/<option[^>]*value="permissions"[^>]*selected/);
    expect(navigation).toMatch(/<button[^>]*aria-current="page"[^>]*>Permissions<\/button>/);
    const router = await load("/workspace/settings/profile");
    const memoryLocation = router.buildLocation({
      params: { section: "memory" },
      search: { item: undefined },
      to: "/workspace/settings/$section",
    });
    router.history.push(memoryLocation.href, {});
    await router.load();
    expect(router.state.location.pathname).toBe("/workspace/settings/memory");
    router.history.back();
    await router.load();
    expect(router.state.location.pathname).toBe("/workspace/settings/profile");
    router.history.forward();
    await router.load();
    expect(router.state.location.pathname).toBe("/workspace/settings/memory");
  });

  it("redirects old memory item URLs to fact details", async () => {
    const index = await load("/workspace/settings");
    expect(redirectLocation(index)).toBe("/workspace/settings/profile");

    const invalid = await load("/workspace/settings/not-real");
    expect(redirectLocation(invalid)).toBe("/workspace/settings/profile");

    const legacy = await load("/workspace/settings/memory?item=team%2Fvoice");
    const factUrl = redirectLocation(legacy);
    expect(factUrl).toBe("/workspace/memory?item=team%2Fvoice");
    const fact = await load(factUrl);
    expect(fact.state.location.search).toMatchObject({ item: "team/voice" });
    expect(fact.state.matches.at(-1)?.routeId).toBe("/workspace/memory");

    const missing = await load("/workspace/memory");
    expect(redirectLocation(missing)).toBe("/workspace/settings/memory");
    const empty = await load("/workspace/memory?item=");
    expect(redirectLocation(empty)).toBe("/workspace/settings/memory");
  });

  it("loads a memory fact after a legacy redirect", async () => {
    const key = "team/voice";
    const legacy = await load("/workspace/settings/memory?item=team%2Fvoice");
    const reloaded = await load(redirectLocation(legacy));
    expect(reloaded.state.location.pathname).toBe("/workspace/memory");
    expect(reloaded.state.location.search).toMatchObject({ item: key });

    const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
    queryClient.setQueryData(getRuntimeQueryKey(), {
      capabilities: { manualDream: { userModel: { decide: true, generate: true } } },
    });
    queryClient.setQueryData(listUserMemoryQueryKey(), {
      items: [{ description: "A voice preference", key }],
      reason: "",
      sha256: "abc",
      sizeBytes: "42",
      supported: true,
    });
    queryClient.setQueryData(getUserMemoryQueryKey({ path: { memoryKey: key } }), {
      current: {
        description: "A voice preference",
        key,
        origin: "conversation",
        sourceSessionId: "session-1",
        status: "active",
        updatedAt: null,
        value: "Use a calm voice.",
        version: "2",
        writer: "mecatl",
      },
      history: [
        {
          description: "A voice preference",
          key,
          origin: "conversation",
          sourceSessionId: "session-1",
          status: "active",
          updatedAt: null,
          value: "Use a short voice.",
          version: "1",
          writer: "mecatl",
        },
      ],
      historyAvailable: true,
    });
    const list = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <RouterContextProvider router={await load("/workspace/settings/memory")}>
          <MemorySettings />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
    expect(list).toContain('href="/workspace/memory?item=team%2Fvoice"');
    expect(list).toContain("Consolidate memory");
    expect(list).toContain("Generate plan");
    const detail = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <RouterContextProvider router={reloaded}>
          <MemoryFactDetail memoryKey={key} />
        </RouterContextProvider>
      </QueryClientProvider>,
    );
    expect(detail).toContain("Use a calm voice.");
    expect(detail).toContain("Revision history (1)");
    expect(detail).toContain("Use a short voice.");
    expect(detail).toContain('href="/workspace/settings/memory"');
  });

  it("round trips provider IDs containing slashes", async () => {
    const providerId = "team/openai";
    const url = `/workspace/provider?providerId=${encodeURIComponent(providerId)}`;
    const direct = await load(url);
    expect(direct.state.location.pathname).toBe("/workspace/provider");
    expect(direct.state.location.search).toMatchObject({ providerId });
    expect(direct.state.matches.at(-1)?.routeId).toBe("/workspace/provider");

    const reloaded = await load(direct.state.location.href);
    expect(reloaded.state.location.search).toMatchObject({ providerId });

    const absent = await load("/workspace/provider");
    expect(redirectLocation(absent)).toBe("/workspace/settings/providers");
  });

  it("returns to the requested settings detail after interactive login", async () => {
    const location = (await load("/workspace/memory?item=team%2Fvoice")).state.location;
    const url = authLoginUrl(location.href);
    expect(new URL(url, "http://studio.local").searchParams.get("return_to")).toBe(
      "/workspace/memory?item=team%2Fvoice",
    );
    const section = (await load("/workspace/settings/permissions")).state.location;
    expect(
      new URL(authLoginUrl(section.href), "http://studio.local").searchParams.get("return_to"),
    ).toBe("/workspace/settings/permissions");
  });
});
