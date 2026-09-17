import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PaletteProvider } from "@/components/palette-provider";
import {
  loadOperatorPalettes,
  resetCustomPalettesForTests,
} from "@/lib/custom-palettes";
import { memoryStorage } from "@/test/memory-storage";
import AppearanceSettingsPage from "./page";

const HIDE_STARTER_PROMPTS_KEY = "mecatl-studio.hide-starter-prompts";

// The palette picker's mount starts the one-per-page /api/palettes fetch:
// stub it and let it settle up front so no state update lands outside act in
// the tests below.
beforeEach(async () => {
  vi.stubGlobal("localStorage", memoryStorage());
  resetCustomPalettesForTests();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ palettes: [] }), {
          headers: { "content-type": "application/json" },
        }),
    ),
  );
  await loadOperatorPalettes();
});
afterEach(() => resetCustomPalettesForTests());

/**
 * Personalize owns the new-chat preference: the starter-prompt switch
 * (default ON). Browser-local, so the page writes the same key the chat reads.
 */
describe("AppearanceSettingsPage — new chat preferences", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("offers starter prompts on by default and hides them via the switch", async () => {
    render(<AppearanceSettingsPage />);
    const toggle = screen.getByRole("switch", { name: "Starter prompts" });
    expect(toggle).toBeChecked();
    expect(window.localStorage.getItem(HIDE_STARTER_PROMPTS_KEY)).toBeNull();

    await userEvent.click(toggle);
    expect(toggle).not.toBeChecked();
    expect(window.localStorage.getItem(HIDE_STARTER_PROMPTS_KEY)).toBe("1");

    await userEvent.click(toggle);
    expect(toggle).toBeChecked();
    expect(window.localStorage.getItem(HIDE_STARTER_PROMPTS_KEY)).toBeNull();
  });
});

/**
 * The page is for office users: key rebinding and the preferences file
 * import/export are gone, and the remaining controls are grouped into three
 * plain cards.
 */
