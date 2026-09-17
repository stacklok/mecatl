import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  AttachmentPill,
  agentMenuItems,
  commandMenuItems,
  commandNoMatchHint,
  commandSourceTag,
  ModelEffortSelector,
  resolveComposerAction,
  resolveComposerSubmission,
} from "./chat-input";
import { EFFORT_SWITCH_NOTE, NO_REASONING_WARNING } from "./effort-picker";

// The composer's `/` menu reads the daemon's command list through this
// module-level getter; the local-command tests swap it for a fixed roster
// and keep every other export (the Studio builtins) real.
const daemon = vi.hoisted(() => ({
  commands: [] as { name: string; description: string }[],
  agents: [] as { handle: string; name: string; description: string }[],
}));
vi.mock("@/features/agent/composer-capabilities", async (importOriginal) => ({
  ...(await importOriginal<
    typeof import("@/features/agent/composer-capabilities")
  >()),
  getSlashCommands: () => daemon.commands,
  getAgentMentions: () => daemon.agents,
}));

/**
 * Studio's built-ins (`/clear /help /session /retry /diagnostics /compact`)
 * are intercepted on send: a bare one runs locally instead of reaching the
 * agent, a held one (arguments, a second line) or a gated-off one stays in
 * the editor with a warning, and the `/` menu lists them ahead of the
 * daemon's commands. Both halves are pure and tested here — interception
 * through `resolveComposerSubmission`, the helper performAction runs BEFORE
 * its steer/queue/send branches, so a built-in typed mid-stream is
 * intercepted identically (never queued or steered).
 */
describe("resolveComposerSubmission", () => {
  const OPEN = { manualCompaction: true };

  it("runs a bare built-in, case-insensitively, with trailing whitespace", () => {
    for (const text of ["/help", "/HELP ", "/Help\n", "/clear"]) {
      expect(
        resolveComposerSubmission({ text, gates: OPEN, hasHandler: true }),
      ).toEqual({
        action: "builtin",
        name: text.trim().toLowerCase().slice(1),
      });
    }
  });

  it("holds a built-in with arguments, even while streaming", () => {
    expect(
      resolveComposerSubmission({
        text: "/clear now",
        gates: OPEN,
        hasHandler: true,
        isStreaming: true,
      }),
    ).toEqual({
      action: "hold",
      warning: "/clear takes no arguments — remove the text to run it",
    });
  });

  it("holds a gated-off built-in with the daemon warning", () => {
    expect(
      resolveComposerSubmission({
        text: "/compact",
        gates: { manualCompaction: false },
        hasHandler: true,
      }),
    ).toEqual({
      action: "hold",
      warning: "/compact is not available on this daemon",
    });
    // No gates at all fails closed the same way.
    expect(
      resolveComposerSubmission({ text: "/compact", hasHandler: true }),
    ).toMatchObject({ action: "hold" });
  });

  it("passes everything else to the agent", () => {
    for (const text of ["/helpme", "help", "", "/review", "/deploy prod"]) {
      expect(
        resolveComposerSubmission({ text, gates: OPEN, hasHandler: true }),
      ).toEqual({ action: "pass" });
    }
  });

  it("passes even a bare built-in where no handler is wired", () => {
    expect(
      resolveComposerSubmission({
        text: "/help",
        gates: OPEN,
        hasHandler: false,
      }),
    ).toEqual({ action: "pass" });
  });
});

/**
 * The `@` menu (TUI file-mention parity): the file rows lead — "Attach a
 * file…" while the query could still spell "file", "Mention path" for a
 * path-shaped token — ahead of the agent roster; an agent query shows
 * neither. The rows are plain menu items (no new mention kind), recognised
 * by their sentinel ids in the composer's select handler.
 */
