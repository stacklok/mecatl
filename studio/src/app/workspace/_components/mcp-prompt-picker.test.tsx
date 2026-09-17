import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { McpPromptView, McpRenderedPrompt } from "@/lib/harness/mcp";
import { NO_MCP_PROVIDER_TEXT } from "@/lib/harness/mcp";
import {
  argumentsToSend,
  MCP_PROMPTS_UNSUPPORTED_TEXT,
  McpPromptPicker,
  NO_MCP_PROMPTS_TEXT,
  PROMPT_PICKER_TITLE,
  promptLabel,
  requiredArgumentsFilled,
} from "./mcp-prompt-picker";

/**
 * The composer's MCP prompt picker (mecatui's f8 mcpPrompts → mcpPromptArgs):
 * lists the prompts with title-or-name, server badge and description; filters
 * as you type; a prompt with arguments opens a form whose Render is held
 * until every REQUIRED argument is filled, fields in argument order for Tab;
 * the preview shows every rendered message; Insert hands the role-prefixed,
 * blank-line-joined text to the caller and closes; a prompt without
 * arguments renders straight from the list; the empty listing, the daemon's
 * no_mcp_provider refusal, an older daemon and any other error each read as
 * their own sentence.
 */

const mcp = vi.hoisted(() => ({
  list: vi.fn<
    (server?: string, signal?: AbortSignal) => Promise<McpPromptView[]>
  >(),
  get: vi.fn<
    (
      server: string,
      name: string,
      args: Record<string, string>,
    ) => Promise<McpRenderedPrompt>
  >(),
}));
vi.mock("@/lib/harness/mcp", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/mcp")>()),
  listHarnessMcpPrompts: (server?: string, signal?: AbortSignal) =>
    mcp.list(server, signal),
  getHarnessMcpPrompt: (
    server: string,
    name: string,
    args: Record<string, string>,
  ) => mcp.get(server, name, args),
}));

const summarize: McpPromptView = {
  server: "github",
  name: "summarize",
  title: "Summarize a file",
  description: "Summarizes one workspace file.",
  arguments: [
    {
      name: "path",
      title: "File path",
      description: "Workspace-relative path.",
      required: true,
    },
    { name: "tone", title: "", description: "", required: false },
  ],
};
const greet: McpPromptView = {
  server: "slack",
  name: "greet",
  title: "",
  description: "",
  arguments: [],
};

afterEach(() => {
  mcp.list.mockReset();
  mcp.get.mockReset();
});

function renderPicker(onInsert = vi.fn(), onOpenChange = vi.fn()) {
  render(
    <McpPromptPicker open onOpenChange={onOpenChange} onInsert={onInsert} />,
  );
  return { onInsert, onOpenChange };
}

describe("helpers", () => {
  it("labels a prompt by title, else name", () => {
    expect(promptLabel(summarize)).toBe("Summarize a file");
    expect(promptLabel(greet)).toBe("greet");
  });

  it("holds Render until every required argument is non-blank", () => {
    expect(requiredArgumentsFilled(summarize, {})).toBe(false);
    expect(requiredArgumentsFilled(summarize, { path: "  " })).toBe(false);
    expect(requiredArgumentsFilled(summarize, { path: "a.md" })).toBe(true);
    expect(requiredArgumentsFilled(greet, {})).toBe(true);
  });

  it("sends only the non-blank arguments", () => {
    expect(argumentsToSend({ path: "a.md", tone: "", extra: " " })).toEqual({
      path: "a.md",
    });
  });
});

