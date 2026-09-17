import { render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { KEYMAP_STORAGE_KEY } from "@/lib/shortcuts/keymap";
import { describeShortcut, SHORTCUTS } from "@/lib/shortcuts/registry";
import { memoryStorage } from "@/test/memory-storage";
import { ShortcutsReference } from "./shortcuts-reference";

/**
 * Pins the help reference against the runtime-status context: connected, the
 * features section derives its rows from the daemon's capabilities (an
 * enabled row has no tag, a disabled one carries "not enabled"); offline, it
 * shows the connect copy and no rows at all. The chord list and the usage
 * legend render regardless of the connection.
 */

const runtime = vi.hoisted(() => ({
  state: "connected" as "connected" | "connecting" | "offline",
  serverCapabilities: {} as Record<string, unknown>,
  features: new Set<string>(),
  deployment: "",
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => ({
    state: runtime.state,
    connected: runtime.state === "connected",
    serverCapabilities: runtime.serverCapabilities,
    features: runtime.features,
    deployment: runtime.deployment,
  }),
}));

function featureRow(label: string): HTMLElement {
  const row = screen.getByText(label).closest("li");
  if (!row) throw new Error(`no feature row labelled ${label}`);
  return row;
}

beforeEach(() => {
  runtime.state = "connected";
  runtime.serverCapabilities = { steer: true, image: false, skills: true };
  runtime.features = new Set();
  runtime.deployment = "";
});

describe("ShortcutsReference", () => {
  it("renders every registry shortcut with its live description", () => {
    render(<ShortcutsReference />);
    // The Enter preference hydrates to "queue" on first render.
    for (const def of SHORTCUTS) {
      expect(
        screen.getByText(describeShortcut(def, "queue")),
      ).toBeInTheDocument();
    }
  });

  it("lists the /help Studio command", () => {
    render(<ShortcutsReference />);
    expect(screen.getByText("/help")).toBeInTheDocument();
    expect(
      screen.getByText("Open this page from the composer"),
    ).toBeInTheDocument();
  });

  it("tags disabled features and leaves enabled ones untagged", () => {
    render(<ShortcutsReference />);
    expect(screen.getByText("Features on this daemon")).toBeInTheDocument();

    const steer = featureRow("Steer");
    expect(steer).toHaveAttribute("data-enabled", "true");
    expect(within(steer).queryByText("not enabled")).toBeNull();

    const skills = featureRow("Skills");
    expect(within(skills).queryByText("not enabled")).toBeNull();

    const image = featureRow("Image attachments");
    expect(image).toHaveAttribute("data-enabled", "false");
    expect(within(image).getByText("not enabled")).toBeInTheDocument();
  });

  it("enables steer from the http_steer feature when the capability is absent", () => {
    runtime.serverCapabilities = {};
    runtime.features = new Set(["http_steer"]);
    render(<ShortcutsReference />);
    expect(within(featureRow("Steer")).queryByText("not enabled")).toBeNull();
  });

  it("shows the deployment label when the daemon sets one", () => {
    runtime.deployment = "staging-eu";
    render(<ShortcutsReference />);
    expect(screen.getByText(/Agent: staging-eu/)).toBeInTheDocument();
  });

  it("renders the usage legend with the chat menu's own labels", () => {
    render(<ShortcutsReference />);
    expect(screen.getByText("Reading the numbers")).toBeInTheDocument();
    for (const label of [
      "input",
      "output",
      "cache read",
      "cache write",
      "reasoning",
      "cache hit rate",
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    expect(
      screen.getByText(/The context meter is approximate/),
    ).toBeInTheDocument();
  });

  it("offline: shows the connect copy and no feature rows", () => {
    runtime.state = "offline";
    render(<ShortcutsReference />);
    expect(
      screen.getByText(
        "Connect to an agent to see which features are turned on.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("Steer")).toBeNull();
    expect(screen.queryByText("not enabled")).toBeNull();
    // The chord list still renders — it needs no daemon.
    expect(screen.getByText("Open search")).toBeInTheDocument();
  });

  it("connecting: says it is still checking, with no rows yet", () => {
    runtime.state = "connecting";
    render(<ShortcutsReference />);
    expect(
      screen.getByText("Checking which features are turned on…"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Steer")).toBeNull();
  });

  it("renders a remapped key's own keycaps with a custom tag; untouched rows stay plain", () => {
    vi.stubGlobal("localStorage", memoryStorage());
    window.localStorage.setItem(
      KEYMAP_STORAGE_KEY,
      JSON.stringify({ "chat.new": "mod+shift+k" }),
    );
    render(<ShortcutsReference />);

    const newChat = screen.getByText("New chat").closest("li");
    if (!newChat) throw new Error("no New chat row");
    expect(within(newChat).getByText("custom")).toBeInTheDocument();
    expect(
      [...newChat.querySelectorAll("kbd")].map((k) => k.textContent),
    ).toEqual(["⌘", "⇧", "K"]);

    const search = screen.getByText("Open search").closest("li");
    if (!search) throw new Error("no Open search row");
    expect(within(search).queryByText("custom")).toBeNull();
    expect(
      [...search.querySelectorAll("kbd")].map((k) => k.textContent),
    ).toEqual(["⌘", "K"]);
  });

  it("documents the approval verdict keys under an Approvals heading", () => {
    render(<ShortcutsReference />);
    expect(
      screen.getByRole("heading", { name: "Approvals" }),
    ).toBeInTheDocument();
    const rows: Array<[string, string]> = [
      ["Allow once — while a permission ask is waiting", "Y"],
      ["Always allow — main-agent asks only, never a subagent's", "W"],
      ["Deny the pending permission ask", "N"],
    ];
    for (const [description, cap] of rows) {
      const row = screen.getByText(description).closest("li");
      if (!row) throw new Error(`no row for ${description}`);
      expect(
        [...row.querySelectorAll("kbd")].map((k) => k.textContent),
      ).toEqual([cap]);
    }
    // Esc's row says it denies a pending ask before it closes or stops.
    expect(
      screen.getByText(
        "Clear the selection, deny the pending permission ask, close the side panel — or stop the running turn",
      ),
    ).toBeInTheDocument();
  });
});
