import { render, screen } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { PERMISSION_MODES, type ScheduleSpecDraft } from "@/lib/protocol";
import {
  describePhraseOutcome,
  draftFromForm,
  emptyScheduleForm,
  formFromDraft,
  PHRASE_HINT,
  ScheduleFormFields,
  type ScheduleFormValue,
  toolProfileDescription,
  WRITE_ACCESS_OFF_NOTE,
  WRITE_ACCESS_ON_NOTE,
} from "./schedule-form";

/**
 * Pins the write-access opt-in (the TUI form's y/n mutating toggle): the
 * mode/mutating pairing the form emits per state, how a stored spec seeds
 * it, and the switch + permission-mode picker that let a user change it.
 */

const SWITCH_NAME = "Allow file and shell writes";

function makeDraft(overrides: Partial<ScheduleSpecDraft>): ScheduleSpecDraft {
  return {
    name: "nightly-digest",
    prompt: "Summarise the day",
    trigger: { kind: "cron", cron: "0 9 * * *", timezone: "" },
    profile: "",
    mode: PERMISSION_MODES.PERMISSION_MODE_PLAN,
    mutating: false,
    maxFires: 0,
    limits: { maxTurns: 0, maxToolCalls: 0, maxConsecutiveFailures: 0 },
    oneShotRetry: false,
    oneShotMaxRetries: 0,
    ...overrides,
  };
}

describe("draftFromForm write access", () => {
  it("defaults to plan mode with mutating off", () => {
    const draft = draftFromForm(emptyScheduleForm());
    expect(draft.mode).toBe(PERMISSION_MODES.PERMISSION_MODE_PLAN);
    expect(draft.mutating).toBe(false);
  });

  it("couples the opt-in with accept-edits mode", () => {
    const draft = draftFromForm({
      ...emptyScheduleForm(),
      allowWrites: true,
      writeMode: "accept_edits",
    });
    expect(draft.mode).toBe(PERMISSION_MODES.PERMISSION_MODE_ACCEPT_EDITS);
    expect(draft.mutating).toBe(true);
  });

  it("couples the opt-in with default mode when picked", () => {
    const draft = draftFromForm({
      ...emptyScheduleForm(),
      allowWrites: true,
      writeMode: "default",
    });
    expect(draft.mode).toBe(PERMISSION_MODES.PERMISSION_MODE_DEFAULT);
    expect(draft.mutating).toBe(true);
  });

  it("never emits the pairing the daemon rejects (non-mutating outside plan)", () => {
    // The writeMode is ignored while the opt-in is off: plan mode wins.
    const draft = draftFromForm({
      ...emptyScheduleForm(),
      allowWrites: false,
      writeMode: "default",
    });
    expect(draft.mode).toBe(PERMISSION_MODES.PERMISSION_MODE_PLAN);
    expect(draft.mutating).toBe(false);
  });
});

describe("formFromDraft write access", () => {
  it("seeds a mutating default-mode spec as writes on + default", () => {
    const value = formFromDraft(
      makeDraft({
        mutating: true,
        mode: PERMISSION_MODES.PERMISSION_MODE_DEFAULT,
      }),
    );
    expect(value.allowWrites).toBe(true);
    expect(value.writeMode).toBe("default");
  });

  it("seeds a mutating accept-edits spec as writes on + accept edits", () => {
    const value = formFromDraft(
      makeDraft({
        mutating: true,
        mode: PERMISSION_MODES.PERMISSION_MODE_ACCEPT_EDITS,
      }),
    );
    expect(value.allowWrites).toBe(true);
    expect(value.writeMode).toBe("accept_edits");
  });

  it("seeds a plan-mode read-only spec as writes off", () => {
    const value = formFromDraft(
      makeDraft({
        mutating: false,
        mode: PERMISSION_MODES.PERMISSION_MODE_PLAN,
      }),
    );
    expect(value.allowWrites).toBe(false);
  });

  it("round-trips a mutating spec through the form unchanged", () => {
    const stored = makeDraft({
      mutating: true,
      mode: PERMISSION_MODES.PERMISSION_MODE_DEFAULT,
    });
    const back = draftFromForm(formFromDraft(stored), stored);
    expect(back.mode).toBe(stored.mode);
    expect(back.mutating).toBe(stored.mutating);
  });
});

