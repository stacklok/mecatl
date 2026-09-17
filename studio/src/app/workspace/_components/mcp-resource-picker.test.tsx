import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { McpResourceContent, McpResourceView } from "@/lib/harness/mcp";
import { NO_MCP_PROVIDER_TEXT } from "@/lib/harness/mcp";
import {
  binaryChunkNote,
  MCP_RESOURCES_UNSUPPORTED_TEXT,
  McpResourcePicker,
  NO_MCP_RESOURCES_TEXT,
  NO_RESOURCE_TEXT,
  PREVIEW_CHARS,
  previewTruncationNote,
  RESOURCE_PICKER_TITLE,
  resourceLabel,
} from "./mcp-resource-picker";

/**
 * The composer's MCP resource picker (mecatui's ctrl+r mcpResources →
 * mcpResourcePrev): lists resources with title-or-name, URI, type and size;
 * a click reads the resource into a preview pane; a long text is cut at
 * PREVIEW_CHARS with a note while the WHOLE text is inserted; binary chunks
 * are named and never inserted; a text-less resource cannot be inserted;
 * and the empty listing and every error class read as their own sentence.
 */

const mcp = vi.hoisted(() => ({
  list: vi.fn<
    (server?: string, signal?: AbortSignal) => Promise<McpResourceView[]>
  >(),
  read: vi.fn<(server: string, uri: string) => Promise<McpResourceContent>>(),
}));
vi.mock("@/lib/harness/mcp", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/mcp")>()),
  listHarnessMcpResources: (server?: string, signal?: AbortSignal) =>
    mcp.list(server, signal),
  readHarnessMcpResource: (server: string, uri: string) =>
    mcp.read(server, uri),
}));

const notes: McpResourceView = {
  server: "github",
  uri: "file:///notes.txt",
  name: "notes.txt",
  title: "Release notes",
  description: "What shipped.",
  mimeType: "text/plain",
  size: 2048,
  readOnly: true,
};
const bare: McpResourceView = {
  server: "slack",
  uri: "slack://channel/general",
  name: "",
  title: "",
  description: "",
  mimeType: "",
  size: 0,
  readOnly: false,
};

afterEach(() => {
  mcp.list.mockReset();
  mcp.read.mockReset();
});

function renderPicker(onInsert = vi.fn(), onOpenChange = vi.fn()) {
  render(
    <McpResourcePicker open onOpenChange={onOpenChange} onInsert={onInsert} />,
  );
  return { onInsert, onOpenChange };
}

describe("helpers", () => {
  it("labels a resource by title, else name, else URI", () => {
    expect(resourceLabel(notes)).toBe("Release notes");
    expect(resourceLabel({ ...notes, title: "" })).toBe("notes.txt");
    expect(resourceLabel(bare)).toBe("slack://channel/general");
  });

  it("words a binary chunk by type and size, never inserted", () => {
    expect(binaryChunkNote({ mimeType: "image/png", bytes: 1024 })).toBe(
      "(binary image/png, 1 KB — not inserted)",
    );
    expect(binaryChunkNote({ mimeType: "", bytes: 3 })).toContain(
      "unknown type",
    );
  });

  it("words the truncation note with both counts", () => {
    expect(previewTruncationNote(25_000)).toBe(
      "Preview shows the first 20,000 of 25,000 characters; the whole text is inserted (up to 64 KiB).",
    );
  });
});