describe("McpPromptPicker", () => {
  it("lists prompts with title-or-name, server badge and description, and filters as you type", async () => {
    mcp.list.mockResolvedValue([summarize, greet]);
    renderPicker();
    expect(
      screen.getByRole("dialog", { name: PROMPT_PICKER_TITLE }),
    ).toBeInTheDocument();
    const rows = await screen.findAllByTestId("mcp-prompt-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("Summarize a file");
    expect(rows[0]).toHaveTextContent("github");
    expect(rows[0]).toHaveTextContent("Summarizes one workspace file.");
    expect(rows[0]).toHaveTextContent("2 arguments");
    expect(rows[1]).toHaveTextContent("greet");
    expect(mcp.list).toHaveBeenCalledWith("", expect.any(AbortSignal));

    fireEvent.change(screen.getByLabelText("Filter prompts"), {
      target: { value: "gre" },
    });
    expect(screen.getAllByTestId("mcp-prompt-row")).toHaveLength(1);
    expect(screen.getByTestId("mcp-prompt-row")).toHaveTextContent("greet");
  });

  it("holds Render until the required argument is filled, tabs through the fields in order, previews every message and inserts the joined text", async () => {
    const user = userEvent.setup();
    mcp.list.mockResolvedValue([summarize]);
    mcp.get.mockResolvedValue({
      description: "Summarizes one workspace file.",
      messages: [
        { role: "user", text: "Summarize README.md" },
        { role: "assistant", text: "Reading it." },
      ],
    });
    const { onInsert, onOpenChange } = renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-prompt-row"));

    const path = screen.getByLabelText(/File path/);
    const tone = screen.getByLabelText(/^tone/);
    expect(path).toHaveAttribute("aria-required", "true");
    expect(tone).toHaveAttribute("aria-required", "false");
    expect(screen.getByText("Workspace-relative path.")).toBeInTheDocument();
    const renderButton = screen.getByRole("button", { name: "Render" });
    expect(renderButton).toBeDisabled();

    // Tab order follows the argument order.
    expect(path).toHaveFocus();
    await user.tab();
    expect(tone).toHaveFocus();
    await user.tab({ shift: true });
    expect(path).toHaveFocus();

    await user.type(path, "README.md");
    expect(renderButton).toBeEnabled();
    fireEvent.click(renderButton);

    expect(await screen.findByText("Summarize README.md")).toBeInTheDocument();
    expect(screen.getByText("Reading it.")).toBeInTheDocument();
    expect(screen.getByText("user")).toBeInTheDocument();
    expect(screen.getByText("assistant")).toBeInTheDocument();
    // Blank optional fields are left out of the request.
    expect(mcp.get).toHaveBeenCalledWith("github", "summarize", {
      path: "README.md",
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Insert into message" }),
    );
    expect(onInsert).toHaveBeenCalledWith(
      "user: Summarize README.md\n\nassistant: Reading it.",
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("submits the argument form on Enter once it is complete", async () => {
    const user = userEvent.setup();
    mcp.list.mockResolvedValue([summarize]);
    mcp.get.mockResolvedValue({ description: "", messages: [] });
    renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-prompt-row"));
    const path = screen.getByLabelText(/File path/);
    await user.type(path, "{Enter}");
    expect(mcp.get).not.toHaveBeenCalled();
    await user.type(path, "a.md{Enter}");
    await waitFor(() =>
      expect(mcp.get).toHaveBeenCalledWith("github", "summarize", {
        path: "a.md",
      }),
    );
  });

  it("renders a prompt without arguments straight from the list, and Back returns to the list", async () => {
    mcp.list.mockResolvedValue([greet]);
    mcp.get.mockResolvedValue({
      description: "",
      messages: [{ role: "", text: "Hello there" }],
    });
    renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-prompt-row"));
    expect(await screen.findByText("Hello there")).toBeInTheDocument();
    expect(mcp.get).toHaveBeenCalledWith("slack", "greet", {});
    expect(screen.queryByRole("button", { name: "Render" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(await screen.findByTestId("mcp-prompt-row")).toBeInTheDocument();
  });

  it("says so when no prompt is exposed", async () => {
    mcp.list.mockResolvedValue([]);
    renderPicker();
    expect(await screen.findByText(NO_MCP_PROMPTS_TEXT)).toBeInTheDocument();
  });

  it("words the daemon's no_mcp_provider refusal, an older daemon and any other error", async () => {
    mcp.list.mockRejectedValueOnce(
      new HarnessApiError(412, "no_mcp_provider", "no provider"),
    );
    const first = render(
      <McpPromptPicker open onOpenChange={() => {}} onInsert={() => {}} />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      NO_MCP_PROVIDER_TEXT,
    );
    first.unmount();

    mcp.list.mockRejectedValueOnce(
      new HarnessApiError(404, "not_found", "no such route"),
    );
    const second = render(
      <McpPromptPicker open onOpenChange={() => {}} onInsert={() => {}} />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      MCP_PROMPTS_UNSUPPORTED_TEXT,
    );
    second.unmount();

    mcp.list.mockRejectedValueOnce(new Error("server github: timeout"));
    render(
      <McpPromptPicker open onOpenChange={() => {}} onInsert={() => {}} />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "server github: timeout",
    );
  });

  it("shows a render failure verbatim and keeps the form", async () => {
    mcp.list.mockResolvedValue([greet]);
    mcp.get.mockRejectedValue(new Error("prompt greet: not found"));
    renderPicker();
    fireEvent.click(await screen.findByTestId("mcp-prompt-row"));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "prompt greet: not found",
    );
    expect(screen.getByTestId("mcp-prompt-row")).toBeInTheDocument();
  });

  it("reads nothing while closed", () => {
    render(
      <McpPromptPicker
        open={false}
        onOpenChange={() => {}}
        onInsert={() => {}}
      />,
    );
    expect(mcp.list).not.toHaveBeenCalled();
  });
});
