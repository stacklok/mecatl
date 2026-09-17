import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DaemonSoulTrust, HarnessTrustState } from "@/lib/harness/client";
import { memoryStorage } from "@/test/memory-storage";
import {
  TRUST_DRIFTED_MESSAGE,
  TRUST_UNTRUSTED_MESSAGE,
  trustBannerState,
  trustDismissKey,
  WorkspaceTrustBanner,
} from "./workspace-trust-banner";

/**
 * The first-encounter / drift trust prompt as a banner. Pins (1) the pure
 * decision table — external mode, no authority, trusted/once, a daemon that
 * already admitted the project (trusted PROJECT soul) and a "Not now" for
 * THIS anchor are all silent; untrusted prompts, drifted re-prompts, and a
 * changed anchor re-prompts past a dismissal — and (2) the rendered banner:
 * the three answers, each grant confirmed before the BODYLESS controller
 * call, "Not now" writing the anchor-keyed localStorage entry, and a refused
 * grant shown inline.
 */

const { runtime, trustWorkspace, trustWorkspaceOnce, fetchDaemonSoulTrust } =
  vi.hoisted(() => ({
    runtime: {
      connected: true,
      mode: "managed" as "managed" | "external",
      trust: null as HarnessTrustState | null,
      workspace: "/srv/repo",
      refresh: vi.fn(async () => {}),
    },
    trustWorkspace: vi.fn(async () => {}),
    trustWorkspaceOnce: vi.fn(async () => {}),
    fetchDaemonSoulTrust: vi.fn<() => Promise<DaemonSoulTrust | null>>(
      async () => null,
    ),
  }));

