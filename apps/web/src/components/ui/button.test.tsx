// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Button } from "./button";
import { DropdownMenu, DropdownMenuTrigger } from "./dropdown-menu";
import { Tooltip, TooltipTrigger } from "./tooltip";

afterEach(cleanup);

describe("Button", () => {
  it("tags itself with data-slot and data-button", () => {
    render(<Button>Save</Button>);
    const button = screen.getByRole("button", { name: "Save" });
    expect(button.getAttribute("data-slot")).toBe("button");
    expect(button.hasAttribute("data-button")).toBe(true);
  });

  it("keeps data-button inside asChild triggers, whose data-slot names the trigger", () => {
    render(
      <>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button>Refresh</Button>
          </TooltipTrigger>
        </Tooltip>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button>Options</Button>
          </DropdownMenuTrigger>
        </DropdownMenu>
      </>,
    );
    const refresh = screen.getByRole("button", { name: "Refresh" });
    expect(refresh.getAttribute("data-slot")).toBe("tooltip-trigger");
    expect(refresh.hasAttribute("data-button")).toBe(true);
    const options = screen.getByRole("button", { name: "Options" });
    expect(options.getAttribute("data-slot")).toBe("dropdown-menu-trigger");
    expect(options.hasAttribute("data-button")).toBe(true);
  });

  it("keeps data-button when it renders its child through asChild", () => {
    render(
      <Button asChild>
        <a href="/workspace">Home</a>
      </Button>,
    );
    const link = screen.getByRole("link", { name: "Home" });
    expect(link.getAttribute("data-slot")).toBe("button");
    expect(link.hasAttribute("data-button")).toBe(true);
  });
});
