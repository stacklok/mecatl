// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { GetRuntimeResponse } from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter } from "@tanstack/react-router";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { ShortcutReference } from "./shortcut-reference";
import { keycaps, shortcutRegistry } from "./shortcut-registry";

function render(runtime?: GetRuntimeResponse) {
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  if (runtime) client.setQueryData(getRuntimeQueryKey(), runtime);
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <ShortcutReference />
    </QueryClientProvider>,
  );
}

const runtime = {
  capabilities: { skills: true, steer: true, scheduling: false },
  connection: "online",
} as GetRuntimeResponse;

describe("shortcut reference", () => {
  it("renders shortcuts from the registry on direct load", async () => {
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/shortcuts"] }),
      routeTree,
    });
    await router.load();
    expect(router.state.matches.at(-1)?.routeId).toBe("/workspace/shortcuts");

    const html = render(runtime);
    const text = document.createElement("div");
    text.innerHTML = html;
    for (const shortcut of shortcutRegistry) {
      expect(text.textContent).toContain(shortcut.description);
      for (const keycap of keycaps(shortcut.combo, navigator.platform.includes("Mac"))) {
        expect(html).toContain(keycap);
      }
    }
    expect(html).toContain("Skills");
    expect(html).toContain("Steer");
    expect(html).toContain("Features on this agent");
    expect(html).not.toContain("Scheduled runs live under the Scheduled tab");

    const offline = render({ ...runtime, connection: "offline" });
    expect(offline).toContain("Connect to an agent to see which features are turned on.");
    expect(offline).not.toContain("Browse and manage skills under Skills");
    expect(render()).toContain("Checking what&#x27;s turned on");
  });
});
