// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./collapsible";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("Collapsible", () => {
  it("shows and hides its content from a trigger that controls it", async () => {
    const user = userEvent.setup();
    render(
      <Collapsible>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent>Hidden body</CollapsibleContent>
      </Collapsible>,
    );
    const trigger = screen.getByRole("button", { name: "Details" });
    expect(trigger.getAttribute("data-slot")).toBe("collapsible-trigger");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Hidden body")).toBeNull();
    await user.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    const content = document.getElementById(trigger.getAttribute("aria-controls") ?? "");
    expect(content?.getAttribute("data-slot")).toBe("collapsible-content");
    expect(content?.textContent).toBe("Hidden body");
    await user.keyboard("{Enter}");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Hidden body")).toBeNull();
  });

  it("follows a controlled open state", () => {
    const { rerender } = render(
      <Collapsible open={false}>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent>Body</CollapsibleContent>
      </Collapsible>,
    );
    expect(screen.queryByText("Body")).toBeNull();
    rerender(
      <Collapsible open>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent>Body</CollapsibleContent>
      </Collapsible>,
    );
    expect(screen.getByText("Body")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Details" }).getAttribute("data-state")).toBe("open");
  });

  it("keeps closed content in the page as hidden until found, and opens on a match", async () => {
    const user = userEvent.setup();
    render(
      <Collapsible>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent hiddenUntilFound>Findable body</CollapsibleContent>
      </Collapsible>,
    );
    const trigger = screen.getByRole("button", { name: "Details" });
    const content = screen.getByText("Findable body");
    expect(content.getAttribute("hidden")).toBe("until-found");
    expect(content.getAttribute("data-state")).toBe("closed");

    await user.click(trigger);
    expect(trigger.getAttribute("aria-controls")).toBe(content.id);
    expect(content.hasAttribute("hidden")).toBe(false);
    await user.click(trigger);
    expect(content.getAttribute("hidden")).toBe("until-found");

    act(() => {
      content.dispatchEvent(new Event("beforematch", { bubbles: true }));
    });
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(content.hasAttribute("hidden")).toBe(false);
  });

  it("asks a controlled owner to open on a find-in-page match", () => {
    const changes: boolean[] = [];
    render(
      <Collapsible onOpenChange={(open) => changes.push(open)} open={false}>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent hiddenUntilFound>Findable body</CollapsibleContent>
      </Collapsible>,
    );
    const content = screen.getByText("Findable body");
    expect(content.getAttribute("hidden")).toBe("until-found");
    act(() => {
      content.dispatchEvent(new Event("beforematch", { bubbles: true }));
    });
    expect(changes).toEqual([true]);
  });
});