describe("agentMenuItems", () => {
  beforeEach(() => {
    daemon.agents = [
      { handle: "reviewer", name: "Reviewer", description: "reviews diffs" },
      { handle: "planner", name: "Planner", description: "plans work" },
    ];
  });
  afterEach(() => {
    daemon.agents = [];
  });

  it("starts with the Attach-a-file row on an empty query, then every agent", () => {
    const items = agentMenuItems("");
    expect(items[0]).toMatchObject({
      id: "__attach-file",
      primary: "Attach a file…",
    });
    expect(items.slice(1).map((item) => item.id)).toEqual([
      "reviewer",
      "planner",
    ]);
  });

  it("offers the Mention-path row for a path-shaped query", () => {
    const items = agentMenuItems("src/");
    expect(items.map((item) => item.primary)).toEqual(["Mention path @src/"]);
    expect(items[0]?.label).toBe("@src/");
  });

  it("shows neither file row for an agent query, filtering by handle prefix or name", () => {
    const items = agentMenuItems("rev");
    expect(items.map((item) => item.id)).toEqual(["reviewer"]);
    expect(agentMenuItems("PLAN").map((item) => item.id)).toEqual(["planner"]);
  });
});

describe("commandMenuItems", () => {
  const STUDIO_HELP = "show keys & features";
  const OPEN = { manualCompaction: true };

  beforeEach(() => {
    daemon.commands = [{ name: "review", description: "Review a diff" }];
  });

  it("lists the Studio builtins first, in fixed order, then the daemon's commands", () => {
    const items = commandMenuItems("", { gates: OPEN });
    // No capability document: only the always-present built-ins, then
    // the daemon's list.
    expect(items.map((i) => i.id)).toEqual([
      "clear",
      "help",
      "session",
      "retry",
      "diagnostics",
      "compact",
      "title",
      "learning",
      "review",
    ]);
    expect(items[1]?.primary).toBe("/help");
    expect(items[1]?.secondary).toBe(STUDIO_HELP);
    // Builtin rows carry the mark the palette renders as a terminal glyph
    // and the "built-in" source tag; daemon rows read "workspace".
    expect(items.slice(0, 8).every((i) => i.builtin === true)).toBe(true);
    expect(items[8]?.builtin).toBeUndefined();
    expect(items.slice(0, 8).map(commandSourceTag)).toEqual(
      Array(8).fill("built-in"),
    );
    expect(commandSourceTag(items[8] as (typeof items)[number])).toBe(
      "workspace",
    );
  });

  it("adds the capability-gated built-ins the daemon enables, ahead of the daemon's commands", () => {
    const items = commandMenuItems("", {
      gates: {
        ...OPEN,
        capabilities: { mcp: true, agents: true, posture: "auto" },
      },
    });
    const ids = items.map((i) => i.id);
    expect(ids).toEqual([
      "clear",
      "help",
      "session",
      "retry",
      "diagnostics",
      "compact",
      "title",
      "mcp",
      "prompts",
      "resources",
      "agents",
      // /models and /effort ride the picker's gate (absent = shown).
      "models",
      "effort",
      "posture",
      "learning",
      "review",
    ]);
    expect(ids).not.toContain("skills");
    expect(items.find((i) => i.id === "mcp")?.secondary).toMatch(/MCP tools/);
  });

  it("names the typed token in the no-match hint, and only for the `/` menu", () => {
    expect(
      commandNoMatchHint({ kind: "command", items: [], query: "zzz" }),
    ).toBe("No command matches /zzz — Enter sends it as text");
    // Rows to pick, an empty query (the bare `/`) or the `@` menu: no hint.
    expect(
      commandNoMatchHint({ kind: "command", items: [{}], query: "zzz" }),
    ).toBeNull();
    expect(
      commandNoMatchHint({ kind: "command", items: [], query: "" }),
    ).toBeNull();
    expect(
      commandNoMatchHint({ kind: "agent", items: [], query: "zzz" }),
    ).toBeNull();
    // The hint is what the palette renders when the prefix matches nothing.
    expect(commandMenuItems("zzz", { gates: OPEN })).toEqual([]);
  });

  it("hides /compact unless the daemon enables manual compaction", () => {
    expect(
      commandMenuItems("", { gates: { manualCompaction: false } }).map(
        (i) => i.id,
      ),
    ).not.toContain("compact");
    // No gates at all fails closed.
    expect(commandMenuItems("").map((i) => i.id)).not.toContain("compact");
  });

  it("filters both layers by prefix", () => {
    expect(commandMenuItems("he", { gates: OPEN }).map((i) => i.id)).toEqual([
      "help",
    ]);
    expect(commandMenuItems("re", { gates: OPEN }).map((i) => i.id)).toEqual([
      "retry",
      "review",
    ]);
  });

  it("shadows a daemon command named like a builtin — the Studio row wins", () => {
    daemon.commands = [
      { name: "help", description: "daemon help" },
      { name: "review", description: "Review a diff" },
    ];
    const items = commandMenuItems("", { gates: OPEN });
    expect(items.filter((i) => i.id === "help")).toHaveLength(1);
    expect(items.find((i) => i.id === "help")?.secondary).toBe(STUDIO_HELP);
    expect(items.at(-1)?.id).toBe("review");
  });

  it("omits the builtins for a surface with no local-command handler", () => {
    daemon.commands = [{ name: "help", description: "daemon help" }];
    const items = commandMenuItems("", { builtins: false, gates: OPEN });
    expect(items.map((i) => i.id)).toEqual(["help"]);
    expect(items[0]?.secondary).toBe("daemon help");
    expect(items[0]?.builtin).toBeUndefined();
  });
});