describe("AppearanceSettingsPage — layout", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("groups the controls into Appearance and Chat cards with no technical extras", () => {
    render(<AppearanceSettingsPage />);
    expect(
      screen.getByRole("heading", { name: "Appearance" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Chat" })).toBeInTheDocument();

    expect(screen.queryByText("Keyboard shortcuts")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByText("Preferences file")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Export" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Import/ }),
    ).not.toBeInTheDocument();
  });

  it("hides the Notifications card where the browser has no Notification API", () => {
    render(<AppearanceSettingsPage />);
    expect(
      screen.queryByRole("heading", { name: "Notifications" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Browser notifications")).not.toBeInTheDocument();
  });
});

/**
 * Text size (the interface scale multiplier) steps in 5% increments between
 * the bounds, and the buttons carry the visible row's words.
 */
describe("AppearanceSettingsPage — text size", () => {
  const SCALE_KEY = "mecatl-studio.ui-scale";

  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("steps the size up and down and persists it", async () => {
    const user = userEvent.setup();
    render(<AppearanceSettingsPage />);
    expect(screen.getByText("Text size")).toBeInTheDocument();
    expect(screen.getByText("100%")).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Increase text size" }),
    );
    expect(screen.getByText("105%")).toBeInTheDocument();
    expect(window.localStorage.getItem(SCALE_KEY)).toBe("1.05");

    await user.click(
      screen.getByRole("button", { name: "Decrease text size" }),
    );
    expect(screen.getByText("100%")).toBeInTheDocument();
  });
});

/**
 * "Messages while the agent works" is the Enter preference in plain words:
 * the three stored values ("queue", "steer", "queue-only") are unchanged, so
 * the composer reads exactly what it always did.
 */
describe("AppearanceSettingsPage — messages while the agent works", () => {
  const ENTER_KEY = "mecatl-studio.enter-send-behavior";
  const ROW = "Messages while the agent works";

  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("offers Always wait and persists it with a matching description", async () => {
    const user = userEvent.setup();
    render(<AppearanceSettingsPage />);
    const trigger = screen.getByRole("button", { name: ROW });
    expect(trigger).toHaveTextContent("Wait for the agent");
    expect(
      screen.getByText(/Shift\+Enter interrupts instead/),
    ).toBeInTheDocument();

    await user.click(trigger);
    for (const label of [
      "Wait for the agent",
      "Interrupt the agent",
      "Always wait",
    ]) {
      expect(
        await screen.findByRole("menuitem", { name: new RegExp(label) }),
      ).toBeInTheDocument();
    }
    await user.click(screen.getByRole("menuitem", { name: /Always wait/ }));

    expect(screen.getByRole("button", { name: ROW })).toHaveTextContent(
      "Always wait",
    );
    expect(window.localStorage.getItem(ENTER_KEY)).toBe("queue-only");
    expect(
      screen.getByText(/Every message waits until the agent finishes/),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Shift\+Enter interrupts instead/),
    ).not.toBeInTheDocument();
  });

  it("persists Interrupt the agent as the stored steer value", async () => {
    const user = userEvent.setup();
    render(<AppearanceSettingsPage />);
    await user.click(screen.getByRole("button", { name: ROW }));
    await user.click(
      await screen.findByRole("menuitem", { name: /Interrupt the agent/ }),
    );
    expect(window.localStorage.getItem(ENTER_KEY)).toBe("steer");
    expect(
      screen.getByText(/interrupts the agent; Shift\+Enter makes it wait/),
    ).toBeInTheDocument();
  });

  it("shows a stored Always wait choice on load", () => {
    window.localStorage.setItem(ENTER_KEY, "queue-only");
    render(<AppearanceSettingsPage />);
    expect(screen.getByRole("button", { name: ROW })).toHaveTextContent(
      "Always wait",
    );
  });
});

/**
 * The Palette row enumerates every built-in palette by name and description,
 * and picking one lands `data-palette` on <html> at once AND persists it,
 * while the light/dark Theme row stays a separate axis.
 */
describe("AppearanceSettingsPage — palette", () => {
  const PALETTE_KEY = "mecatl-studio.palette";

  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });
  afterEach(() => {
    document.documentElement.removeAttribute("data-palette");
  });

  it("lists the built-in palettes with descriptions and applies a choice", async () => {
    const user = userEvent.setup();
    render(<AppearanceSettingsPage />);
    const trigger = screen.getByRole("button", { name: "Palette" });
    expect(trigger).toHaveTextContent("Default");

    await user.click(trigger);
    for (const label of ["Default", "Aztec", "Mono", "Solar"]) {
      expect(
        await screen.findByRole("menuitem", { name: new RegExp(label) }),
      ).toBeInTheDocument();
    }
    expect(screen.getByRole("menuitem", { name: /Aztec/ })).toHaveTextContent(
      "Jade, turquoise and gold on obsidian.",
    );

    await user.click(screen.getByRole("menuitem", { name: /Aztec/ }));
    expect(screen.getByRole("button", { name: "Palette" })).toHaveTextContent(
      "Aztec",
    );
    expect(window.localStorage.getItem(PALETTE_KEY)).toBe("aztec");
    expect(document.documentElement.getAttribute("data-palette")).toBe("aztec");
    // The light/dark axis is untouched by a palette choice.
    expect(screen.getByRole("button", { name: "Theme" })).toHaveTextContent(
      "System",
    );
  });

  it("shows the stored palette on load under a deployment default, in one plain sentence", () => {
    window.localStorage.setItem(PALETTE_KEY, "solar");
    render(
      <PaletteProvider defaultPalette="mono">
        <AppearanceSettingsPage />
      </PaletteProvider>,
    );
    expect(screen.getByRole("button", { name: "Palette" })).toHaveTextContent(
      "Solar",
    );
    expect(
      screen.getByText("Accent colours for buttons and highlights."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/deployment/)).not.toBeInTheDocument();
  });
});

describe("AppearanceSettingsPage — start on", () => {
  const LAUNCH_KEY = "mecatl-studio.launch-target";

  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("defaults to New chat and persists Most recent chat", async () => {
    const user = userEvent.setup();
    render(<AppearanceSettingsPage />);
    const trigger = screen.getByRole("button", { name: "Start on" });
    expect(trigger).toHaveTextContent("New chat");
    expect(window.localStorage.getItem(LAUNCH_KEY)).toBeNull();
    expect(
      screen.getByText("What opens when you go to Chat."),
    ).toBeInTheDocument();

    await user.click(trigger);
    await user.click(
      await screen.findByRole("menuitem", { name: /Most recent chat/ }),
    );
    expect(screen.getByRole("button", { name: "Start on" })).toHaveTextContent(
      "Most recent chat",
    );
    expect(window.localStorage.getItem(LAUNCH_KEY)).toBe("latest");

    await user.click(screen.getByRole("button", { name: "Start on" }));
    await user.click(await screen.findByRole("menuitem", { name: /New chat/ }));
    expect(window.localStorage.getItem(LAUNCH_KEY)).toBeNull();
  });

  it("shows the stored choice on load", async () => {
    window.localStorage.setItem(LAUNCH_KEY, "latest");
    render(<AppearanceSettingsPage />);
    expect(
      await screen.findByRole("button", { name: "Start on" }),
    ).toHaveTextContent("Most recent chat");
  });
});

/**
 * Browser notifications appear only where the browser supports them, and the
 * row's words follow the permission state.
 */
describe("AppearanceSettingsPage — browser notifications", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function stubNotification(permission: NotificationPermission) {
    const requestPermission = vi.fn(async () => permission);
    vi.stubGlobal(
      "Notification",
      Object.assign(vi.fn(), { permission, requestPermission }),
    );
    return requestPermission;
  }

  it("offers Enable when permission has not been asked yet", async () => {
    stubNotification("default");
    render(<AppearanceSettingsPage />);
    expect(
      await screen.findByRole("heading", { name: "Notifications" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Browser notifications")).toBeInTheDocument();
    expect(
      screen.getByText("Get an alert when the agent finishes a task."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Enable" })).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "Send a test notification" }),
    ).toBeDisabled();
  });

  it("explains a blocked permission in plain words", async () => {
    stubNotification("denied");
    render(<AppearanceSettingsPage />);
    expect(
      await screen.findByText(
        "Blocked in your browser — allow notifications for this site to turn them on.",
      ),
    ).toBeInTheDocument();
  });

  it("shows Enabled and arms the test button once granted", async () => {
    stubNotification("granted");
    render(<AppearanceSettingsPage />);
    expect(
      await screen.findByRole("button", { name: "Enabled" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Send a test notification" }),
    ).toBeEnabled();
  });
});
