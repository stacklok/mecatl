import { describe, expect, it, vi } from "vitest";
import {
  AUTHORIZATION_WINDOW_NAME,
  authorizationRequiredNotice,
  authorizationResolvedNotice,
  openAuthorizationWindow,
  reduceAuthorizationEvent,
} from "./mcp-authorization-phase";
import type { AuthorizationRequest, StreamEvent } from "./types";

const required = (
  overrides: Partial<Extract<StreamEvent, { type: "authorization" }>> = {},
): StreamEvent => ({
  type: "authorization",
  authorizationId: "auth-1",
  callId: "call-1",
  displayName: "GitHub MCP",
  status: "pending",
  expiresAt: 1_000_000,
  ...overrides,
});

const resolved = (
  overrides: Partial<
    Extract<StreamEvent, { type: "authorization_resolved" }>
  > = {},
): StreamEvent => ({
  type: "authorization_resolved",
  authorizationId: "auth-1",
  displayName: "GitHub MCP",
  status: "granted",
  ...overrides,
});

const pending: AuthorizationRequest = {
  authorizationId: "auth-1",
  sessionId: "s1",
  callId: "call-1",
  displayName: "GitHub MCP",
  expiresAt: 1_000_000,
};

describe("reduceAuthorizationEvent", () => {
  it("parks on a pending authorization.required", () => {
    expect(reduceAuthorizationEvent(null, required(), "s1")).toEqual(pending);
  });

  it("clears the pending request when the matching resolved arrives", () => {
    expect(reduceAuthorizationEvent(pending, resolved(), "s1")).toBeNull();
  });

  it("ignores a resolved for a different authorization", () => {
    expect(
      reduceAuthorizationEvent(
        pending,
        resolved({ authorizationId: "auth-other" }),
        "s1",
      ),
    ).toBe(pending);
  });

  it("treats a required frame that already carries a terminal status as resolved", () => {
    expect(
      reduceAuthorizationEvent(pending, required({ status: "expired" }), "s1"),
    ).toBeNull();
    expect(
      reduceAuthorizationEvent(null, required({ status: "denied" }), "s1"),
    ).toBeNull();
  });

  it("keeps a re-observed pending request (and its notice) while refreshing the expiry", () => {
    const withNotice = { ...pending, notice: "copied" };
    expect(
      reduceAuthorizationEvent(
        withNotice,
        required({ expiresAt: 2_000_000 }),
        "s1",
      ),
    ).toEqual({ ...withNotice, expiresAt: 2_000_000 });
  });

  it("leaves a resolved frame that still reads pending alone", () => {
    expect(
      reduceAuthorizationEvent(pending, resolved({ status: "pending" }), "s1"),
    ).toBe(pending);
  });

  it("is a no-op for every other stream event", () => {
    expect(
      reduceAuthorizationEvent(pending, { type: "token", text: "x" }, "s1"),
    ).toBe(pending);
    expect(
      reduceAuthorizationEvent(null, { type: "token", text: "x" }, "s1"),
    ).toBeNull();
  });
});

describe("authorization notices", () => {
  it("name the server, falling back when the daemon named none", () => {
    expect(authorizationRequiredNotice("GitHub MCP")).toBe(
      "Waiting for browser authorization: GitHub MCP",
    );
    expect(authorizationRequiredNotice("")).toBe(
      "Waiting for browser authorization: an MCP server",
    );
    expect(authorizationResolvedNotice("GitHub MCP", "granted")).toBe(
      "Browser authorization for GitHub MCP: signed in",
    );
    expect(authorizationResolvedNotice("", "cancelled")).toBe(
      "Browser authorization for an MCP server: cancelled",
    );
  });
});

describe("openAuthorizationWindow", () => {
  const popup = () =>
    ({
      closed: false,
      opener: {} as unknown,
      location: { href: "" },
      close: vi.fn(),
    }) as unknown as Window & { opener: unknown; close: () => void };

  it("opens the blank window synchronously, then navigates it with its opener severed", async () => {
    const window = popup();
    const open = vi.fn(() => window);
    const outcome = await openAuthorizationWindow(
      async () => "https://idp.example/authorize",
      open,
    );
    expect(outcome).toBe("opened");
    expect(open).toHaveBeenCalledWith("", AUTHORIZATION_WINDOW_NAME);
    expect(window.opener).toBeNull();
    expect(window.location.href).toBe("https://idp.example/authorize");
  });

  it("reports a blocked pop-up instead of throwing", async () => {
    const outcome = await openAuthorizationWindow(
      async () => "https://idp.example/authorize",
      vi.fn(() => null),
    );
    expect(outcome).toBe("blocked");
  });

  it("closes the blank window and rethrows when the URL fetch fails", async () => {
    const window = popup();
    await expect(
      openAuthorizationWindow(
        async () => {
          throw new Error("the pending MCP authorization has expired");
        },
        vi.fn(() => window),
      ),
    ).rejects.toThrow("expired");
    expect(window.close).toHaveBeenCalledTimes(1);
  });
});
