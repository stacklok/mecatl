// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as React from "react";
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

  it("names its content in aria-controls only while open, as Radix does", async () => {
    const user = userEvent.setup();
    render(
      <Collapsible>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent hiddenUntilFound>Findable body</CollapsibleContent>
      </Collapsible>,
    );
    const trigger = screen.getByRole("button", { name: "Details" });
    expect(trigger.hasAttribute("aria-controls")).toBe(false);
    await user.click(trigger);
    expect(trigger.getAttribute("aria-controls")).toBe(screen.getByText("Findable body").id);
  });

  it("leaves an open outer collapsible alone when a match opens a nested one", () => {
    // A toggle-style owner, like the tool-call list: it flips on any change
    // request and ignores the value, so a stray request would close it.
    let outerToggles = 0;
    function Outer() {
      const [open, setOpen] = React.useState(true);
      return (
        <Collapsible
          onOpenChange={() => {
            outerToggles += 1;
            setOpen((current) => !current);
          }}
          open={open}
        >
          <CollapsibleTrigger>Outer</CollapsibleTrigger>
          <CollapsibleContent hiddenUntilFound>
            <Collapsible>
              <CollapsibleTrigger>Inner</CollapsibleTrigger>
              <CollapsibleContent hiddenUntilFound>Nested body</CollapsibleContent>
            </Collapsible>
          </CollapsibleContent>
        </Collapsible>
      );
    }
    render(<Outer />);
    const outer = screen.getByRole("button", { name: "Outer" });
    const inner = screen.getByRole("button", { name: "Inner" });
    const nested = screen.getByText("Nested body");
    expect(outer.getAttribute("aria-expanded")).toBe("true");
    expect(nested.getAttribute("hidden")).toBe("until-found");

    act(() => {
      nested.dispatchEvent(new Event("beforematch", { bubbles: true }));
    });
    expect(inner.getAttribute("aria-expanded")).toBe("true");
    expect(nested.hasAttribute("hidden")).toBe(false);
    expect(outer.getAttribute("aria-expanded")).toBe("true");
    expect(outerToggles).toBe(0);
  });

  it("opens only for a match in its own content, not one that bubbles up from a nested collapsible", () => {
    render(
      <Collapsible>
        <CollapsibleTrigger>Outer</CollapsibleTrigger>
        <CollapsibleContent hiddenUntilFound>
          <Collapsible>
            <CollapsibleTrigger>Inner</CollapsibleTrigger>
            <CollapsibleContent hiddenUntilFound>Nested body</CollapsibleContent>
          </Collapsible>
        </CollapsibleContent>
      </Collapsible>,
    );
    act(() => {
      screen.getByText("Nested body").dispatchEvent(new Event("beforematch", { bubbles: true }));
    });
    expect(
      screen.getByRole("button", { name: "Inner", hidden: true }).getAttribute("aria-expanded"),
    ).toBe("true");
    expect(screen.getByRole("button", { name: "Outer" }).getAttribute("aria-expanded")).toBe(
      "false",
    );
  });

  it("does not report a change when asked to open an already open collapsible", () => {
    const changes: boolean[] = [];
    render(
      <Collapsible onOpenChange={(open) => changes.push(open)} open>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent hiddenUntilFound>Findable body</CollapsibleContent>
      </Collapsible>,
    );
    act(() => {
      screen.getByText("Findable body").dispatchEvent(new Event("beforematch", { bubbles: true }));
    });
    expect(changes).toEqual([]);
  });
});
