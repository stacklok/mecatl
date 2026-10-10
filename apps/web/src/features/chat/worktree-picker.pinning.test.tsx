// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the worktree picker's choice before its native radios become the
 * shadcn radio group: one radio per eligible worktree, named by its label,
 * branch, and revision; the selected worktree is the only checked one; a
 * click on a radio or on its card chooses that worktree; and the actions wait
 * for a choice. A radio's state is read from `checked` or `aria-checked`,
 * whichever the control exposes, so these hold on both implementations.
 */

import type { SessionWorktreesResponse } from "@mecatl-studio/contracts";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorktreePickerView } from "./worktree-picker-dialog";

afterEach(cleanup);

const worktrees: SessionWorktreesResponse = {
  items: [
    {
      bare: false,
      branch: "feature/a",
      label: "Feature A",
      revision: "abcdef1234567",
      selector: "opaque-a",
    },
    { bare: true, branch: "", label: "Release", revision: "1234567890", selector: "opaque-b" },
  ],
} as SessionWorktreesResponse;

function isChecked(radio: HTMLElement): boolean {
  return (
    radio.getAttribute("aria-checked") === "true" || (radio as HTMLInputElement).checked === true
  );
}

function picker(props: Partial<Parameters<typeof WorktreePickerView>[0]> = {}) {
  const onSelect = vi.fn();
  const view = render(
    <WorktreePickerView
      busy={false}
      canClear
      canFork
      error={false}
      onClear={() => {}}
      onFork={() => {}}
      onRetry={() => {}}
      onSelect={onSelect}
      pending={false}
      worktrees={worktrees}
      {...props}
    />,
  );
  return { onSelect, view };
}

describe("worktree picker pins", () => {
  it("names one radio per worktree by its label, branch, and revision", () => {
    picker();
    const radios = screen.getAllByRole("radio");
    expect(radios).toHaveLength(2);
    expect(screen.getByRole("radio", { name: /Feature A/ }).closest("label")?.textContent).toBe(
      "Feature Afeature/aabcdef1",
    );
    expect(screen.getByRole("radio", { name: /Release/ }).closest("label")?.textContent).toBe(
      "Release1234567bare",
    );
    expect(radios.map(isChecked)).toEqual([false, false]);
  });

  it("checks only the selected worktree and offers the actions once one is chosen", () => {
    picker();
    expect(screen.getByRole("button", { name: "Fork in selected worktree" })).toHaveProperty(
      "disabled",
      true,
    );
    cleanup();
    picker({ selected: "opaque-b" });
    expect(screen.getAllByRole("radio").map(isChecked)).toEqual([false, true]);
    expect(screen.getByRole("button", { name: "Fork in selected worktree" })).toHaveProperty(
      "disabled",
      false,
    );
    expect(screen.getByRole("button", { name: "Clear in selected worktree" })).toHaveProperty(
      "disabled",
      false,
    );
  });

  it("chooses a worktree from its radio or from its card", () => {
    const { onSelect } = picker();
    fireEvent.click(screen.getByRole("radio", { name: /Release/ }));
    expect(onSelect).toHaveBeenLastCalledWith("opaque-b");
    fireEvent.click(screen.getByText("feature/a"));
    expect(onSelect).toHaveBeenLastCalledWith("opaque-a");
  });
});
