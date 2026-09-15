import { beforeEach, expect, it, vi } from "vitest";
import { GET } from "./route";

const mockReadFile = vi.hoisted(() => vi.fn());
vi.mock("node:fs/promises", () => ({ readFile: mockReadFile }));

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal("fetch", vi.fn());
  mockReadFile.mockResolvedValue("<svg>stacklok logo</svg>");
});

it("serves the fallback SVG when BRAND_LOGO_URL is not set", async () => {
  vi.stubEnv("BRAND_LOGO_URL", "");
  const response = await GET();
  expect(response.headers.get("Content-Type")).toBe("image/svg+xml");
  expect(mockReadFile).toHaveBeenCalledWith(
    `${process.cwd()}/public/stacklok-logo.svg`,
  );
});

it("proxies BRAND_LOGO_URL and forwards the upstream Content-Type", async () => {
  vi.stubEnv("BRAND_LOGO_URL", "https://example.com/logo.png");
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        new Response("PNG data", { headers: { "Content-Type": "image/png" } }),
      ),
  );
  const response = await GET();
  expect(response.headers.get("Content-Type")).toBe("image/png");
});

it("defaults Content-Type to image/svg+xml when upstream omits it", async () => {
  vi.stubEnv("BRAND_LOGO_URL", "https://example.com/logo.svg");
  const encoder = new TextEncoder();
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(encoder.encode("<svg>brand</svg>"));
      controller.close();
    },
  });
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(stream, { status: 200 })),
  );
  const response = await GET();
  expect(response.headers.get("Content-Type")).toBe("image/svg+xml");
});

it("returns 404 when upstream returns a non-ok status", async () => {
  vi.stubEnv("BRAND_LOGO_URL", "https://example.com/logo.svg");
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(null, { status: 500 })),
  );
  const response = await GET();
  expect(response.status).toBe(404);
});

it("returns 404 when fetch throws a network error", async () => {
  vi.stubEnv("BRAND_LOGO_URL", "https://example.com/logo.svg");
  vi.stubGlobal(
    "fetch",
    vi.fn().mockRejectedValue(new Error("Network failure")),
  );
  const response = await GET();
  expect(response.status).toBe(404);
});
