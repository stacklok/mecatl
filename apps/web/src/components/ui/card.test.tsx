// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "./card";

afterEach(cleanup);

function slot(container: HTMLElement, name: string) {
  return container.querySelector(`[data-slot="${name}"]`) as HTMLElement;
}

describe("Card", () => {
  it("tags every part with its data-slot", () => {
    const { container } = render(
      <Card>
        <CardHeader>
          <CardTitle>Title</CardTitle>
          <CardDescription>Description</CardDescription>
          <CardAction>Action</CardAction>
        </CardHeader>
        <CardContent>Body</CardContent>
        <CardFooter>Footer</CardFooter>
      </Card>,
    );
    for (const [name, text] of [
      ["card", "TitleDescriptionActionBodyFooter"],
      ["card-header", "TitleDescriptionAction"],
      ["card-title", "Title"],
      ["card-description", "Description"],
      ["card-action", "Action"],
      ["card-content", "Body"],
      ["card-footer", "Footer"],
    ] as const) {
      expect(slot(container, name).textContent).toBe(text);
    }
  });

  it("makes the header a container grid that opens an action column", () => {
    const { container } = render(
      <CardHeader>
        <CardTitle>Title</CardTitle>
        <CardAction>Action</CardAction>
      </CardHeader>,
    );
    const header = slot(container, "card-header");
    expect(header.className).toContain("@container/card-header");
    expect(header.className).toContain("grid");
    expect(header.className).toContain("has-data-[slot=card-action]:grid-cols-[1fr_auto]");
    const action = slot(container, "card-action");
    expect(action.className).toContain("col-start-2");
    expect(action.className).toContain("row-span-2");
    expect(action.className).toContain("justify-self-end");
  });

  it("keeps Studio's card surface tokens and merges caller classes", () => {
    const { container } = render(<Card className="gap-3 py-4">Body</Card>);
    const card = slot(container, "card");
    expect(card.className).toContain("bg-card");
    expect(card.className).toContain("text-card-foreground");
    expect(card.className).toContain("gap-3");
    expect(card.className).not.toContain("gap-6");
    expect(card.className).not.toContain("py-6");
  });

  it("pads a bordered header and footer away from the body", () => {
    const { container } = render(
      <Card>
        <CardHeader className="border-b">Header</CardHeader>
        <CardFooter className="border-t">Footer</CardFooter>
      </Card>,
    );
    expect(slot(container, "card-header").className).toContain("[.border-b]:pb-6");
    expect(slot(container, "card-footer").className).toContain("[.border-t]:pt-6");
  });
});
