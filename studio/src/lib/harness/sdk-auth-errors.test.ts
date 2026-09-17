import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import { listHarnessModels, probeHarness } from "./inventory";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";

/**
 * Pins how a refused credential crosses the SDK into Studio's typed error.
 * The SDK collapses EVERY HTTP 401 into `AuthenticationError("Authentication
 * failed")` before reading the body, and narrows any code outside its
 * registry (the proxy's `oidc_*` codes, a 403's code) to "unknown" — the raw
 * problem body survives only on the error's `cause`. `toHarnessError` reads
 * it back so the offline banner and the chat strip can name the cause.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const fail = (call: () => Promise<unknown>): Promise<HarnessApiError> =>
  call().then(
    () => {
      throw new Error("expected the call to fail");
    },
    (caught: unknown) => caught as HarnessApiError,
  );

describe("toHarnessError on a refused credential", () => {
  it("a proxy 401 oidc_session_expired keeps its code and the proxy's own words", async () => {
    stubHarnessFetch(() =>
      jsonResponse(401, {
        code: "oidc_session_expired",
        error:
          "The OIDC session expired — sign in again from Settings to keep using this deployment.",
      }),
    );
    const error = await fail(listHarnessModels);
    expect(error).toBeInstanceOf(HarnessApiError);
    expect(error).toMatchObject({
      status: 401,
      code: "oidc_session_expired",
    });
    expect(error.message).toMatch(/sign in again from Settings/);
  });

  it("a proxy 401 oidc_login_required likewise", async () => {
    stubHarnessFetch(() =>
      jsonResponse(401, {
        code: "oidc_login_required",
        error: "This deployment requires OIDC sign-in.",
      }),
    );
    expect(await fail(listHarnessModels)).toMatchObject({
      status: 401,
      code: "oidc_login_required",
      message: "This deployment requires OIDC sign-in.",
    });
  });

  it("a daemon 401 `unauthenticated` (RFC 9457) keeps its code and detail", async () => {
    stubHarnessFetch(() =>
      jsonResponse(401, {
        type: "urn:mecatl:error:unauthenticated",
        code: "unauthenticated",
        title: "Unauthenticated",
        detail: "missing or invalid bearer token",
        status: 401,
      }),
    );
    expect(await fail(listHarnessModels)).toMatchObject({
      status: 401,
      code: "unauthenticated",
      message: "missing or invalid bearer token",
    });
  });

  it("a 401 with no body code falls back to the SDK's authentication code and its words", async () => {
    stubHarnessFetch(() => new Response(null, { status: 401 }));
    const error = await fail(listHarnessModels);
    expect(error).toMatchObject({ status: 401, code: "authentication" });
    expect(error.message).not.toBe("");
  });

  it("a proxy 502 oidc_idp_unavailable and a 403's code cross verbatim instead of degrading to ''", async () => {
    stubHarnessFetch(() =>
      jsonResponse(502, {
        code: "oidc_idp_unavailable",
        error: "Could not refresh the OIDC access token.",
      }),
    );
    expect(await fail(listHarnessModels)).toMatchObject({
      status: 502,
      code: "oidc_idp_unavailable",
      message: "Could not refresh the OIDC access token.",
    });
    await resetHarnessClient();

    stubHarnessFetch(() =>
      jsonResponse(403, { code: "forbidden", error: "audience mismatch" }),
    );
    expect(await fail(listHarnessModels)).toMatchObject({
      status: 403,
      code: "forbidden",
      message: "audience mismatch",
    });
  });

  it("a registered daemon code is untouched by the cause read", async () => {
    stubHarnessFetch(() =>
      jsonResponse(409, {
        code: "stale_run_control",
        error: "the named run already ended",
      }),
    );
    expect(await fail(listHarnessModels)).toMatchObject({
      status: 409,
      code: "stale_run_control",
    });
  });
});

describe("probeHarness typed failure", () => {
  it("surfaces the status and code of a refused credential", async () => {
    stubHarnessFetch(() =>
      jsonResponse(401, {
        code: "oidc_login_required",
        error: "This deployment requires OIDC sign-in.",
      }),
    );
    await expect(probeHarness()).resolves.toEqual({
      live: false,
      status: 401,
      code: "oidc_login_required",
      detail: "This deployment requires OIDC sign-in.",
    });
  });

  it("reports status 0 and no code when nothing answered", async () => {
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("connection refused");
    });
    const status = await probeHarness();
    expect(status.live).toBe(false);
    expect(status.status).toBe(0);
    expect(status.code).toBe("transport");
  });
});
