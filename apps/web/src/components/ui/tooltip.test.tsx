// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./tooltip";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

/** Gives the trigger a box the test controls; happy-dom has no layout. */
function size(element: HTMLElement, box: { client: number; scroll: number }) {
  Object.defineProperty(element, "clientWidth", { configurable: true, get: () => box.client });
  Object.defineProperty(element, "scrollWidth", { configurable: true, get: () => box.scroll });
  Object.defineProperty(element, "clientHeight", { configurable: true, get: () => 20 });
  Object.defineProperty(element, "scrollHeight", { configurable: true, get: () => 20 });
}

/** Re-measures through the resize listener `useIsTruncated` installs, then lets its frame run. */
async function remeasure() {
  await act(async () => {
    window.dispatchEvent(new Event("resize"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
  });
}

async function focus(element: HTMLElement) {
  await act(async () => element.focus());
}

async function blur(element: HTMLElement) {
  await act(async () => element.blur());
}

describe("Tooltip", () => {
  it("opens without an app-level provider and tags its parts", async () => {
    render(
      <Tooltip>
        <TooltipTrigger>Chats</TooltipTrigger>
        <TooltipContent>Open chats</TooltipContent>
      </Tooltip>,
    );
    const trigger = screen.getByRole("button", { name: "Chats" });
    expect(trigger.getAttribute("data-slot")).toBe("tooltip-trigger");
    await focus(trigger);
    expect((await screen.findByRole("tooltip")).textContent).toBe("Open chats");
    const content = document.querySelector('[data-slot="tooltip-content"]') as HTMLElement;
    expect(content.className).toContain("bg-foreground");
    expect(content.className).toContain("text-background");
  });

  it("keeps working inside Studio's app-level TooltipProvider", async () => {
    render(
      <TooltipProvider delayDuration={500}>
        <Tooltip>
          <TooltipTrigger>Skills</TooltipTrigger>
          <TooltipContent>Open skills</TooltipContent>
        </Tooltip>
      </TooltipProvider>,
    );
    await focus(screen.getByRole("button", { name: "Skills" }));
    expect((await screen.findByRole("tooltip")).textContent).toBe("Open skills");
  });

  it("honours a controlled open state", () => {
    render(
      <Tooltip open>
        <TooltipTrigger>Settings</TooltipTrigger>
        <TooltipContent>Open settings</TooltipContent>
      </Tooltip>,
    );
    expect(screen.getByRole("tooltip").textContent).toBe("Open settings");
  });

  it("opens an onlyWhenTruncated tooltip only while its trigger overflows", async () => {
    const ref = createRef<HTMLButtonElement>();
    render(
      <Tooltip onlyWhenTruncated>
        <TooltipTrigger className="truncate" ref={ref}>
          a/very/long/path/to/a/file.ts
        </TooltipTrigger>
        <TooltipContent>a/very/long/path/to/a/file.ts</TooltipContent>
      </Tooltip>,
    );
    const trigger = screen.getByRole("button");
    expect(ref.current).toBe(trigger);
    const box = { client: 200, scroll: 120 };
    size(trigger, box);
    await remeasure();

    await focus(trigger);
    expect(screen.queryByRole("tooltip")).toBeNull();
    await blur(trigger);

    box.scroll = 480;
    await remeasure();
    await focus(trigger);
    await waitFor(() =>
      expect(screen.getByRole("tooltip").textContent).toBe("a/very/long/path/to/a/file.ts"),
    );

    box.scroll = 120;
    await remeasure();
    await waitFor(() => expect(screen.queryByRole("tooltip")).toBeNull());
  });
});