/**
 * The TOOL PROFILE (the spec's `profile`, ADR 0291 — the same field a chat's
 * create carries): the form owns it now instead of passing the stored value
 * through, so a user can attenuate a schedule's fire session to the
 * file-less catalog or lift that again; the fields the form still has no
 * control for (limits) keep riding `base`.
 */
describe("draftFromForm / formFromDraft tool profile", () => {
  it('defaults a new schedule to all tools (profile omitted on the wire as "")', () => {
    expect(emptyScheduleForm().profile).toBe("");
    expect(draftFromForm(emptyScheduleForm()).profile).toBe("");
  });

  it("emits the picked no-fs profile", () => {
    const draft = draftFromForm({ ...emptyScheduleForm(), profile: "no-fs" });
    expect(draft.profile).toBe("no-fs");
  });

  it("seeds the form from a stored no-fs spec and narrows an odd stored value", () => {
    expect(formFromDraft(makeDraft({ profile: "no-fs" })).profile).toBe(
      "no-fs",
    );
    expect(formFromDraft(makeDraft({ profile: "" })).profile).toBe("");
    // A value outside the closed set (a newer daemon's profile) reads as the
    // default rather than leaking an unknown string onto the wire.
    expect(
      formFromDraft(
        makeDraft({
          profile: "gpu" as unknown as ScheduleSpecDraft["profile"],
        }),
      ).profile,
    ).toBe("");
  });

  it("round-trips a no-fs spec while the carried limits still survive via base", () => {
    const stored = makeDraft({
      profile: "no-fs",
      limits: { maxTurns: 12, maxToolCalls: 40, maxConsecutiveFailures: 3 },
    });
    const back = draftFromForm(formFromDraft(stored), stored);
    expect(back.profile).toBe("no-fs");
    expect(back.limits).toEqual(stored.limits);
  });

  it("lets an edit lift the attenuation: the form value wins over the stored base", () => {
    const stored = makeDraft({ profile: "no-fs" });
    const value = { ...formFromDraft(stored), profile: "" as const };
    expect(draftFromForm(value, stored).profile).toBe("");
  });

  it("names the consequence of each profile", () => {
    expect(toolProfileDescription("")).toMatch(/Shell and the file tools/);
    expect(toolProfileDescription("no-fs")).toMatch(/Shell, Read, Edit, Write/);
    expect(toolProfileDescription("no-fs")).toMatch(/web tools remain/);
  });
});

/** Controlled wrapper: the real dialog folds patches into state the same way. */
function Harness({
  initial,
  onChange,
}: {
  initial?: Partial<ScheduleFormValue>;
  onChange?: (patch: Partial<ScheduleFormValue>) => void;
}) {
  const [value, setValue] = useState<ScheduleFormValue>(() => ({
    ...emptyScheduleForm(),
    ...initial,
  }));
  return (
    <ScheduleFormFields
      value={value}
      onChange={(patch) => {
        onChange?.(patch);
        setValue((v) => ({ ...v, ...patch }));
      }}
    />
  );
}

