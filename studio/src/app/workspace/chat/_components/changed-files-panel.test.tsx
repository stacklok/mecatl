import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ChangedFile } from "@/lib/tool-summary";
import { memoryStorage } from "@/test/memory-storage";
import {
  ChangedFilesPanel,
  changedFilesHeading,
  describeFileChanges,
} from "./changed-files-panel";

// The panel frame (SidePanel) persists its width in localStorage; this vitest
// environment's storage shim is method-less, so a real in-memory Storage is
// stubbed per test (the global afterEach unstubs it).
beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

const mocks = vi.hoisted(() => ({
  copyToClipboard: vi.fn(async () => true),
}));

vi.mock("@/lib/clipboard", () => ({
  copyToClipboard: mocks.copyToClipboard,
}));

const files: ChangedFile[] = [
  { path: "src/b.ts", edits: 2, writes: 0 },
  {
    path: "docs/a.md",
    edits: 1,
    writes: 1,
    lastWrite: { path: "docs/a.md", name: "a.md", content: "# A" },
  },
];

const shared = {
  onClose: () => {},
  maximized: false,
  onToggleMaximize: () => {},
};

/**
 * The conversation-wide changed-files list (the TUI's "N files changed this
 * session" appendix): first-seen order, what happened to each path, Open for
 * a Write's content, Copy path for everything.
 */
describe("ChangedFilesPanel", () => {
  it("lists paths in first-seen order with their changes and the count heading", () => {
    render(<ChangedFilesPanel files={files} {...shared} />);
    expect(
      screen.getByText(/2 files changed in this conversation/),
    ).toBeInTheDocument();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("src/b.ts");
    expect(items[0]).toHaveTextContent("edited ×2");
    expect(items[1]).toHaveTextContent("docs/a.md");
    expect(items[1]).toHaveTextContent("written · edited");
  });

  it("offers Open only for a path with a Write, routing to the file preview", () => {
    const onOpenFile = vi.fn();
    render(
      <ChangedFilesPanel files={files} onOpenFile={onOpenFile} {...shared} />,
    );
    expect(screen.queryByRole("button", { name: "Open src/b.ts" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Open docs/a.md" }));
    expect(onOpenFile).toHaveBeenCalledWith({
      path: "docs/a.md",
      name: "a.md",
      content: "# A",
    });
  });

  it("copies a path through the one copy-with-feedback helper", () => {
    render(<ChangedFilesPanel files={files} {...shared} />);
    fireEvent.click(screen.getByRole("button", { name: "Copy path src/b.ts" }));
    expect(mocks.copyToClipboard).toHaveBeenCalledWith("src/b.ts", "Path");
  });

  it("says so when nothing changed", () => {
    render(<ChangedFilesPanel files={[]} {...shared} />);
    expect(
      screen.getByText("No files changed in this conversation yet."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("list")).toBeNull();
  });
});

describe("describeFileChanges / changedFilesHeading", () => {
  it("phrases the counts", () => {
    expect(describeFileChanges({ path: "p", edits: 1, writes: 0 })).toBe(
      "edited",
    );
    expect(describeFileChanges({ path: "p", edits: 0, writes: 3 })).toBe(
      "written ×3",
    );
    expect(describeFileChanges({ path: "p", edits: 2, writes: 1 })).toBe(
      "written · edited ×2",
    );
    expect(changedFilesHeading(1)).toBe("1 file changed in this conversation");
    expect(changedFilesHeading(4)).toBe("4 files changed in this conversation");
  });
});
