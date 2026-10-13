// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatComposer } from "./chat-composer";
import { EditDiffBlock, WriteBlock } from "./edit-diff";
import { ReasoningDisclosure } from "./reasoning-disclosure";

/**
 * The chat labels that ellipsise long text (a diff's file path, the streaming
 * reasoning preview, an attached image's file name) used a native `title=`.
 * They now disclose the full text through `Tooltip onlyWhenTruncated`. These
 * tests pin that the full text stays in the label, that hovering a truncated
 * label shows all of it, and that a label that fits shows no tooltip.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

/** Gives a label the box the test wants; happy-dom has no layout. */
async function setOverflow(element: HTMLElement, overflowing: boolean) {
  Object.defineProperty(element, "clientWidth", { configurable: true, get: () => 100 });
  Object.defineProperty(element, "scrollWidth", {
    configurable: true,
    get: () => (overflowing ? 400 : 100),
  });
  Object.defineProperty(element, "clientHeight", { configurable: true, get: () => 16 });
  Object.defineProperty(element, "scrollHeight", { configurable: true, get: () => 16 });
  await act(async () => {
    window.dispatchEvent(new Event("resize"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
  });
}

/** Hovers a label and reports the tooltip text it opened, if any. */
async function hoverTooltip(label: HTMLElement) {
  const user = userEvent.setup();
  await user.hover(label);
  await act(async () => new Promise((resolve) => setTimeout(resolve, 10)));
  const tooltip = screen.queryByRole("tooltip");
  await user.unhover(label);
  return tooltip?.textContent ?? null;
}

const longPath = "apps/web/src/features/chat/a/really/deep/directory/tree/edit-diff-example.tsx";

describe("truncated chat labels", () => {
  for (const [name, block] of [
    ["edit", <EditDiffBlock key="edit" newText="b" oldText="a" path={longPath} />],
    ["write", <WriteBlock content="hello" key="write" path={longPath} />],
  ] as const) {
    it(`shows a ${name} diff's full path on hover only when it is cut off`, async () => {
      render(block);
      const label = screen.getByText(longPath);
      expect(label.className).toContain("truncate");
      expect(label.getAttribute("title")).toBeNull();

      await setOverflow(label, false);
      expect(await hoverTooltip(label)).toBeNull();

      await setOverflow(label, true);
      expect(await hoverTooltip(label)).toBe(longPath);
    });
  }

  it("shows the whole streaming reasoning line on hover when it is cut off", async () => {
    const line =
      "Comparing the three candidate schedules against the user's timezone and quiet hours";
    render(<ReasoningDisclosure streaming text={`first thought\n${line}`} />);
    const label = screen.getByText(line);
    expect(label.className).toContain("truncate");

    await setOverflow(label, false);
    expect(await hoverTooltip(label)).toBeNull();

    await setOverflow(label, true);
    expect(await hoverTooltip(label)).toBe(line);
  });

  it("shows an attached image's whole file name on hover when it is cut off", async () => {
    const user = userEvent.setup();
    const fileName = "screenshot-of-the-settings-page-after-the-palette-change.png";
    render(
      <ChatComposer
        imageAttachmentsSupported
        onPreviewImage={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await user.upload(
      screen.getByLabelText("Choose images to attach"),
      new File(["image data"], fileName, { type: "image/png" }),
    );
    const label = await waitFor(() => screen.getByText(fileName));
    expect(label.className).toContain("truncate");
    // The neighbouring controls keep their accessible names.
    expect(screen.getByRole("button", { name: `Preview ${fileName}` })).toBeTruthy();
    expect(screen.getByRole("button", { name: `Remove ${fileName}` })).toBeTruthy();

    await setOverflow(label, false);
    expect(await hoverTooltip(label)).toBeNull();

    await setOverflow(label, true);
    expect(await hoverTooltip(label)).toBe(fileName);
  });
});
