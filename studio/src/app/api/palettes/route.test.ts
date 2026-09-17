import { resolve } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GET } from "./route";

const FIXTURES = resolve(
  import.meta.dirname,
  "../../../../tests/fixtures/palettes",
);

/**
 * `/api/palettes` is the operator-palette read: same-origin gated like the
 * proxies, per-request (no-store) so a changed STUDIO_PALETTE_DIR needs no
 * rebuild, and never more than the validated documents in its body.
 */
describe("GET /api/palettes", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
  });

  it("answers an empty list when STUDIO_PALETTE_DIR is unset", async () => {
    vi.stubEnv("STUDIO_PALETTE_DIR", "");
    const response = await GET(
      new Request("http://localhost:3000/api/palettes"),
    );
    expect(response.status).toBe(200);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(await response.json()).toEqual({ palettes: [] });
  });

  it("serves the validated documents from the directory and logs the skipped file", async () => {
    vi.stubEnv("STUDIO_PALETTE_DIR", FIXTURES);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const response = await GET(
      new Request("http://localhost:3000/api/palettes"),
    );
    expect(response.status).toBe(200);
    const body = (await response.json()) as {
      palettes: Array<{ name: string }>;
    };
    expect(body.palettes.map((p) => p.name)).toEqual(["midnight"]);
    expect(JSON.stringify(body)).not.toContain("url(");
    expect(warn).toHaveBeenCalledWith(
      expect.stringMatching(/^\[palettes\] skipped broken\.json/),
    );
  });

  it("refuses a request from an origin Studio is not served from", async () => {
    vi.stubEnv("STUDIO_PALETTE_DIR", FIXTURES);
    const rebinding = await GET(
      new Request("http://attacker.example/api/palettes"),
    );
    expect(rebinding.status).toBe(403);
    const csrf = await GET(
      new Request("http://localhost:3000/api/palettes", {
        headers: { origin: "https://evil.example" },
      }),
    );
    expect(csrf.status).toBe(403);
    expect(await csrf.json()).toEqual({
      error: "request origin is not allowed",
    });
  });
});
