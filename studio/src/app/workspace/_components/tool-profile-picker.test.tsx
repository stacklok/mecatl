import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SHELL_DISABLED_NOTE } from "@/lib/tool-profile";
import {
  ToolProfileSelector,
  ToolProfileSheetRows,
  toolProfileReadOnlyLine,
} from "./tool-profile-picker";

/**
 * The composer's TOOLS choice (the daemon's per-session `profile`, ADR
 * 0291) as its own pill next to Mode:
 *
 * - a DRAFT (`onProfileChange` wired) gets a dropdown with the two rows —
 *   All tools / No filesystem — and the pill names the pick;
 * - a LIVE chat gets no dropdown: a KNOWN profile renders a read-only pill,
 *   an unknown one (a chat Studio did not mint) renders nothing;
 * - a daemon whose Shell tool is off (`capabilities.bash === false`) gets
 *   the muted note; an older daemon that does not report it gets none.
 */

const runtime = vi.hoisted(() => ({
  value: null as null | { serverCapabilities: Record<string, unknown> },
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useOptionalRuntimeStatus: () => runtime.value,
}));

const pill = () => screen.getByTestId("tool-profile-pill");

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(pill());
  await screen.findByRole("menuitem", { name: /^All tools/ });
}

beforeEach(() => {
  runtime.value = null;
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("ToolProfileSelector (draft)", () => {
  it("lists All tools and No filesystem, checks the current pick, and reports a change", async () => {
    const user = userEvent.setup();
    const onProfileChange = vi.fn();
    render(
      <ToolProfileSelector profile="" onProfileChange={onProfileChange} />,
    );
    expect(pill()).toHaveTextContent("Tools: All tools");
    await openMenu(user);
    const noFs = screen.getByRole("menuitem", { name: /^No filesystem/ });
    expect(noFs).toBeInTheDocument();
    fireEvent.click(noFs);
    expect(onProfileChange).toHaveBeenCalledWith("no-fs");
  });

  it("names the attenuated profile on the pill once picked", () => {
    render(<ToolProfileSelector profile="no-fs" onProfileChange={vi.fn()} />);
    expect(pill()).toHaveTextContent("Tools: No filesystem");
    expect(pill()).toHaveAttribute("title", "Tools: No filesystem");
  });

  it("says when the daemon's Shell tool is off, and stays quiet when it is not reported", async () => {
    const user = userEvent.setup();
    runtime.value = { serverCapabilities: { bash: false } };
    const view = render(
      <ToolProfileSelector profile="" onProfileChange={vi.fn()} />,
    );
    await openMenu(user);
    expect(screen.getByText(SHELL_DISABLED_NOTE)).toBeInTheDocument();
    view.unmount();

    runtime.value = { serverCapabilities: {} };
    render(<ToolProfileSelector profile="" onProfileChange={vi.fn()} />);
    await openMenu(user);
    expect(screen.queryByText(SHELL_DISABLED_NOTE)).toBeNull();
  });
});

describe("ToolProfileSelector (live chat)", () => {
  it("renders a known profile as a read-only pill", () => {
    render(<ToolProfileSelector profile="no-fs" />);
    expect(pill()).toHaveTextContent("Tools: No filesystem");
    expect(pill()).toHaveAttribute("title", toolProfileReadOnlyLine("no-fs"));
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("renders nothing at all for a chat whose profile is unknown", () => {
    const { container } = render(<ToolProfileSelector />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("ToolProfileSheetRows (mobile)", () => {
  it("offers the two rows on a draft and reports the pick", async () => {
    const user = userEvent.setup();
    const onProfileChange = vi.fn();
    render(
      <ToolProfileSheetRows profile="" onProfileChange={onProfileChange} />,
    );
    expect(screen.getByText("Tools")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^No filesystem/ }));
    expect(onProfileChange).toHaveBeenCalledWith("no-fs");
    await user.click(screen.getByRole("button", { name: /^All tools/ }));
    expect(onProfileChange).toHaveBeenCalledWith("");
  });

  it("is read-only on a live chat with a known profile and absent when unknown", () => {
    const known = render(<ToolProfileSheetRows profile="" />);
    expect(screen.getByText(toolProfileReadOnlyLine(""))).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
    known.unmount();

    const { container } = render(<ToolProfileSheetRows />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the shell-off note under the rows", () => {
    runtime.value = { serverCapabilities: { bash: false } };
    render(<ToolProfileSheetRows profile="" onProfileChange={vi.fn()} />);
    expect(screen.getByText(SHELL_DISABLED_NOTE)).toBeInTheDocument();
  });
});
