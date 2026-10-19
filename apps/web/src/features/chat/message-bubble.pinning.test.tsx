// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MessageAvatar } from "./message-bubble";

// Pins the message avatar's observable contract before it moves onto the
// Avatar primitive: its accessible name, the icon and colours of each
// fallback, and the frame size at both breakpoints. The class checks read
// the whole avatar subtree, so they hold whether the frame and the colours
// sit on one element or on a root and its fallback.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** A stand-in for `window.Image` whose picture has already loaded (or failed). */
function stubImage(loaded: boolean) {
  class StubImage extends EventTarget {
    complete = true;
    crossOrigin: string | null = null;
    naturalWidth = loaded ? 32 : 0;
    referrerPolicy = "";
    src = "";
  }
  vi.stubGlobal("Image", StubImage);
}

function avatarClasses(container: HTMLElement): Set<string> {
  const root = container.firstElementChild;
  if (!root) throw new Error("the avatar rendered nothing");
  const classes = new Set<string>();
  for (const element of [root, ...root.querySelectorAll("*")]) {
    for (const name of (element.getAttribute("class") ?? "").split(/\s+/u)) {
      if (name) classes.add(name);
    }
  }
  return classes;
}

function expectClasses(container: HTMLElement, expected: string[]) {
  const classes = avatarClasses(container);
  for (const name of expected) expect(classes.has(name), name).toBe(true);
}

describe("message avatar", () => {
  it("draws the agent fallback as a brand circle with the bot icon, hidden from assistive tech", () => {
    const { container } = render(<MessageAvatar avatarUrl="" fallback="agent" name="Mecatl" />);
    expect(screen.queryAllByRole("img")).toHaveLength(0);
    expect(container.querySelector(".lucide-bot")).not.toBeNull();
    expect(container.querySelector(".lucide-user")).toBeNull();
    expect(container.firstElementChild?.getAttribute("aria-hidden")).toBe("true");
    expect(container.textContent).toBe("");
    expectClasses(container, [
      "size-7",
      "lg:size-9",
      "shrink-0",
      "rounded-full",
      "flex",
      "items-center",
      "justify-center",
      "bg-brand",
      "text-white",
      "size-4",
      "lg:size-5",
    ]);
    expect(avatarClasses(container).has("bg-muted")).toBe(false);
  });

  it("draws the user fallback as a muted circle with the person icon, hidden from assistive tech", () => {
    const { container } = render(<MessageAvatar avatarUrl="" fallback="user" name="Sam" />);
    expect(screen.queryAllByRole("img")).toHaveLength(0);
    expect(container.querySelector(".lucide-user")).not.toBeNull();
    expect(container.querySelector(".lucide-bot")).toBeNull();
    expect(container.firstElementChild?.getAttribute("aria-hidden")).toBe("true");
    expectClasses(container, [
      "size-7",
      "lg:size-9",
      "rounded-full",
      "bg-muted",
      "text-muted-foreground",
      "size-4",
      "lg:size-5",
    ]);
    expect(avatarClasses(container).has("bg-brand")).toBe(false);
  });

  it("shows a configured picture as a round, cropped image named after the speaker", async () => {
    stubImage(true);
    const { container } = render(
      <MessageAvatar avatarUrl="data:image/png;base64,Yg==" fallback="user" name="Sam" />,
    );
    await waitFor(() => expect(container.querySelector("img")).not.toBeNull());
    const image = screen.getByRole("img", { name: "Sam" });
    expect(image.tagName).toBe("IMG");
    expect(image.getAttribute("src")).toBe("data:image/png;base64,Yg==");
    expect(container.querySelector(".lucide-user")).toBeNull();
    expectClasses(container, ["size-7", "lg:size-9", "rounded-full", "object-cover"]);
  });

  it("names the avatar after the speaker even before a picture has loaded", () => {
    render(<MessageAvatar avatarUrl="data:image/png;base64,YQ==" fallback="agent" name="Mecatl" />);
    expect(screen.getByRole("img", { name: "Mecatl" })).toBeTruthy();
  });

  it("names the avatar after the speaker when its picture fails to load", async () => {
    stubImage(false);
    render(
      <MessageAvatar avatarUrl="https://example.invalid/a.png" fallback="agent" name="Mecatl" />,
    );
    await waitFor(() => expect(screen.getByRole("img", { name: "Mecatl" })).toBeTruthy());
    expect(screen.getAllByRole("img")).toHaveLength(1);
  });
});