describe("McpResourcePicker", () => {
  it("lists resources with label, server, type and size badges and the URI, and filters as you type", async () => {
    mcp.list.mockResolvedValue([notes, bare]);
    renderPicker();
    expect(
      screen.getByRole("dialog", { name: RESOURCE_PICKER_TITLE }),
    ).toBeInTheDocument();
    const rows = await screen.findAllByTestId("mcp-resource-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("Release notes");
    expect(rows[0]).toHaveTextContent("github");
    expect(rows[0]).toHaveTextContent("text/plain");
    expect(rows[0]).toHaveTextContent("2 KB");
    expect(rows[0]).toHaveTextContent("file:///notes.txt");
    expect(rows[0]).toHaveTextContent("What shipped.");
    expect(rows[1]).toHaveTextContent("slack://channel/general");
    expect(mcp.list).toHaveBeenCalledWith("", expect.any(AbortSignal));

    fireEvent.change(screen.getByLabelText("Filter resources"), {
      target: { value: "general" },
    });
    expect(screen.getAllByTestId("mcp-resource-row")).toHaveLength(1);
  });

  it("reads the picked resource into a preview and inserts its text, closing the dialog", async () => {
    mcp.list.mockResolvedValue([notes]);
    mcp.read.mockResolvedValue({
      text: "line one\nline two",
      binaryChunks: [],
    });
    const { onInsert, onOpenChange } = renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-resource-row"));
    const preview = await screen.findByTestId("mcp-resource-preview");
    expect(preview).toHaveTextContent("line one line two");
    expect(mcp.read).toHaveBeenCalledWith("github", "file:///notes.txt");
    expect(screen.queryByRole("note")).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Insert into message" }),
    );
    expect(onInsert).toHaveBeenCalledWith("line one\nline two");
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("cuts a long preview with a note but inserts the whole text, and names binary chunks without inserting them", async () => {
    const text = "x".repeat(PREVIEW_CHARS + 500);
    mcp.list.mockResolvedValue([notes]);
    mcp.read.mockResolvedValue({
      text,
      binaryChunks: [{ mimeType: "image/png", bytes: 1024 }],
    });
    const { onInsert } = renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-resource-row"));
    const preview = await screen.findByTestId("mcp-resource-preview");
    expect(preview.textContent).toHaveLength(PREVIEW_CHARS);
    expect(screen.getByRole("note")).toHaveTextContent(
      previewTruncationNote(text.length),
    );
    expect(
      screen.getByText("(binary image/png, 1 KB — not inserted)"),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Insert into message" }),
    );
    expect(onInsert).toHaveBeenCalledWith(text);
  });

  it("cannot insert a resource that holds no text, and Back returns to the list", async () => {
    mcp.list.mockResolvedValue([notes]);
    mcp.read.mockResolvedValue({
      text: "",
      binaryChunks: [{ mimeType: "application/pdf", bytes: 7 }],
    });
    renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-resource-row"));
    expect(await screen.findByText(NO_RESOURCE_TEXT)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Insert into message" }),
    ).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Back to list" }));
    expect(await screen.findByTestId("mcp-resource-row")).toBeInTheDocument();
  });

  it("says so when no resource is exposed", async () => {
    mcp.list.mockResolvedValue([]);
    renderPicker();
    expect(await screen.findByText(NO_MCP_RESOURCES_TEXT)).toBeInTheDocument();
  });

  it("words the daemon's no_mcp_provider refusal, an older daemon and a read failure", async () => {
    mcp.list.mockRejectedValueOnce(
      new HarnessApiError(412, "no_mcp_provider", "no provider"),
    );
    const first = render(
      <McpResourcePicker open onOpenChange={() => {}} onInsert={() => {}} />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      NO_MCP_PROVIDER_TEXT,
    );
    first.unmount();

    mcp.list.mockRejectedValueOnce(
      new HarnessApiError(404, "not_found", "no such route"),
    );
    const second = render(
      <McpResourcePicker open onOpenChange={() => {}} onInsert={() => {}} />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      MCP_RESOURCES_UNSUPPORTED_TEXT,
    );
    second.unmount();

    mcp.list.mockResolvedValue([notes]);
    mcp.read.mockRejectedValue(new Error("resource gone"));
    renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-resource-row"));
    expect(await screen.findByRole("alert")).toHaveTextContent("resource gone");
    expect(screen.getByTestId("mcp-resource-row")).toBeInTheDocument();
  });

  it("reads nothing while closed", () => {
    render(
      <McpResourcePicker
        open={false}
        onOpenChange={() => {}}
        onInsert={() => {}}
      />,
    );
    expect(mcp.list).not.toHaveBeenCalled();
  });
});