/**
 * The Enter matrix is tested through `resolveComposerAction`, the pure
 * decision function the composer's keydown handler and send button both call.
 * Driving the TipTap editor's ProseMirror view with synthetic keydowns in
 * jsdom is impractical (the editor mounts asynchronously and owns its own
 * capture-phase handlers), so the decision table is extracted and tested
 * exhaustively instead; "newline" means the key is NOT intercepted — the
 * editor's own hardBreak inserts the newline and onSend is never called.
 */
describe("resolveComposerAction", () => {
  const resolve = (
    shift: boolean,
    isStreaming: boolean,
    behavior: "queue" | "steer",
  ) => resolveComposerAction({ shift, isStreaming, behavior });

  it("sends on idle Enter, whatever the preference", () => {
    expect(resolve(false, false, "queue")).toBe("send");
    expect(resolve(false, false, "steer")).toBe("send");
  });

  it("keeps idle Shift+Enter as a newline (the key is not intercepted, so onSend is never called)", () => {
    expect(resolve(true, false, "queue")).toBe("newline");
    expect(resolve(true, false, "steer")).toBe("newline");
  });

  it("queues on streaming Enter with the default preference", () => {
    expect(resolve(false, true, "queue")).toBe("queue");
  });

  it("steers on streaming Shift+Enter with the default preference", () => {
    expect(resolve(true, true, "queue")).toBe("steer");
  });

  it("inverts both keys when the preference is steer", () => {
    expect(resolve(false, true, "steer")).toBe("steer");
    expect(resolve(true, true, "steer")).toBe("queue");
  });

  // ⌘Enter / Ctrl+Enter (the TUI's ctrl+j): the unconditional newline. It
  // wins over every other rule — idle or streaming, either preference, shift
  // held or not — so a multi-line message can be composed mid-run, where
  // Shift+Enter is repurposed. The keydown handler leaves the chord to the
  // editor's Mod-Enter hardBreak, so "newline" here means never intercepted.
  it("keeps mod+Enter a newline in every cell of the matrix", () => {
    for (const isStreaming of [false, true]) {
      for (const behavior of ["queue", "steer"] as const) {
        for (const shift of [false, true]) {
          expect(
            resolveComposerAction({ shift, mod: true, isStreaming, behavior }),
          ).toBe("newline");
        }
      }
    }
    // An absent `mod` is the legacy call: the rest of the table is untouched.
    expect(
      resolveComposerAction({
        shift: false,
        isStreaming: true,
        behavior: "queue",
      }),
    ).toBe("queue");
  });

  // Attachments no longer force the queue path: a steer carries staged image
  // parts (ADR 0251). Availability is the handler's business — performAction
  // degrades steer→queue when onSteer is absent, keeping the files attached.

  // "Queue only" is the client-level never-steer switch (mecatui --no-steer):
  // both keys queue while streaming — Shift+Enter has no opposite to invert
  // into — and the idle cells are the ordinary send / newline.
  it("queues on BOTH streaming Enter and Shift+Enter under queue-only", () => {
    expect(
      resolveComposerAction({
        shift: false,
        isStreaming: true,
        behavior: "queue-only",
      }),
    ).toBe("queue");
    expect(
      resolveComposerAction({
        shift: true,
        isStreaming: true,
        behavior: "queue-only",
      }),
    ).toBe("queue");
  });

  it("leaves the idle and mod+Enter cells alone under queue-only", () => {
    expect(
      resolveComposerAction({
        shift: false,
        isStreaming: false,
        behavior: "queue-only",
      }),
    ).toBe("send");
    expect(
      resolveComposerAction({
        shift: true,
        isStreaming: false,
        behavior: "queue-only",
      }),
    ).toBe("newline");
    expect(
      resolveComposerAction({
        shift: false,
        mod: true,
        isStreaming: true,
        behavior: "queue-only",
      }),
    ).toBe("newline");
  });
});