describe("ScheduleFormFields write access control", () => {
  it("renders the switch off by default with the read-only note and no mode picker", () => {
    render(<Harness />);
    expect(screen.getByRole("switch", { name: SWITCH_NAME })).not.toBeChecked();
    expect(screen.getByText(WRITE_ACCESS_OFF_NOTE)).toBeInTheDocument();
    expect(screen.queryByText(WRITE_ACCESS_ON_NOTE)).toBeNull();
    expect(
      screen.queryByRole("combobox", { name: "Permission mode" }),
    ).toBeNull();
  });

  it("turning the switch on patches allowWrites, swaps the note and reveals the mode picker", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    const writes = screen.getByRole("switch", { name: SWITCH_NAME });
    await user.click(writes);

    expect(onChange).toHaveBeenCalledWith({ allowWrites: true });
    expect(writes).toBeChecked();
    expect(screen.getByText(WRITE_ACCESS_ON_NOTE)).toBeInTheDocument();
    expect(screen.queryByText(WRITE_ACCESS_OFF_NOTE)).toBeNull();
    // Accept edits is the default write mode; the picker shows it by name.
    expect(
      screen.getByRole("combobox", { name: "Permission mode" }),
    ).toHaveTextContent("Accept edits");
  });

  it("picking Default in the mode picker patches writeMode", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={{ allowWrites: true }} onChange={onChange} />);

    await user.click(screen.getByRole("combobox", { name: "Permission mode" }));
    await user.click(
      await screen.findByRole("option", {
        name: "Default — standard permission rules apply",
      }),
    );

    expect(onChange).toHaveBeenCalledWith({ writeMode: "default" });
    expect(
      screen.getByRole("combobox", { name: "Permission mode" }),
    ).toHaveTextContent("Default");
  });

  it("turning the switch back off hides the mode picker and restores the read-only note", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={{ allowWrites: true }} onChange={onChange} />);

    const writes = screen.getByRole("switch", { name: SWITCH_NAME });
    expect(writes).toBeChecked();
    await user.click(writes);

    expect(onChange).toHaveBeenCalledWith({ allowWrites: false });
    expect(writes).not.toBeChecked();
    expect(
      screen.queryByRole("combobox", { name: "Permission mode" }),
    ).toBeNull();
    expect(screen.getByText(WRITE_ACCESS_OFF_NOTE)).toBeInTheDocument();
  });

  it("is keyboard operable: Space toggles the focused switch", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    const writes = screen.getByRole("switch", { name: SWITCH_NAME });
    writes.focus();
    expect(writes).toHaveFocus();
    await user.keyboard(" ");
    expect(writes).toBeChecked();
    await user.keyboard(" ");
    expect(writes).not.toBeChecked();
  });
});

/**
 * The natural-language trigger (the TUI Create form's phrase input): a phrase
 * compiles INTO the cron/one-shot value the wire carries, the structured
 * controls re-derive from it, and a phrase the table does not know leaves the
 * value alone and says so.
 */
/**
 * Open a Radix Select and choose an option. user-event leaves a typed input
 * focused; opening a Radix Select straight from that state makes its focus
 * hand-off land outside `act` in jsdom (it does for the Name field too), so
 * the helper first parks focus on the body — what a click on the dialog's
 * chrome does for a real user.
 */
async function pickOption(
  user: UserEvent,
  combobox: HTMLElement,
  option: string,
) {
  await user.click(document.body);
  await user.click(combobox);
  await user.click(await screen.findByRole("option", { name: option }));
}

