import { describe, expect, it } from "vitest";
import {
  requestIsTrusted,
  studioAllowedOrigins,
  type TrustCheckedRequest,
} from "./request-trust";

/** Plain-object requests: undici's Request drops forbidden headers like
 * `host`, which would make the rebinding rows vacuous. */
function fakeRequest(
  url: string,
  headers: Record<string, string> = {},
): TrustCheckedRequest {
  const map = new Map(
    Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v]),
  );
  return {
    url,
    headers: { get: (name) => map.get(name.toLowerCase()) ?? null },
  };
}

describe("studioAllowedOrigins", () => {
  it("defaults to the local dev origins", () => {
    // "" (unset) exercises the default without reading this process's env.
    expect(studioAllowedOrigins("")).toEqual(
      new Set(["http://localhost:3000", "http://127.0.0.1:3000"]),
    );
  });

  it("parses a configured comma list, trimming trailing slashes", () => {
    expect(
      studioAllowedOrigins("https://studio.example/, http://localhost:3000"),
    ).toEqual(new Set(["https://studio.example", "http://localhost:3000"]));
  });
});

/** The CSRF/DNS-rebinding truth table, mirroring the hermetic suite's idiom
 * (tests/rendered-html.test.mjs) for the shared server-tier check the proxy
 * routes AND the OIDC auth routes run. */
describe("requestIsTrusted", () => {
  const allowed = new Set(["http://localhost:3000"]);
  const table: Array<{
    name: string;
    request: TrustCheckedRequest;
    trusted: boolean;
  }> = [
    {
      name: "same-origin navigation with no Origin header",
      request: fakeRequest("http://localhost:3000/api/auth/oidc/start", {
        host: "localhost:3000",
      }),
      trusted: true,
    },
    {
      name: "same-origin fetch with a matching Origin",
      request: fakeRequest("http://localhost:3000/api/mecatl/v1/sessions", {
        host: "localhost:3000",
        origin: "http://localhost:3000",
      }),
      trusted: true,
    },
    {
      name: "cross-site request (CSRF): hostile Origin",
      request: fakeRequest("http://localhost:3000/api/auth/oidc/logout", {
        host: "localhost:3000",
        origin: "https://evil.example",
      }),
      trusted: false,
    },
    {
      name: "DNS rebinding: hostile Host header",
      request: fakeRequest("http://localhost:3000/api/mecatl/v1/models", {
        host: "attacker.example",
      }),
      trusted: false,
    },
    {
      name: "host absent falls back to the request URL's host",
      request: fakeRequest("http://localhost:3000/api/auth/oidc/status"),
      trusted: true,
    },
  ];

  for (const row of table) {
    it(row.name, () => {
      expect(requestIsTrusted(row.request, allowed)).toBe(row.trusted);
    });
  }

  it("honors x-forwarded-proto when a TLS terminator fronts Studio", () => {
    const https = new Set(["https://studio.example"]);
    expect(
      requestIsTrusted(
        fakeRequest("http://studio.example/api/auth/oidc/status", {
          host: "studio.example",
          "x-forwarded-proto": "https",
        }),
        https,
      ),
    ).toBe(true);
    expect(
      requestIsTrusted(
        fakeRequest("http://studio.example/api/auth/oidc/status", {
          host: "studio.example",
          "x-forwarded-proto": "http",
        }),
        https,
      ),
    ).toBe(false);
  });
});
