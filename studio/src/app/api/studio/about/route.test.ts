import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GET } from "./route";

/**
 * The route behind Help & about's configuration reference. It reports WHICH
 * names are set, never a value — the negative assertion below is the one
 * that matters — and it is same-origin only, like every other Studio API
 * route (a rebinding Host or a foreign Origin gets a 403).
 */

const TRUSTED = "http://localhost:3000/api/studio/about";

beforeEach(() => {
  // The default allowlist (localhost:3000 / 127.0.0.1:3000) applies.
  vi.stubEnv("MECATL_STUDIO_PUBLIC_ORIGIN", "");
  vi.stubEnv("MECATL_BASE_URL", "http://127.0.0.1:9");
  vi.stubEnv("MECATL_AUTH_TOKEN", "sekrit");
  vi.stubEnv("MECATL_OIDC_ISSUER", "");
  vi.stubEnv("NEXT_PUBLIC_STUDIO_VERSION", "0.1.0");
  vi.stubEnv("NEXT_PUBLIC_SDK_VERSION", "0.2.0");
  vi.stubEnv("NEXT_PUBLIC_STUDIO_BUILD", "0.1.0+abc1234");
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("GET /api/studio/about", () => {
  it("answers the versions, the mode and which names are set — never a value", async () => {
    const response = GET(new Request(TRUSTED));
    expect(response.status).toBe(200);
    expect(response.headers.get("Cache-Control")).toBe("no-store");
    const text = await response.text();
    const body = JSON.parse(text);
    expect(body).toMatchObject({
      version: "0.1.0",
      sdkVersion: "0.2.0",
      build: "0.1.0+abc1234",
      mode: "external",
    });
    expect(body.configured.MECATL_BASE_URL).toBe(true);
    expect(body.configured.MECATL_AUTH_TOKEN).toBe(true);
    expect(body.configured.MECATL_OIDC_ISSUER).toBe(false);
    expect(text).not.toContain("sekrit");
    expect(text).not.toContain("127.0.0.1:9");
  });

  it("reports managed mode when MECATL_BASE_URL is unset", async () => {
    vi.stubEnv("MECATL_BASE_URL", "");
    const body = await GET(new Request(TRUSTED)).json();
    expect(body.mode).toBe("managed");
    expect(body.configured.MECATL_BASE_URL).toBe(false);
  });

  it("refuses a request that reached a foreign Host (DNS rebinding)", async () => {
    const response = GET(new Request("http://evil.example/api/studio/about"));
    expect(response.status).toBe(403);
    expect(await response.text()).not.toContain("sekrit");
  });

  it("refuses a browser request from a foreign Origin (CSRF)", () => {
    // undici's Request drops the forbidden `origin` header, which would make
    // this vacuous; the handler reads only `url` + `headers.get`, so a plain
    // object stands in for the browser's request.
    const request = {
      url: TRUSTED,
      headers: new Map([["origin", "http://evil.example"]]),
    } as unknown as Request;
    expect(GET(request).status).toBe(403);
  });
});