describe("ScheduleFormFields phrase input", () => {
  const phraseBox = () =>
    screen.getByRole("textbox", { name: "Describe the schedule" });
  const repeatBox = () => screen.getByRole("combobox", { name: "Repeat" });

  it("compiles a recurring phrase into the cron and lands the builder on Every…", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await user.type(phraseBox(), "every 30 minutes");

    expect(onChange).toHaveBeenCalledWith({
      triggerKind: "cron",
      cron: "*/30 * * * *",
    });
    expect(repeatBox()).toHaveTextContent("Every…");
    expect(screen.getByRole("spinbutton", { name: "Every" })).toHaveValue(30);
    expect(screen.getByRole("combobox", { name: "Unit" })).toHaveTextContent(
      "Minutes",
    );
    // The interval shapes have no "At" time; the preview says what compiled.
    expect(screen.queryByLabelText("At")).toBeNull();
    expect(screen.getByText("Every 30 minutes")).toBeInTheDocument();
    expect(phraseBox()).toHaveAccessibleDescription("Every 30 minutes");
  });

  it("compiles a fixed-time phrase onto the matching builder shape", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await user.type(phraseBox(), "every weekday at 9:15pm");

    expect(onChange).toHaveBeenLastCalledWith({
      triggerKind: "cron",
      cron: "15 21 * * 1-5",
    });
    expect(repeatBox()).toHaveTextContent("Weekdays");
    expect(screen.getByLabelText("At")).toHaveValue("21:15");
    expect(screen.getByText("Weekdays at 9:15 PM")).toBeInTheDocument();
  });

  it("compiles a one-shot phrase and switches to Run once", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await user.type(phraseBox(), "tomorrow at 8am");

    const oneShot = onChange.mock.calls
      .map(([patch]) => patch as Partial<ScheduleFormValue>)
      .findLast((patch) => patch.triggerKind === "one-shot");
    expect(oneShot?.oneShotAt).toMatch(/^\d{4}-\d{2}-\d{2}T08:00$/);
    expect(screen.getByRole("tab", { name: "Run once" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByLabelText("Time")).toHaveValue("08:00");
    expect(screen.getByText(/^Once at /)).toBeInTheDocument();
  });

  it("treats unmatched five-field input as the raw cron, landing on Custom", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await user.type(phraseBox(), "0 9 * * 1,3,5");

    expect(onChange).toHaveBeenLastCalledWith({
      triggerKind: "cron",
      cron: "0 9 * * 1,3,5",
    });
    expect(repeatBox()).toHaveTextContent("Custom");
    expect(screen.getByLabelText("Cron expression")).toHaveValue(
      "0 9 * * 1,3,5",
    );
    expect(
      screen.getByText("Cron expression 0 9 * * 1,3,5"),
    ).toBeInTheDocument();
  });

  it("leaves the value alone and shows the hint for a phrase it does not know", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await user.type(phraseBox(), "gibberish");

    expect(onChange).not.toHaveBeenCalled();
    expect(repeatBox()).toHaveTextContent("Daily");
    expect(screen.getByText(PHRASE_HINT)).toBeInTheDocument();
    expect(phraseBox()).toHaveAccessibleDescription(PHRASE_HINT);
  });

  it("shows no hint while the phrase box is empty", () => {
    render(<Harness />);
    expect(screen.queryByText(PHRASE_HINT)).toBeNull();
    expect(phraseBox()).not.toHaveAttribute("aria-describedby");
  });

  it("clears the phrase and its preview once a builder control is edited", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(phraseBox(), "every 30 minutes");
    expect(screen.getByText("Every 30 minutes")).toBeInTheDocument();

    await pickOption(user, repeatBox(), "Daily");

    // The structured control now owns the trigger: no stale phrase remains
    // to contradict it.
    expect(phraseBox()).toHaveValue("");
    expect(screen.queryByText("Every 30 minutes")).toBeNull();
    expect(repeatBox()).toHaveTextContent("Daily");
  });

  it("clears the phrase when the trigger tab is switched", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(phraseBox(), "tomorrow at 8am");
    expect(phraseBox()).toHaveValue("tomorrow at 8am");

    await user.click(screen.getByRole("tab", { name: "Recurring" }));
    expect(phraseBox()).toHaveValue("");
    expect(screen.queryByText(/^Once at /)).toBeNull();
  });
});

describe("describePhraseOutcome", () => {
  it("reads each outcome in plain English", () => {
    expect(describePhraseOutcome({ kind: "cron", cron: "0 9 * * *" })).toBe(
      "Daily at 9:00 AM",
    );
    expect(
      describePhraseOutcome({ kind: "raw-cron", cron: "0 9 * * 1,3,5" }),
    ).toBe("Cron expression 0 9 * * 1,3,5");
    // A five-field fallback the describer CAN read gets the English.
    expect(
      describePhraseOutcome({ kind: "raw-cron", cron: "*/5 * * * *" }),
    ).toBe("Every 5 minutes");
    const at = new Date(2026, 8, 17, 8, 0).getTime();
    expect(describePhraseOutcome({ kind: "one-shot", at })).toBe(
      `Once at ${new Date(at).toLocaleString()}`,
    );
    expect(describePhraseOutcome({ kind: "none" })).toBeNull();
  });
});

