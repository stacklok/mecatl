import { resolve } from "node:path";
import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV !== "production";

/**
 * Content Security Policy header.
 * Every daemon/controller call happens server-side through the /api/mecatl*
 * proxy routes, so the browser CSP only needs 'self'.
 */
const cspHeader = `
  default-src 'self';
  script-src 'self' 'unsafe-inline'${isDev ? " 'unsafe-eval'" : ""};
  style-src 'self' 'unsafe-inline';
  img-src 'self' blob: data:;
  font-src 'self';
  connect-src 'self';
  form-action 'self';
  frame-ancestors 'none';
  base-uri 'self';
  object-src 'none';
  ${process.env.NODE_ENV === "production" ? "upgrade-insecure-requests;" : ""}
`
  .replace(/\s{2,}/g, " ")
  .trim();

/**
 * Hostnames allowed to request dev-server assets, derived from the same
 * MECATL_STUDIO_PUBLIC_ORIGIN allowlist the API proxy trusts — one knob for
 * LAN access. Without this, Next's dev cross-origin protection 403s the
 * /_next/* chunks for a non-localhost Host, so the page renders but never
 * hydrates. Dev-only: `next start` has no such gate.
 */
const allowedDevOrigins = (process.env.MECATL_STUDIO_PUBLIC_ORIGIN || "")
  .split(",")
  .map((origin) => origin.trim())
  .filter(Boolean)
  .map((origin) => {
    try {
      return new URL(origin).hostname;
    } catch {
      return origin;
    }
  });

const nextConfig: NextConfig = {
  reactCompiler: true,
  poweredByHeader: false,
  // The TypeScript SDK is a `file:../sdk/typescript` dependency — npm links it
  // as a symlink pointing OUTSIDE studio/. Turbopack resolves modules only
  // under its root, so the root is the monorepo checkout, not studio/.
  turbopack: { root: resolve(import.meta.dirname, "..") },
  ...(allowedDevOrigins.length > 0 ? { allowedDevOrigins } : {}),
  async headers() {
    return [
      {
        source: "/(.*)",
        headers: [
          { key: "Content-Security-Policy", value: cspHeader },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "X-Frame-Options", value: "DENY" },
          { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
          {
            key: "Permissions-Policy",
            value: "camera=(), microphone=(), geolocation=()",
          },
          { key: "Cross-Origin-Resource-Policy", value: "same-origin" },
        ],
      },
    ];
  },
};

export default nextConfig;
