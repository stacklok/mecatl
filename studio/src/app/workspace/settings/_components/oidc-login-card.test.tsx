import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  OIDC_START_URL,
  OidcLoginCard,
  type OidcStatus,
} from "./oidc-login-card";

/**
 * The sign-in card, kept to what a user does: review a discovered identity
 * provider (default-deny until "Continue to sign in" confirms — the popup is
 * pointed at the authorize redirect only AFTER the server accepted the hash,
 * and closed on refusal), sign in, sign in again, sign out. The copy-link
 * flow and the deployment rows (credential ordering, token store, transport,
 * sign-in window) are gone. No token and no deployment address ever render.
 */

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });

const baseStatus: OidcStatus = {
  configured: true,
  state: "signed-out",
  source: "env",
  issuer: "https://idp.example.com/realms/mecatl",
  clientId: "studio-client",
  audience: "mecatl-daemon",
  scopes: ["openid", "profile", "offline_access"],
  authMode: "oidc",
  store: { kind: "memory" },
  transport: { tlsCa: false, insecure: true, privateIssuer: false },
  callbackTimeoutSeconds: 600,
};

type Handler = (url: string, init?: RequestInit) => Response | undefined;

function stubFetch(handler: Handler) {
  const calls: { url: string; init?: RequestInit }[] = [];
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      calls.push({ url, init });
      return handler(url, init) ?? json(404, { error: "unexpected" });
    },
  );
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("OidcLoginCard — discovered profile review", () => {
  const discovered: OidcStatus = {
    ...baseStatus,
    configured: false,
    state: "discovered",
    source: "discovery",
    profileHash: "a".repeat(64),
  };

  it("names the identity provider and confirms BEFORE pointing the popup at the authorize redirect", async () => {
    const popup = { location: { href: "about:blank" }, close: vi.fn() };
    const open = vi.fn(() => popup as unknown as Window);
    vi.stubGlobal("open", open);
    const calls = stubFetch((url, init) => {
      if (url === "/api/auth/oidc/status") return json(200, discovered);
      if (url === "/api/auth/oidc/confirm-discovery" && init?.method === "POST")
        return json(200, { ok: true });
      return undefined;
    });
    render(<OidcLoginCard />);

    expect(await screen.findByTestId("oidc-issuer")).toHaveTextContent(
      "https://idp.example.com/realms/mecatl",
    );
    expect(screen.getByText("Review before signing in")).toBeInTheDocument();
    // Only the identity provider is listed — no client id, audience, scopes
    // or deployment rows.
    expect(screen.queryByText("studio-client")).not.toBeInTheDocument();
    expect(screen.queryByText("mecatl-daemon")).not.toBeInTheDocument();
    expect(screen.queryByText(/offline_access/)).not.toBeInTheDocument();
    expect(
      screen.queryByText(/Token store|Transport|Sign-in window/),
    ).toBeNull();
    // Default-deny: no plain Sign in button while unconfirmed.
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();

    fireEvent.click(
      screen.getByRole("button", { name: "Continue to sign in" }),
    );
    // The popup opens synchronously on the click (popup-blocker safe) but on
    // about:blank — the issuer is not reached before the confirmation.
    expect(open).toHaveBeenCalledWith(
      "about:blank",
      "mecatl-oidc-login",
      expect.any(String),
    );
    expect(popup.location.href).toBe("about:blank");

    await waitFor(() => expect(popup.location.href).toBe(OIDC_START_URL));
    const confirm = calls.find(
      (c) => c.url === "/api/auth/oidc/confirm-discovery",
    );
    expect(confirm?.init?.method).toBe("POST");
    expect(JSON.parse(String(confirm?.init?.body))).toEqual({
      profileHash: "a".repeat(64),
    });
    expect(popup.close).not.toHaveBeenCalled();
  });

  it("closes the popup and shows the refusal when the server rejects the hash", async () => {
    const popup = { location: { href: "about:blank" }, close: vi.fn() };
    vi.stubGlobal(
      "open",
      vi.fn(() => popup as unknown as Window),
    );
    stubFetch((url, init) => {
      if (url === "/api/auth/oidc/status") return json(200, discovered);
      if (url === "/api/auth/oidc/confirm-discovery" && init?.method === "POST")
        return json(409, {
          ok: false,
          error:
            "The discovered sign-in profile changed since it was reviewed — review it again.",
        });
      return undefined;
    });
    render(<OidcLoginCard />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Continue to sign in" }),
    );
    await waitFor(() => expect(popup.close).toHaveBeenCalled());
    expect(popup.location.href).toBe("about:blank");
    expect(screen.getByRole("status")).toHaveTextContent(/review it again/);
  });
});

