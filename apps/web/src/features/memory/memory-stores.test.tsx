// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { MemoryStores } from "./memory-stores";

afterEach(cleanup);

describe("MemoryStores", () => {
  it("reports each store's on/off state from the runtime capabilities", () => {
    render(<MemoryStores capabilities={{ memory: true, userModel: false }} />);

    expect(
      screen.getByText("Memory is set where the agent runs and can’t be changed here."),
    ).toBeTruthy();
    expect(screen.getByText("Project memory")).toBeTruthy();
    expect(screen.getByText("Facts about you")).toBeTruthy();
    expect(screen.getByTestId("memory-status-project").textContent).toBe("On");
    expect(screen.getByTestId("memory-status-user-model").textContent).toBe("Off");
  });

  it("is a read-only report with no editable control", () => {
    render(<MemoryStores capabilities={{ memory: true, userModel: true }} />);

    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("never names the daemon, the BFF, or a flag", () => {
    render(<MemoryStores capabilities={{ memory: false, userModel: false }} />);

    expect(document.body.textContent).not.toMatch(/daemon|mecated|BFF|--/i);
  });
});
