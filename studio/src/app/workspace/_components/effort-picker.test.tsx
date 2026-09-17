import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  EFFORT_SWITCH_NOTE,
  EffortMenuItems,
  EffortSheetSection,
  effortTriggerLabel,
  NO_REASONING_WARNING,
} from "./effort-picker";

/**
 * The composer's Effort list: the daemon's neutral tiers with auto first,
 * the checkmark on the tier in force (the draft's pick or the live
 * session's resolved effort), picks reported as WIRE values ("" for auto),
 * the live-chat fork note, and the TUI's no-reasoning warning.
 */

const TIER_LABELS = ["Auto", "Low", "Medium", "High", "Extra high", "Max"];

function renderMenu(ui: React.ReactNode) {
  return render(
    <DropdownMenu defaultOpen>
      <DropdownMenuTrigger>Model</DropdownMenuTrigger>
      <DropdownMenuContent>{ui}</DropdownMenuContent>
    </DropdownMenu>,
  );
}

describe("EffortMenuItems (desktop submenu)", () => {
  it("lists auto and the five daemon tiers in rank order", async () => {
    renderMenu(<EffortMenuItems value="" onPick={() => {}} />);
    const rows = await screen.findAllByRole("menuitemradio");
    expect(rows.map((r) => r.textContent)).toEqual(TIER_LABELS);
  });

  it("checkmarks the tier in force from the wire value", async () => {
    renderMenu(<EffortMenuItems value="xhigh" onPick={() => {}} />);
    expect(
      await screen.findByRole("menuitemradio", { name: "Extra high" }),
    ).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("menuitemradio", { name: "Auto" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("reads the empty wire value as auto", async () => {
    renderMenu(<EffortMenuItems value="" onPick={() => {}} />);
    expect(
      await screen.findByRole("menuitemradio", { name: "Auto" }),
    ).toHaveAttribute("aria-checked", "true");
  });

  it("reports picks as wire values: a tier verbatim, auto as the empty string", async () => {
    const user = userEvent.setup();
    const onPick = vi.fn();
    renderMenu(<EffortMenuItems value="" onPick={onPick} />);
    await user.click(
      await screen.findByRole("menuitemradio", { name: "High" }),
    );
    expect(onPick).toHaveBeenLastCalledWith("high");
  });

  it("reports auto as the empty wire value", async () => {
    const user = userEvent.setup();
    const onPick = vi.fn();
    renderMenu(<EffortMenuItems value="medium" onPick={onPick} />);
    await user.click(
      await screen.findByRole("menuitemradio", { name: "Auto" }),
    );
    expect(onPick).toHaveBeenLastCalledWith("");
  });

  it("says a live pick continues the chat in a copy, and stays quiet in a draft", async () => {
    const { unmount } = renderMenu(
      <EffortMenuItems value="" onPick={() => {}} live />,
    );
    expect(await screen.findByText(EFFORT_SWITCH_NOTE)).toBeInTheDocument();
    unmount();
    renderMenu(<EffortMenuItems value="" onPick={() => {}} />);
    await screen.findAllByRole("menuitemradio");
    expect(screen.queryByText(EFFORT_SWITCH_NOTE)).toBeNull();
  });

  it("warns only when the model reports no reasoning support", async () => {
    const { unmount } = renderMenu(
      <EffortMenuItems value="" onPick={() => {}} modelReasoning={false} />,
    );
    expect(await screen.findByRole("note")).toHaveTextContent(
      NO_REASONING_WARNING,
    );
    unmount();
    // Unknown (auto-routed / unlisted model): no warning on a guess.
    renderMenu(<EffortMenuItems value="" onPick={() => {}} />);
    await screen.findAllByRole("menuitemradio");
    expect(screen.queryByRole("note")).toBeNull();
    unmount();
    renderMenu(
      <EffortMenuItems value="" onPick={() => {}} modelReasoning={true} />,
    );
    await screen.findAllByRole("menuitemradio");
    expect(screen.queryByRole("note")).toBeNull();
  });
});

describe("EffortSheetSection (mobile sheet)", () => {
  it("renders the same tiers as a labelled group of toggle rows with the current one pressed", () => {
    render(<EffortSheetSection value="max" onPick={() => {}} />);
    const group = screen.getByRole("group", { name: "Effort" });
    const rows = screen.getAllByRole("button");
    expect(group).toContainElement(rows[0]);
    expect(rows.map((r) => r.textContent)).toEqual(TIER_LABELS);
    expect(screen.getByRole("button", { name: "Max" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "Auto" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("reports the wire value of a tapped row and shows the live note and warning", async () => {
    const user = userEvent.setup();
    const onPick = vi.fn();
    render(
      <EffortSheetSection
        value=""
        onPick={onPick}
        live
        modelReasoning={false}
      />,
    );
    expect(screen.getByText(EFFORT_SWITCH_NOTE)).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent(NO_REASONING_WARNING);
    await user.click(screen.getByRole("button", { name: "Low" }));
    expect(onPick).toHaveBeenCalledWith("low");
  });
});

describe("effortTriggerLabel", () => {
  it("reads {model} · {effort} with auto for the empty wire value", () => {
    expect(effortTriggerLabel("fixture-model", "medium")).toBe(
      "fixture-model · Medium",
    );
    expect(effortTriggerLabel("Default model", "")).toBe(
      "Default model · Auto",
    );
  });
});
