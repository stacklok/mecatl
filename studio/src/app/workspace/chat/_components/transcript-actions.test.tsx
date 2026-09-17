import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { AgentMessage } from "@/features/agent";
import {
  CONVERSATION_COPIED_MESSAGE,
  COPY_CONVERSATION_LABEL,
  CopyTranscriptMenuItem,
  copyTranscript,
  NOTHING_TO_COPY_MESSAGE,
  SELECT_CONVERSATION_LABEL,
  TranscriptMenuItems,
  TranscriptSheetItems,
} from "./transcript-actions";
import { serializeTranscript } from "./transcript-text";

/**
 * The ··· menus' whole-conversation actions: "Select conversation" hands
 * off to the caller's DOM select, "Copy conversation" puts the serialized
 * transcript on the clipboard and toasts the outcome — naming the
 * insecure-page cause when the Clipboard API is missing — and both are
 * disabled on an empty conversation. The mobile sheet rows do the same and
 * close their sheet.
 */

const messages: AgentMessage[] = [
  { id: "u1", role: "user", content: "What is 2+2?", timestamp: 1 },
  { id: "a1", role: "assistant", content: "4.", timestamp: 2 },
];

function stubClipboard(writeText = vi.fn(async (_text: string) => {})) {
  // A plain DOM click: user-event's setup() would install its own clipboard
  // stub over navigator.clipboard and swallow this spy.
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  });
  return writeText;
}

function renderMenu(items: React.ReactNode) {
  return render(
    <DropdownMenu defaultOpen>
      <DropdownMenuTrigger>Options</DropdownMenuTrigger>
      <DropdownMenuContent>{items}</DropdownMenuContent>
    </DropdownMenu>,
  );
}

afterEach(() => {
  delete (navigator as { clipboard?: unknown }).clipboard;
});

describe("TranscriptMenuItems", () => {
  it("selects the conversation through the caller's handler", async () => {
    const onSelectTranscript = vi.fn();
    renderMenu(
      <TranscriptMenuItems
        messages={messages}
        botName="Mecatl"
        onSelectTranscript={onSelectTranscript}
      />,
    );
    fireEvent.click(
      await screen.findByRole("menuitem", { name: SELECT_CONVERSATION_LABEL }),
    );
    expect(onSelectTranscript).toHaveBeenCalledTimes(1);
  });

  it("copies the serialized transcript and toasts success", async () => {
    const writeText = stubClipboard();
    renderMenu(
      <TranscriptMenuItems
        messages={messages}
        botName="Mecatl"
        onSelectTranscript={() => {}}
      />,
    );
    fireEvent.click(
      await screen.findByRole("menuitem", { name: COPY_CONVERSATION_LABEL }),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(CONVERSATION_COPIED_MESSAGE),
    );
    expect(writeText).toHaveBeenCalledWith(
      serializeTranscript(messages, { botName: "Mecatl" }),
    );
    expect(writeText.mock.calls[0][0]).toBe(
      "**You**\n\nWhat is 2+2?\n\n**Mecatl**\n\n4.",
    );
  });

  it("explains a missing Clipboard API as the insecure-page cause", async () => {
    delete (navigator as { clipboard?: unknown }).clipboard;
    renderMenu(
      <TranscriptMenuItems
        messages={messages}
        botName="Mecatl"
        onSelectTranscript={() => {}}
      />,
    );
    fireEvent.click(
      await screen.findByRole("menuitem", { name: COPY_CONVERSATION_LABEL }),
    );
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    expect(vi.mocked(toast.error).mock.calls[0][0]).toContain(
      "https or localhost",
    );
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("disables both actions on an empty conversation", async () => {
    renderMenu(
      <TranscriptMenuItems
        messages={[]}
        botName="Mecatl"
        onSelectTranscript={() => {}}
      />,
    );
    expect(
      await screen.findByRole("menuitem", { name: SELECT_CONVERSATION_LABEL }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(
      screen.getByRole("menuitem", { name: COPY_CONVERSATION_LABEL }),
    ).toHaveAttribute("aria-disabled", "true");
  });
});

describe("CopyTranscriptMenuItem", () => {
  it("takes its own label and success message (the thread panel's Copy thread)", async () => {
    const writeText = stubClipboard();
    renderMenu(
      <CopyTranscriptMenuItem
        messages={messages}
        botName="Mecatl"
        label="Copy thread"
        successMessage="Thread copied"
      />,
    );
    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Copy thread" }),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Thread copied"),
    );
    expect(writeText).toHaveBeenCalledTimes(1);
  });
});

describe("copyTranscript", () => {
  it("says there is nothing to copy instead of copying an empty string", async () => {
    const writeText = stubClipboard();
    await copyTranscript(
      [{ id: "t", role: "tool", content: "tool output", timestamp: 1 }],
      "Mecatl",
    );
    expect(writeText).not.toHaveBeenCalled();
    expect(toast.info).toHaveBeenCalledWith(NOTHING_TO_COPY_MESSAGE);
    expect(toast.success).not.toHaveBeenCalled();
  });
});

describe("TranscriptSheetItems", () => {
  it("selects, copies, and closes the sheet after each action", async () => {
    const writeText = stubClipboard();
    const onSelectTranscript = vi.fn();
    const onDone = vi.fn();
    render(
      <TranscriptSheetItems
        messages={messages}
        botName="Mecatl"
        onSelectTranscript={onSelectTranscript}
        onDone={onDone}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: SELECT_CONVERSATION_LABEL }),
    );
    expect(onSelectTranscript).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledTimes(1);

    fireEvent.click(
      screen.getByRole("button", { name: COPY_CONVERSATION_LABEL }),
    );
    expect(onDone).toHaveBeenCalledTimes(2);
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(CONVERSATION_COPIED_MESSAGE),
    );
    expect(writeText).toHaveBeenCalledWith(
      serializeTranscript(messages, { botName: "Mecatl" }),
    );
  });

  it("disables both rows on an empty conversation", () => {
    render(
      <TranscriptSheetItems
        messages={[]}
        botName="Mecatl"
        onSelectTranscript={() => {}}
      />,
    );
    expect(
      screen.getByRole("button", { name: SELECT_CONVERSATION_LABEL }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: COPY_CONVERSATION_LABEL }),
    ).toBeDisabled();
  });
});