vi.mock("./runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

vi.mock("@/lib/harness/client", () => ({
  trustWorkspace,
  trustWorkspaceOnce,
  fetchDaemonSoulTrust,
}));

const untrusted: HarnessTrustState = {
  hasAuthority: true,
  decision: "untrusted",
  source: "none",
  anchor: "a".repeat(64),
};
const drifted: HarnessTrustState = { ...untrusted, decision: "drifted" };

beforeEach(() => {
  runtime.mode = "managed";
  runtime.connected = true;
  runtime.trust = untrusted;
  runtime.workspace = "/srv/repo";
  runtime.refresh.mockClear();
  trustWorkspace.mockClear();
  trustWorkspaceOnce.mockClear();
  fetchDaemonSoulTrust.mockReset();
  fetchDaemonSoulTrust.mockResolvedValue(null);
  // jsdom under this vitest has no localStorage of its own; the banner
  // reads/writes `window.localStorage`, so give it a fresh in-memory one.
  vi.stubGlobal("localStorage", memoryStorage());
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("trustBannerState", () => {
  const base = {
    mode: "managed" as const,
    dismissedAnchor: null,
    daemonSoul: null,
  };

  it("is silent in external mode, without controller trust, and without authority", () => {
    expect(
      trustBannerState({ ...base, mode: "external", trust: untrusted }),
    ).toBeNull();
    expect(trustBannerState({ ...base, trust: null })).toBeNull();
    expect(
      trustBannerState({
        ...base,
        trust: { ...untrusted, hasAuthority: false },
      }),
    ).toBeNull();
  });

  it("is silent once trusted — remembered, for this session, or by the posture", () => {
    for (const decision of ["trusted", "once"] as const) {
      expect(
        trustBannerState({ ...base, trust: { ...untrusted, decision } }),
      ).toBeNull();
    }
  });

  it("prompts on untrusted and re-prompts on drift, each with its own copy", () => {
    expect(trustBannerState({ ...base, trust: untrusted })).toEqual({
      kind: "untrusted",
      message: TRUST_UNTRUSTED_MESSAGE,
    });
    expect(trustBannerState({ ...base, trust: drifted })).toEqual({
      kind: "drifted",
      message: TRUST_DRIFTED_MESSAGE,
    });
    expect(TRUST_DRIFTED_MESSAGE).toMatch(/when the daemon last started/);
  });

  it("is silent when the daemon reports a TRUSTED project soul (admitted from its own registry), not for an untrusted or user soul", () => {
    expect(
      trustBannerState({
        ...base,
        trust: untrusted,
        daemonSoul: { projectSoul: true, trusted: true },
      }),
    ).toBeNull();
    expect(
      trustBannerState({
        ...base,
        trust: untrusted,
        daemonSoul: { projectSoul: true, trusted: false },
      }),
    ).not.toBeNull();
    expect(
      trustBannerState({
        ...base,
        trust: untrusted,
        daemonSoul: { projectSoul: false, trusted: true },
      }),
    ).not.toBeNull();
  });

  it("honours Not now for the SAME anchor only, and stays silent until the stored value is read", () => {
    expect(
      trustBannerState({
        ...base,
        trust: untrusted,
        dismissedAnchor: untrusted.anchor,
      }),
    ).toBeNull();
    expect(
      trustBannerState({
        ...base,
        trust: untrusted,
        dismissedAnchor: "b".repeat(64),
      }),
    ).not.toBeNull();
    expect(
      trustBannerState({
        ...base,
        trust: untrusted,
        dismissedAnchor: undefined,
      }),
    ).toBeNull();
  });
});

describe("WorkspaceTrustBanner", () => {
  it("renders the prompt with its three answers and the residual note", async () => {
    render(<WorkspaceTrustBanner />);
    const banner = await screen.findByRole("status", { name: "Project trust" });
    expect(banner).toHaveTextContent(TRUST_UNTRUSTED_MESSAGE);
    expect(banner).toHaveTextContent(/Studio cannot see that grant/);
    expect(screen.getByRole("button", { name: "Trust project" })).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "Trust for this session" }),
    ).toBeEnabled();
    expect(screen.getByRole("button", { name: "Not now" })).toBeEnabled();
    expect(fetchDaemonSoulTrust).toHaveBeenCalled();
  });

  it("renders nothing when there is nothing to prompt for", () => {
    runtime.trust = { ...untrusted, hasAuthority: false };
    render(<WorkspaceTrustBanner />);
    expect(screen.queryByRole("status")).toBeNull();
    runtime.trust = { ...untrusted, decision: "trusted", source: "studio" };
    render(<WorkspaceTrustBanner />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("hides once the daemon reports the project soul trusted", async () => {
    fetchDaemonSoulTrust.mockResolvedValue({
      projectSoul: true,
      trusted: true,
    });
    render(<WorkspaceTrustBanner />);
    await waitFor(() => expect(fetchDaemonSoulTrust).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByRole("status")).toBeNull());
  });

  it("Trust project confirms, then POSTs the remembered grant and re-probes", async () => {
    const user = userEvent.setup();
    render(<WorkspaceTrustBanner />);
    await user.click(
      await screen.findByRole("button", { name: "Trust project" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(/Trust this project\?/);
    expect(dialog).toHaveTextContent(/auto-approve tool calls/);
    expect(dialog).toHaveTextContent(/daemon restarts; in-flight runs end/);
    expect(trustWorkspace).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(trustWorkspace).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Trust project" }));
    await screen.findByRole("alertdialog");
    // Two "Trust project" buttons exist while the dialog is open — the
    // banner's and the dialog's action (the last one).
    const actions = screen.getAllByRole("button", { name: "Trust project" });
    await user.click(actions[actions.length - 1]);
    await waitFor(() => expect(trustWorkspace).toHaveBeenCalledTimes(1));
    expect(trustWorkspaceOnce).not.toHaveBeenCalled();
    await waitFor(() => expect(runtime.refresh).toHaveBeenCalled());
  });

  it("Trust for this session confirms with the controller-lifetime copy, then POSTs the once grant", async () => {
    const user = userEvent.setup();
    render(<WorkspaceTrustBanner />);
    await user.click(
      await screen.findByRole("button", { name: "Trust for this session" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(/until Studio's controller restarts/);
    expect(dialog).toHaveTextContent(/Nothing is saved/);
    const actions = screen.getAllByRole("button", {
      name: "Trust for this session",
    });
    await user.click(actions[actions.length - 1]);
    await waitFor(() => expect(trustWorkspaceOnce).toHaveBeenCalledTimes(1));
    expect(trustWorkspace).not.toHaveBeenCalled();
  });

  it("Not now writes the anchor under the workspace-keyed localStorage entry and hides the banner", async () => {
    const user = userEvent.setup();
    render(<WorkspaceTrustBanner />);
    await user.click(await screen.findByRole("button", { name: "Not now" }));
    expect(window.localStorage.getItem(trustDismissKey("/srv/repo"))).toBe(
      untrusted.anchor,
    );
    expect(screen.queryByRole("status")).toBeNull();
    expect(trustWorkspace).not.toHaveBeenCalled();
    expect(trustWorkspaceOnce).not.toHaveBeenCalled();
  });

  it("stays hidden for a dismissed anchor and re-prompts once the anchor changes (drift)", async () => {
    window.localStorage.setItem(trustDismissKey("/srv/repo"), untrusted.anchor);
    const first = render(<WorkspaceTrustBanner />);
    await waitFor(() => expect(fetchDaemonSoulTrust).toHaveBeenCalled());
    expect(screen.queryByRole("status")).toBeNull();
    first.unmount();

    runtime.trust = { ...drifted, anchor: "c".repeat(64) };
    render(<WorkspaceTrustBanner />);
    const banner = await screen.findByRole("status", { name: "Project trust" });
    expect(banner).toHaveTextContent(TRUST_DRIFTED_MESSAGE);
  });

  it("shows a refused grant inline", async () => {
    trustWorkspaceOnce.mockRejectedValueOnce(
      new Error("This setting is owned by the external mecated deployment."),
    );
    const user = userEvent.setup();
    render(<WorkspaceTrustBanner />);
    await user.click(
      await screen.findByRole("button", { name: "Trust for this session" }),
    );
    const actions = await screen.findAllByRole("button", {
      name: "Trust for this session",
    });
    await user.click(actions[actions.length - 1]);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /external mecated deployment/,
    );
    // The banner stays: nothing was granted.
    expect(screen.getByRole("status", { name: "Project trust" })).toBeVisible();
  });
});
