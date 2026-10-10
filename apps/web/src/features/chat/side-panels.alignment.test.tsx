// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { ContentPreviewPanel } from "./content-preview-panel";
import { SessionSidebar } from "./session-sidebar";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  vi.restoreAllMocks();
});

function Canvas({ initial }: { initial: string }) {
  const [value, setValue] = useState(initial);
  return (
    <ContentPreviewPanel
      canvas={value}
      onCanvasChange={setValue}
      onClose={() => {}}
      preview={{ kind: "canvas" }}
    />
  );
}

describe("side panel frames", () => {
  it("numbers a code file's lines in the preview", () => {
    render(
      <ContentPreviewPanel
        canvas=""
        onCanvasChange={() => {}}
        onClose={() => {}}
        preview={{
          file: {
            content: "one\ntwo\nthree\n",
            kind: "code",
            name: "a.go",
            size: 14,
            type: "text/x-go",
          },
          kind: "file",
        }}
      />,
    );
    const rows = within(screen.getByRole("table")).getAllByRole("row");
    expect(rows).toHaveLength(3);
    expect(rows.map((row) => row.textContent)).toEqual(["1one", "2two", "3three"]);
  });

  it("copies and clears the canvas from its toolbar, and marks the active view", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    render(<Canvas initial="# Notes" />);
    const edit = screen.getByRole("button", { name: "Edit" });
    const preview = screen.getByRole("button", { name: "Preview" });
    expect(edit.getAttribute("aria-pressed")).toBe("true");
    expect(preview.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(screen.getByRole("button", { name: "Copy canvas" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("# Notes"));
    await user.click(preview);
    expect(preview.getAttribute("aria-pressed")).toBe("true");
    await user.click(preview);
    expect(preview.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Clear canvas" }));
    expect(screen.getByText("No canvas notes yet.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy canvas" })).toHaveProperty("disabled", true);
  });

  it("closes with the prototype's controls and keeps the maximize label", () => {
    render(
      <ContentPreviewPanel
        canvas=""
        onCanvasChange={() => {}}
        onClose={() => {}}
        preview={{ kind: "canvas" }}
      />,
    );
    const panel = screen.getByRole("complementary", { name: "Local canvas" });
    expect(within(panel).getByRole("button", { name: "Close panel" })).toBeTruthy();
    expect(within(panel).getByRole("heading", { name: "Local canvas" })).toBeTruthy();
  });
});

describe("inspect-only rows", () => {
  const base: SessionSummaryResponse = {
    capabilities: {
      copyId: true,
      copyIdReason: "",
      delete: false,
      deleteReason: "inspect_only_kind",
      fork: false,
      forkReason: "inspect_only_kind",
      inspect: true,
      inspectReason: "",
      publicChat: false,
      publicChatReason: "inspect_only_kind",
      rename: false,
      renameReason: "inspect_only_kind",
      viewTranscript: true,
      viewTranscriptReason: "",
    },
    createdAt: "2026-09-24T12:00:00.000Z",
    debugTargetSessionId: "",
    id: "worker",
    kind: "parallel_branch",
    modelId: "m",
    state: "completed",
    title: "",
    titleProvenance: "",
    titleRevision: "0",
    turns: 1,
    updatedAt: "2026-09-24T12:00:00.000Z",
  };

  it("shows a kind chip and a Read-only badge with the daemon's reason", () => {
    const onSelect = vi.fn();
    render(
      <SessionSidebar
        collapsed={false}
        creating={false}
        folders={{ assignments: {}, folders: [] }}
        items={[base]}
        onClose={() => {}}
        onCreate={() => {}}
        onCreateFolder={() => {}}
        onDelete={() => {}}
        onDeleteFolder={() => {}}
        onMoveToFolder={() => {}}
        onRename={() => {}}
        onRenameFolder={() => {}}
        onSelect={onSelect}
        open
        side="right"
      />,
    );
    const group = screen.getByRole("heading", { name: "Inspect-only sessions" }).closest("section");
    if (!group) throw new Error("group missing");
    const open = within(group).getByRole("button", { name: "Inspect run: Parallel branch" });
    expect(within(open).getAllByText("Parallel branch")).toHaveLength(2);
    expect(within(open).getByText("Read-only").getAttribute("title")).toBe("Read-only run");
    fireEvent.click(open);
    expect(onSelect).toHaveBeenCalledWith("worker");
  });
});
