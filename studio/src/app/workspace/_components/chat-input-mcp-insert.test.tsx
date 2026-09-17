import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { McpPromptView, McpRenderedPrompt } from "@/lib/harness/mcp";
import { ShortcutsProvider } from "@/lib/shortcuts/use-shortcuts";
import { ChatInput } from "./chat-input";
import {
  MCP_INSERT_LABEL,
  requestOpenMcpPicker,
  resetMcpPickerOpener,
} from "./mcp-composer-insert";
import { PROMPT_PICKER_TITLE } from "./mcp-prompt-picker";
import { RESOURCE_PICKER_TITLE } from "./mcp-resource-picker";

/**
 * "Insert from MCP" through the rendered ChatInput (the TUI's f8 / ctrl+r
 * pickers): the toolbar entry exists ONLY when the daemon grants
 * `capabilities.mcp`; picking a prompt appends the rendered text AFTER the
 * existing draft, as paragraphs separated by a blank line, and sends nothing;
 * the `mcp.prompts` chord opens the picker from the composer; an outside
 * open request (`requestOpenMcpPicker`, the slash built-ins' path) reaches
 * the mounted composer and is refused while the capability is off.
 */

const runtime = vi.hoisted(() => ({
  serverCapabilities: {} as Record<string, unknown>,
}));
vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    serverCapabilities: runtime.serverCapabilities,
  }),
  useOptionalRuntimeStatus: () => ({
    connected: true,
    serverCapabilities: runtime.serverCapabilities,
  }),
}));

const mcp = vi.hoisted(() => ({
  listPrompts: vi.fn<() => Promise<McpPromptView[]>>(),
  getPrompt: vi.fn<() => Promise<McpRenderedPrompt>>(),
  listResources: vi.fn<() => Promise<never[]>>(),
}));
vi.mock("@/lib/harness/mcp", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/mcp")>()),
  listHarnessMcpPrompts: () => mcp.listPrompts(),
  getHarnessMcpPrompt: () => mcp.getPrompt(),
  listHarnessMcpResources: () => mcp.listResources(),
}));

// jsdom lays nothing out: ProseMirror's scroll-into-view after a focus asks
// the selection for its rects, so give it empty ones.
const zeroRect = () =>
  ({
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    width: 0,
    height: 0,
    toJSON: () => ({}),
  }) as DOMRect;
for (const proto of [Element.prototype, Range.prototype]) {
  if (!proto.getClientRects) {
    proto.getClientRects = () => [] as unknown as DOMRectList;
  }
  if (!proto.getBoundingClientRect) {
    proto.getBoundingClientRect = zeroRect;
  }
}

const greet: McpPromptView = {
  server: "fixture-mcp",
  name: "greet",
  title: "Greet",
  description: "",
  arguments: [],
};

afterEach(() => {
  runtime.serverCapabilities = {};
  mcp.listPrompts.mockReset();
  mcp.getPrompt.mockReset();
  mcp.listResources.mockReset();
  resetMcpPickerOpener();
});

async function renderComposer(props: Parameters<typeof ChatInput>[0] = {}) {
  const utils = render(
    <ShortcutsProvider>
      <ChatInput {...props} />
    </ShortcutsProvider>,
  );
  const dom = await waitFor(() => {
    const el = utils.container.querySelector<HTMLElement>(".ProseMirror");
    if (!el) throw new Error("editor not mounted yet");
    return el;
  });
  return { ...utils, dom };
}

/** The composer's paragraphs, one string each ("" for a blank line). */
const paragraphs = (dom: HTMLElement) =>
  Array.from(dom.querySelectorAll("p")).map((p) => p.textContent ?? "");

describe("ChatInput — Insert from MCP", () => {
  it("offers no MCP entry and refuses outside open requests without the mcp capability", async () => {
    await renderComposer();
    expect(screen.queryByRole("button", { name: MCP_INSERT_LABEL })).toBeNull();
    expect(requestOpenMcpPicker("prompt")).toBe(false);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("appends the rendered prompt after the draft as paragraphs, and sends nothing", async () => {
    runtime.serverCapabilities = { mcp: true };
    mcp.listPrompts.mockResolvedValue([greet]);
    mcp.getPrompt.mockResolvedValue({
      description: "",
      messages: [{ role: "user", text: "Hello there\nsecond line" }],
    });
    const onSend = vi.fn();
    const user = userEvent.setup();
    const { dom } = await renderComposer({
      onSend,
      initialText: "Look at this:",
    });
    await waitFor(() => expect(paragraphs(dom)).toEqual(["Look at this:"]));

    await user.click(screen.getByRole("button", { name: MCP_INSERT_LABEL }));
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Prompt/ }));
    expect(
      await screen.findByRole("dialog", { name: PROMPT_PICKER_TITLE }),
    ).toBeInTheDocument();
    fireEvent.click(await screen.findByTestId("mcp-prompt-row"));
    fireEvent.click(
      await screen.findByRole("button", { name: "Insert into message" }),
    );

    await waitFor(() =>
      expect(paragraphs(dom)).toEqual([
        "Look at this:",
        "",
        "user: Hello there",
        "second line",
      ]),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: PROMPT_PICKER_TITLE }),
      ).toBeNull(),
    );
    expect(onSend).not.toHaveBeenCalled();
    // The picker never lists resources on a prompt pick (one RPC per picker).
    expect(mcp.listResources).not.toHaveBeenCalled();
  });

  it("opens the prompt picker from the composer on the mcp.prompts chord", async () => {
    runtime.serverCapabilities = { mcp: true };
    mcp.listPrompts.mockResolvedValue([]);
    const { dom } = await renderComposer();
    act(() => {
      dom.focus();
    });
    const event = new KeyboardEvent("keydown", {
      key: "'",
      metaKey: true,
      bubbles: true,
      cancelable: true,
    });
    act(() => {
      dom.dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(true);
    expect(
      await screen.findByRole("dialog", { name: PROMPT_PICKER_TITLE }),
    ).toBeInTheDocument();
  });

  it("offers the two pickers in the mobile options sheet too", async () => {
    runtime.serverCapabilities = { mcp: true };
    mcp.listResources.mockResolvedValue([]);
    await renderComposer();
    fireEvent.click(screen.getByRole("button", { name: "Composer options" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Insert an MCP resource" }),
    );
    expect(
      await screen.findByRole("dialog", { name: RESOURCE_PICKER_TITLE }),
    ).toBeInTheDocument();
  });

  it("hides the mobile rows without the capability", async () => {
    await renderComposer();
    fireEvent.click(screen.getByRole("button", { name: "Composer options" }));
    expect(await screen.findByText("Add a file")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Insert an MCP prompt" }),
    ).toBeNull();
  });

  it("opens the resource picker for an outside request while the capability is on", async () => {
    runtime.serverCapabilities = { mcp: true };
    mcp.listResources.mockResolvedValue([]);
    await renderComposer();
    let accepted = false;
    act(() => {
      accepted = requestOpenMcpPicker("resource");
    });
    expect(accepted).toBe(true);
    expect(
      await screen.findByRole("dialog", { name: RESOURCE_PICKER_TITLE }),
    ).toBeInTheDocument();
    expect(mcp.listPrompts).not.toHaveBeenCalled();
  });
});