describe("AttachmentPill", () => {
  // jsdom has no object-URL implementation; stub the pair the pill uses.
  const createObjectURL = vi.fn(() => "blob:thumb");
  const revokeObjectURL = vi.fn();

  beforeEach(() => {
    createObjectURL.mockClear();
    revokeObjectURL.mockClear();
    vi.stubGlobal("URL", {
      ...URL,
      createObjectURL,
      revokeObjectURL,
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows a thumbnail for an image file and revokes its object URL on unmount", async () => {
    const file = new File(["x"], "photo.png", { type: "image/png" });
    const { container, unmount } = render(
      <AttachmentPill file={file} onRemove={() => {}} />,
    );
    await waitFor(() =>
      expect(container.querySelector("img")?.getAttribute("src")).toBe(
        "blob:thumb",
      ),
    );
    unmount();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:thumb");
  });

  it("shows a file-kind glyph, not a thumbnail, for a non-image file", () => {
    const file = new File(["x"], "report.pdf", { type: "application/pdf" });
    const { container } = render(
      <AttachmentPill file={file} onRemove={() => {}} />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(container.querySelector('[aria-label="PDF"]')).toBeTruthy();
    expect(container.textContent).toContain("report.pdf");
  });
});

/**
 * The combined model + effort picker: the Effort submenu is real in BOTH
 * modes — a draft's pick is reported for the create body, a live chat's pick
 * forks (like a model switch) and the checkmark follows the daemon's
 * EFFECTIVE tier — and it hides with the model list when the daemon's
 * model_selection capability is off.
 */
describe("ModelEffortSelector", () => {
  const models = [
    { id: "m1", label: "Model One", providerId: "p", reasoning: false },
    { id: "m2", label: "Model Two", providerId: "p", reasoning: true },
  ];

  /** The trigger is the only button before the menu opens. */
  const trigger = () => screen.getByRole("button");

  /** Opens the root menu (a real pointer on the trigger), then the Effort
   *  submenu. The sub is opened and its rows picked with plain clicks: jsdom
   *  reports zero-size rects, so a simulated pointer MOVE onto a sub row
   *  reads to Radix as leaving the submenu and closes it. */
  async function openEffort(user: ReturnType<typeof userEvent.setup>) {
    await user.click(trigger());
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Effort/ }));
    return screen.findAllByRole("menuitemradio");
  }

  const pick = (name: string) =>
    fireEvent.click(screen.getByRole("menuitemradio", { name }));

  it("live chat: labels the trigger {model} · {effort} from the resolved tier and offers the Effort submenu", async () => {
    const user = userEvent.setup();
    const onSwitchEffort = vi.fn();
    render(
      <ModelEffortSelector
        models={models}
        onSwitchModel={() => {}}
        onSwitchEffort={onSwitchEffort}
        currentModelId="m2"
        currentEffort="medium"
      />,
    );
    expect(trigger()).toHaveAttribute("title", "Model Two · Medium");
    const rows = await openEffort(user);
    expect(rows.map((r) => r.textContent)).toEqual([
      "Auto",
      "Low",
      "Medium",
      "High",
      "Extra high",
      "Max",
    ]);
    expect(
      screen.getByRole("menuitemradio", { name: "Medium" }),
    ).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText(EFFORT_SWITCH_NOTE)).toBeInTheDocument();
    // A reasoning model: no warning.
    expect(screen.queryByRole("note")).toBeNull();
    // Picking a different tier forks with the WIRE value.
    pick("High");
    expect(onSwitchEffort).toHaveBeenCalledWith("high");
  });

  it("live chat: re-picking the current tier is a no-op, auto forks with the empty value", async () => {
    const user = userEvent.setup();
    const onSwitchEffort = vi.fn();
    render(
      <ModelEffortSelector
        models={models}
        onSwitchModel={() => {}}
        onSwitchEffort={onSwitchEffort}
        currentModelId="m2"
        currentEffort="medium"
      />,
    );
    await openEffort(user);
    pick("Medium");
    expect(onSwitchEffort).not.toHaveBeenCalled();
    // Radix closes the menu on select, so the trigger is reachable again.
    await openEffort(user);
    pick("Auto");
    expect(onSwitchEffort).toHaveBeenCalledWith("");
  });

  it("live chat: warns when the session's model reports no reasoning support", async () => {
    const user = userEvent.setup();
    render(
      <ModelEffortSelector
        models={models}
        onSwitchModel={() => {}}
        onSwitchEffort={() => {}}
        currentModelId="m1"
        currentEffort=""
      />,
    );
    // No tier echoed = auto.
    expect(trigger()).toHaveAttribute("title", "Model One · Auto");
    await openEffort(user);
    expect(screen.getByRole("note")).toHaveTextContent(NO_REASONING_WARNING);
  });

  it("live chat: the caller's reasoning flag wins over the option lookup", async () => {
    const user = userEvent.setup();
    render(
      <ModelEffortSelector
        models={models}
        onSwitchModel={() => {}}
        onSwitchEffort={() => {}}
        currentModelId="m2"
        currentEffort="low"
        currentModelReasoning={false}
      />,
    );
    await openEffort(user);
    expect(screen.getByRole("note")).toHaveTextContent(NO_REASONING_WARNING);
  });

  it("draft: reports the pending pick as a wire value, relabels the trigger, and reset returns to untouched (null)", async () => {
    const user = userEvent.setup();
    const onEffortChange = vi.fn();
    const onModelChange = vi.fn();
    render(
      <ModelEffortSelector
        models={models}
        onEffortChange={onEffortChange}
        onModelChange={onModelChange}
      />,
    );
    expect(trigger()).toHaveAttribute("title", "Default model · Auto");
    expect(screen.queryByText(EFFORT_SWITCH_NOTE)).toBeNull();
    await openEffort(user);
    expect(screen.queryByText(EFFORT_SWITCH_NOTE)).toBeNull();
    pick("Extra high");
    expect(onEffortChange).toHaveBeenCalledWith("xhigh");
    // Radix closes the menu on select; the trigger reflects the pick.
    await waitFor(() =>
      expect(trigger()).toHaveAttribute("title", "Default model · Extra high"),
    );
    await user.click(trigger());
    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Reset to default" }),
    );
    expect(onEffortChange).toHaveBeenLastCalledWith("");
    // Tri-state draft pick: Reset reports null (untouched), not "" (explicit auto).
    expect(onModelChange).toHaveBeenCalledWith(null);
    await waitFor(() =>
      expect(trigger()).toHaveAttribute("title", "Default model · Auto"),
    );
  });

  it("draft: a controlled effort value drives the checkmark and the label", async () => {
    const user = userEvent.setup();
    render(
      <ModelEffortSelector
        models={models}
        effort="max"
        onEffortChange={() => {}}
        onModelChange={() => {}}
      />,
    );
    expect(trigger()).toHaveAttribute("title", "Default model · Max");
    await openEffort(user);
    expect(screen.getByRole("menuitemradio", { name: "Max" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  it("hides the Effort submenu, and the effort label, when model selection is off", async () => {
    const user = userEvent.setup();
    render(
      <ModelEffortSelector
        models={models}
        onSwitchModel={() => {}}
        onSwitchEffort={() => {}}
        currentModelId="m2"
        currentEffort="medium"
        effortSupported={false}
      />,
    );
    expect(trigger()).toHaveAttribute("title", "Model Two");
    await user.click(trigger());
    await screen.findByRole("menuitem", { name: /^Model/ });
    expect(screen.queryByRole("menuitem", { name: /^Effort/ })).toBeNull();
  });
});