describe("OidcLoginCard — sign in, sign in again, sign out", () => {
  it("signed out: Sign in opens the popup on the click; no copy-link flow, no deployment rows", async () => {
    const open = vi.fn(() => null);
    vi.stubGlobal("open", open);
    stubFetch((url) =>
      url === "/api/auth/oidc/status" ? json(200, baseStatus) : undefined,
    );
    render(<OidcLoginCard />);
    expect(await screen.findByText("Not signed in")).toBeInTheDocument();
    expect(screen.getByText("Sign in opens a new window.")).toBeInTheDocument();
    expect(screen.getByTestId("oidc-issuer")).toHaveTextContent(
      "https://idp.example.com/realms/mecatl",
    );
    expect(
      screen.queryByRole("button", { name: /Copy sign-in link/ }),
    ).not.toBeInTheDocument();
    // The transport warning and every other deployment row stay off the card.
    expect(screen.queryByText(/verification is disabled/)).toBeNull();
    expect(screen.queryByText(/MECATL_/)).toBeNull();
    expect(
      screen.queryByText(/Authentication|Token store|Transport/),
    ).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(open).toHaveBeenCalledWith(
      OIDC_START_URL,
      "mecatl-oidc-login",
      expect.any(String),
    );
  });

  it("expired: says so in plain words and offers Sign in again", async () => {
    stubFetch((url) =>
      url === "/api/auth/oidc/status"
        ? json(200, { ...baseStatus, state: "expired" })
        : undefined,
    );
    render(<OidcLoginCard />);
    expect(await screen.findByText("Sign-in expired")).toBeInTheDocument();
    expect(
      screen.getByText("Sign in again to keep using the agent."),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Sign in again" }),
    ).toBeInTheDocument();
  });

  it("signed in: shows who is signed in and signs out through the server", async () => {
    const calls = stubFetch((url, init) => {
      if (url === "/api/auth/oidc/status")
        return json(200, {
          ...baseStatus,
          state: "signed-in",
          email: "op@example.com",
        });
      if (url === "/api/auth/oidc/logout" && init?.method === "POST")
        return json(200, { ok: true });
      return undefined;
    });
    render(<OidcLoginCard />);
    expect(await screen.findByText("Signed in")).toBeInTheDocument();
    expect(screen.getByText("op@example.com")).toBeInTheDocument();
    expect(screen.queryByTestId("oidc-issuer")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url === "/api/auth/oidc/logout")).toBe(true),
    );
  });

  it("not configured: one plain note, no environment variable names", async () => {
    stubFetch((url) =>
      url === "/api/auth/oidc/status"
        ? json(200, {
            configured: false,
            state: "not-configured",
            problem: "MECATL_OIDC_ISSUER is set without MECATL_OIDC_CLIENT_ID",
            authMode: "static",
            store: { kind: "memory" },
            transport: { tlsCa: false, insecure: false, privateIssuer: false },
            callbackTimeoutSeconds: 600,
          })
        : undefined,
    );
    render(<OidcLoginCard />);
    expect(
      await screen.findByText("Sign-in is not set up for this agent."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/MECATL_/)).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("says when the status could not be read", async () => {
    stubFetch(() => json(500, { error: "boom" }));
    render(<OidcLoginCard />);
    expect(
      await screen.findByText(
        "The sign-in status could not be read right now.",
      ),
    ).toBeInTheDocument();
  });
});
