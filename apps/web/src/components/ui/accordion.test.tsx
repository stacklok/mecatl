// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "./accordion";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("Accordion", () => {
  it("expands and collapses an item", async () => {
    const user = userEvent.setup();
    render(
      <Accordion collapsible type="single">
        <AccordionItem value="one">
          <AccordionTrigger>First section</AccordionTrigger>
          <AccordionContent>First body</AccordionContent>
        </AccordionItem>
        <AccordionItem value="two">
          <AccordionTrigger>Second section</AccordionTrigger>
          <AccordionContent>Second body</AccordionContent>
        </AccordionItem>
      </Accordion>,
    );
    const first = screen.getByRole("button", { name: "First section" });
    expect(first.getAttribute("data-slot")).toBe("accordion-trigger");
    expect(first.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("First body")).toBeNull();
    await user.click(first);
    expect(first.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("region", { name: "First section" }).textContent).toBe("First body");
    await user.click(screen.getByRole("button", { name: "Second section" }));
    expect(first.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("First body")).toBeNull();
    expect(screen.getByText("Second body")).toBeTruthy();
  });
});
