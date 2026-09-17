import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AuthorizationRequest } from "@/features/agent";
import {
  AUTHORIZATION_POLLING_STATUS,
  AuthorizationPanel,
  formatAuthorizationCountdown,
} from "./authorization-panel";

const authorization: AuthorizationRequest = {
  authorizationId: "auth-1",
  sessionId: "s1",
  callId: "call-1",
  displayName: "GitHub MCP",
};

function renderPanel(
  overrides: Partial<AuthorizationRequest> = {},
  handlers: Partial<{
    onOpen: () => Promise<void>;
    onCopyLink: () => Promise<boolean>;
    onRecheck: () => Promise<void>;
    onCancel: () => Promise<void>;
  }> = {},
) {
  const props = {
    onOpen: vi.fn(async () => {}),
    onCopyLink: vi.fn(async () => true),
    onRecheck: vi.fn(async () => {}),
    onCancel: vi.fn(async () => {}),
    ...handlers,
  };
  render(
    <AuthorizationPanel
      authorization={{ ...authorization, ...overrides }}
      {...props}
    />,
  );
  return props;
}

describe("AuthorizationPanel", () => {
  it("names the MCP server and offers the four authorization actions", () => {
    renderPanel();
    expect(
      screen.getByRole("heading", { name: "Browser authorization required" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/GitHub MCP needs you to sign in in your browser/),
    ).toBeInTheDocument();
    for (const name of [
      "Open sign-in page",
      "Copy link",
      "I've finished — re-check",
      "Cancel",
    ]) {
      expect(screen.getByRole("button", { name })).toBeEnabled();
    }
  });

  it("routes each button to its handler", async () => {
    const handlers = renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Open sign-in page" }));
    await waitFor(() => expect(handlers.onOpen).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    await waitFor(() => expect(handlers.onCopyLink).toHaveBeenCalledTimes(1));
    fireEvent.click(
      screen.getByRole("button", { name: "I've finished — re-check" }),
    );
    await waitFor(() => expect(handlers.onRecheck).toHaveBeenCalledTimes(1));
    // Cancel fails the parked tool call, so it confirms first.
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Cancel the sign-in?");
    expect(dialog).toHaveTextContent(
      "The tool call waiting on GitHub MCP is cancelled and the run carries on without it.",
    );
    expect(handlers.onCancel).not.toHaveBeenCalled();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Cancel sign-in" }),
    );
    await waitFor(() => expect(handlers.onCancel).toHaveBeenCalledTimes(1));
  });

  it("keeps waiting when the cancel confirmation is declined", async () => {
    const handlers = renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Keep waiting" }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(handlers.onCancel).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled(),
    );
  });

  it("shows the polling line only while Studio re-checks on the 3 s cadence", () => {
    renderPanel();
    expect(screen.queryByText(AUTHORIZATION_POLLING_STATUS)).toBeNull();
  });

  it("names the poll cadence once the sign-in page was opened or copied", () => {
    renderPanel({ polling: true });
    expect(screen.getByText(AUTHORIZATION_POLLING_STATUS)).toBeInTheDocument();
    expect(AUTHORIZATION_POLLING_STATUS).toMatch(/checking every 3 seconds/);
  });

  it("disables every action while one is in flight, then re-enables them", async () => {
    let finish: () => void = () => {};
    const onRecheck = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    renderPanel({}, { onRecheck });
    fireEvent.click(
      screen.getByRole("button", { name: "I've finished — re-check" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled(),
    );
    expect(screen.getByRole("button", { name: "Copy link" })).toBeDisabled();
    finish();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled(),
    );
  });

  it("renders the daemon's error line verbatim as an alert", () => {
    renderPanel({
      error:
        "the pending MCP authorization has expired. Re-check to see where the run stands.",
    });
    expect(screen.getByRole("alert")).toHaveTextContent(
      "the pending MCP authorization has expired",
    );
  });

  it("renders a status notice when there is no error", () => {
    renderPanel({ notice: "Sign-in link copied." });
    expect(screen.getByRole("status")).toHaveTextContent(
      "Sign-in link copied.",
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows the expiry countdown when the daemon set one", () => {
    renderPanel({ expiresAt: Date.now() + 5 * 60_000 + 500 });
    expect(screen.getByText(/Expires in [45]m \d+s/)).toBeInTheDocument();
  });

  it("falls back to a generic server name when the daemon named none", () => {
    renderPanel({ displayName: "  " });
    expect(
      screen.getByText(/An MCP server needs you to sign in/),
    ).toBeInTheDocument();
  });
});

describe("formatAuthorizationCountdown", () => {
  it("is absent without an expiry", () => {
    expect(formatAuthorizationCountdown(undefined, 1_000)).toBeNull();
  });

  it("counts minutes and seconds down, then nudges a re-check once expired", () => {
    expect(formatAuthorizationCountdown(125_000, 0)).toBe("Expires in 2m 5s");
    expect(formatAuthorizationCountdown(45_000, 0)).toBe("Expires in 45s");
    expect(formatAuthorizationCountdown(1_000, 5_000)).toBe(
      "The sign-in window has expired — re-check to confirm.",
    );
  });
});
