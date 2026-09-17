import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  AuthRecoveryBanner,
  fetchOidcSignInStatus,
  MANAGED_CREDENTIAL_REMEDY,
  OIDC_POPUP_FEATURES,
  OIDC_POPUP_NAME,
  OIDC_START_URL,
  OIDC_STATUS_URL,
  SIGN_IN_SETTINGS_HREF,
} from "./auth-recovery-banner";
import { classifyOffline } from "./offline-cause";

/**
 * The auth-recovery banner: names the cause class, offers Sign in only when
 * the server-tier OIDC status says the deployment is configured for it,
 * opens the popup on the click, links to the sign-in settings and keeps
 * Retry, and reconnects at once on the callback page's `mecatl-oidc`
 * message and on window focus. In managed mode a rejected credential is the
 * controller-spawned daemon refusing the controller's token: the banner
 * offers Restart daemon instead of a sign-in link to a page with no sign-in
 * card, and never reads the OIDC status.
 */

const harness = vi.hoisted(() => ({
  restartHarnessDaemon: vi.fn(async () => undefined),
}));

vi.mock("@/lib/harness/client", () => ({
  restartHarnessDaemon: harness.restartHarnessDaemon,
}));

const statusBody = (over: Record<string, unknown> = {}) =>
  new Response(
    JSON.stringify({
      configured: true,
      state: "signed-out",
      issuer: "https://idp.example.com/realms/mecatl",
      ...over,
    }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );

const loginRequired = classifyOffline({
  status: 401,
  code: "oidc_login_required",
  detail: "This deployment requires OIDC sign-in.",
});
const sessionExpired = classifyOffline({
  status: 401,
  code: "oidc_session_expired",
  detail: "The OIDC session expired.",
});
const credentialRejected = classifyOffline({
  status: 401,
  code: "unauthenticated",
  detail: "missing or invalid bearer token",
});
const idpUnavailable = classifyOffline({
  status: 502,
  code: "oidc_idp_unavailable",
  detail: "Could not refresh the OIDC access token.",
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  harness.restartHarnessDaemon.mockClear();
});

describe("AuthRecoveryBanner", () => {
  it("names a sign-in-required cause and offers Sign in once the status says OIDC is configured", async () => {
    const fetchMock = vi.fn(async () => statusBody());
    vi.stubGlobal("fetch", fetchMock);
    render(<AuthRecoveryBanner cause={loginRequired} onRetry={vi.fn()} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Sign-in required");
    expect(alert).toHaveTextContent(/Sign in to reconnect/);
    expect(alert).toHaveAttribute("data-offline-cause", "login-required");
    // The one configured target is named by its issuer.
    expect(
      await screen.findByText("https://idp.example.com/realms/mecatl"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
    // The status is read exactly once, on mount.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(
      OIDC_STATUS_URL,
      expect.objectContaining({ cache: "no-store" }),
    );
  });

  it("labels the expired session's action 'Sign in again' and opens the popup on the click", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => statusBody({ state: "expired" })),
    );
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    render(<AuthRecoveryBanner cause={sessionExpired} onRetry={vi.fn()} />);

    expect(screen.getByRole("alert")).toHaveTextContent("Sign-in expired");
    const button = await screen.findByRole("button", {
      name: "Sign in again",
    });
    fireEvent.click(button);
    expect(open).toHaveBeenCalledWith(
      OIDC_START_URL,
      OIDC_POPUP_NAME,
      OIDC_POPUP_FEATURES,
    );
  });

  it("withholds Sign in when OIDC is not configured, but keeps the settings link and Retry", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        statusBody({
          configured: false,
          state: "not-configured",
          issuer: undefined,
        }),
      ),
    );
    const onRetry = vi.fn();
    render(<AuthRecoveryBanner cause={loginRequired} onRetry={onRetry} />);

    const link = screen.getByRole("link", { name: "Open sign-in settings" });
    expect(link).toHaveAttribute("href", SIGN_IN_SETTINGS_HREF);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
    // Let the status read settle: still no Sign in.
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("button", { name: /Sign in/ }),
    ).not.toBeInTheDocument();
  });

  it("never offers Sign in for a rejected credential or an unreachable identity provider, even when OIDC is configured", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => statusBody()),
    );
    const { unmount } = render(
      <AuthRecoveryBanner cause={credentialRejected} onRetry={vi.fn()} />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Credential rejected");
    expect(screen.getByRole("alert")).toHaveTextContent(/check its token or sign-in settings/);
    await screen.findByText("https://idp.example.com/realms/mecatl");
    expect(
      screen.queryByRole("button", { name: /Sign in/ }),
    ).not.toBeInTheDocument();
    unmount();

    render(<AuthRecoveryBanner cause={idpUnavailable} onRetry={vi.fn()} />);
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Identity provider unreachable",
    );
    await screen.findByText("https://idp.example.com/realms/mecatl");
    expect(
      screen.queryByRole("button", { name: /Sign in/ }),
    ).not.toBeInTheDocument();
  });

  it("re-probes at once on the callback page's same-origin `mecatl-oidc` message and on window focus", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => statusBody()),
    );
    const onRetry = vi.fn();
    render(<AuthRecoveryBanner cause={sessionExpired} onRetry={onRetry} />);
    await screen.findByRole("button", { name: "Sign in again" });

    // A message from another origin is ignored.
    window.dispatchEvent(
      new MessageEvent("message", {
        data: { type: "mecatl-oidc", ok: true },
        origin: "https://evil.example",
      }),
    );
    expect(onRetry).not.toHaveBeenCalled();

    window.dispatchEvent(
      new MessageEvent("message", {
        data: { type: "mecatl-oidc", ok: true },
        origin: window.location.origin,
      }),
    );
    expect(onRetry).toHaveBeenCalledTimes(1);

    window.dispatchEvent(new Event("focus"));
    expect(onRetry).toHaveBeenCalledTimes(2);
  });

  it("survives an unreadable status route: no Sign in, no crash", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("network down");
      }),
    );
    await expect(fetchOidcSignInStatus()).resolves.toBeNull();
    render(<AuthRecoveryBanner cause={loginRequired} onRetry={vi.fn()} />);
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("button", { name: /Sign in/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("keeps the sign-in settings link for a rejected credential in external mode", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        statusBody({ configured: false, state: "not-configured" }),
      ),
    );
    render(
      <AuthRecoveryBanner
        cause={credentialRejected}
        onRetry={vi.fn()}
        mode="external"
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(/check its token or sign-in settings/);
    expect(
      screen.getByRole("link", { name: "Open sign-in settings" }),
    ).toHaveAttribute("href", SIGN_IN_SETTINGS_HREF);
    expect(
      screen.queryByRole("button", { name: "Restart daemon" }),
    ).not.toBeInTheDocument();
    // Let the status read settle: still no Sign in for a rejected credential.
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("button", { name: /Sign in/ }),
    ).not.toBeInTheDocument();
  });

  it("in managed mode offers Restart daemon for a rejected credential — no sign-in link, no OIDC status read", async () => {
    const fetchMock = vi.fn(async () => statusBody());
    vi.stubGlobal("fetch", fetchMock);
    const onRetry = vi.fn(async () => undefined);
    render(
      <AuthRecoveryBanner
        cause={credentialRejected}
        onRetry={onRetry}
        mode="managed"
      />,
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Credential rejected");
    expect(alert).toHaveTextContent(MANAGED_CREDENTIAL_REMEDY);
    // The external-mode env remediation would mislead here.
    expect(alert).not.toHaveTextContent(/check its token or sign-in settings/);
    // The provider page mounts no sign-in card in managed mode: no link to it.
    expect(
      screen.queryByRole("link", { name: "Open sign-in settings" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Sign in/ }),
    ).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Restart daemon" }));
    await waitFor(() =>
      expect(harness.restartHarnessDaemon).toHaveBeenCalledTimes(1),
    );
    // The restart is followed by the provider's own re-probe.
    await waitFor(() => expect(onRetry).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("in managed mode surfaces a failed restart in the banner", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => statusBody()),
    );
    harness.restartHarnessDaemon.mockRejectedValueOnce(
      new Error("controller refused the restart"),
    );
    const onRetry = vi.fn(async () => undefined);
    render(
      <AuthRecoveryBanner
        cause={credentialRejected}
        onRetry={onRetry}
        mode="managed"
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Restart daemon" }));
    expect(
      await screen.findByText("controller refused the restart"),
    ).toBeInTheDocument();
    expect(onRetry).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: "Restart daemon" }),
    ).not.toBeDisabled();
  });

  it("in managed mode an oidc_* cause keeps the sign-in actions — the code proves the proxy is external", async () => {
    const fetchMock = vi.fn(async () => statusBody({ state: "expired" }));
    vi.stubGlobal("fetch", fetchMock);
    render(
      <AuthRecoveryBanner
        cause={sessionExpired}
        onRetry={vi.fn()}
        mode="managed"
      />,
    );
    expect(
      await screen.findByRole("button", { name: "Sign in again" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Open sign-in settings" }),
    ).toHaveAttribute("href", SIGN_IN_SETTINGS_HREF);
    expect(
      screen.queryByRole("button", { name: "Restart daemon" }),
    ).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