describe("CronBuilderFields interval controls", () => {
  const repeatBox = () => screen.getByRole("combobox", { name: "Repeat" });

  it("seeds Every…/step/unit from an interval cron and hides the At time", () => {
    render(<Harness initial={{ cron: "0 */2 * * *" }} />);
    expect(repeatBox()).toHaveTextContent("Every…");
    expect(screen.getByRole("spinbutton", { name: "Every" })).toHaveValue(2);
    expect(screen.getByRole("combobox", { name: "Unit" })).toHaveTextContent(
      "Hours",
    );
    expect(screen.queryByLabelText("At")).toBeNull();
    expect(screen.queryByLabelText("Cron expression")).toBeNull();
  });

  it("picking Every… derives the default every-30-minutes cron", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await pickOption(user, repeatBox(), "Every…");

    expect(onChange).toHaveBeenCalledWith({ cron: "*/30 * * * *" });
    expect(screen.getByRole("spinbutton", { name: "Every" })).toHaveValue(30);
  });

  it("retyping the step and switching the unit re-derive the cron", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={{ cron: "*/15 * * * *" }} onChange={onChange} />);

    const every = screen.getByRole("spinbutton", { name: "Every" });
    await user.clear(every);
    await user.type(every, "5");
    expect(onChange).toHaveBeenLastCalledWith({ cron: "*/5 * * * *" });

    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Unit" }),
      "Hours",
    );
    expect(onChange).toHaveBeenLastCalledWith({ cron: "0 */5 * * *" });
    expect(every).toHaveAttribute("max", "23");
  });

  it("clamps an over-range step and snaps the field to it on blur", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={{ cron: "*/15 * * * *" }} onChange={onChange} />);

    const every = screen.getByRole("spinbutton", { name: "Every" });
    await user.clear(every);
    await user.type(every, "90");
    expect(onChange).toHaveBeenLastCalledWith({ cron: "*/59 * * * *" });
    // The draft shows what was typed until focus leaves.
    expect(every).toHaveValue(90);
    await user.tab();
    expect(every).toHaveValue(59);
  });

  it("a one-hour interval reads back as Every… 1 Hours", () => {
    render(<Harness initial={{ cron: "0 * * * *" }} />);
    expect(repeatBox()).toHaveTextContent("Every…");
    expect(screen.getByRole("spinbutton", { name: "Every" })).toHaveValue(1);
    expect(screen.getByRole("combobox", { name: "Unit" })).toHaveTextContent(
      "Hours",
    );
  });
});

describe("ScheduleFormFields tool profile control", () => {
  const profileBox = () =>
    screen.getByRole("combobox", { name: "Tool profile" });

  it("renders All tools by default with its consequence line", () => {
    render(<Harness />);
    expect(profileBox()).toHaveTextContent("All tools");
    expect(screen.getByText(toolProfileDescription(""))).toBeInTheDocument();
    expect(screen.queryByText(toolProfileDescription("no-fs"))).toBeNull();
  });

  it("picking No filesystem patches the profile and swaps the line", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await pickOption(user, profileBox(), "No filesystem");

    expect(onChange).toHaveBeenCalledWith({ profile: "no-fs" });
    expect(profileBox()).toHaveTextContent("No filesystem");
    expect(
      screen.getByText(toolProfileDescription("no-fs")),
    ).toBeInTheDocument();
  });

  it("seeds from a no-fs value and can be picked back to All tools", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={{ profile: "no-fs" }} onChange={onChange} />);
    expect(profileBox()).toHaveTextContent("No filesystem");

    await pickOption(user, profileBox(), "All tools");

    expect(onChange).toHaveBeenCalledWith({ profile: "" });
    expect(profileBox()).toHaveTextContent("All tools");
  });

  it("is independent of the write-access switch", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={{ profile: "no-fs" }} onChange={onChange} />);
    await user.click(screen.getByRole("switch", { name: SWITCH_NAME }));
    expect(onChange).toHaveBeenCalledWith({ allowWrites: true });
    expect(profileBox()).toHaveTextContent("No filesystem");
  });
});
