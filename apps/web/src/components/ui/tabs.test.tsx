// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./tabs";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("Tabs", () => {
  it("switches the visible panel when another tab is chosen", async () => {
    const user = userEvent.setup();
    render(
      <Tabs defaultValue="overview">
        <TabsList aria-label="Sections">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
        </TabsList>
        <TabsContent value="overview">Overview panel</TabsContent>
        <TabsContent value="logs">Logs panel</TabsContent>
      </Tabs>,
    );
    expect(screen.getByRole("tablist", { name: "Sections" }).getAttribute("data-slot")).toBe(
      "tabs-list",
    );
    const overview = screen.getByRole("tab", { name: "Overview" });
    const logs = screen.getByRole("tab", { name: "Logs" });
    expect(overview.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tabpanel").textContent).toBe("Overview panel");
    await user.click(logs);
    expect(logs.getAttribute("aria-selected")).toBe("true");
    expect(overview.getAttribute("aria-selected")).toBe("false");
    expect(screen.getByRole("tabpanel").textContent).toBe("Logs panel");
  });
});
