import { execSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV !== "production";

/**
 * Studio's client build stamp — the web analogue of mecatui's linker-stamped
 * `buildinfo.BuildID`, shown on Settings → Provider → About and printed on
 * the `/diagnostics` report's `client build:` line (src/lib/studio-build.ts).
 *
 * `MECATL_STUDIO_BUILD` wins when the build environment sets it (a CI image
 * naming its release); otherwise `<package.json version>+<short git sha>`,
 * or `<version>+dev` when the build tree has no `.git` (a source tarball).
 * It is inlined at `next build` via the `env` block below, so `next start`
 * reports the stamp of the build it serves — a bug report wants the build,
 * not the checkout that happens to be running it.
 */
function studioBuildId(): string {
  const explicit = (process.env.MECATL_STUDIO_BUILD ?? "").trim();
  if (explicit) return explicit;
  let version = "0.0.0";
  try {
    const pkg = JSON.parse(
      readFileSync(resolve(import.meta.dirname, "package.json"), "utf8"),
    ) as { version?: string };
    if (pkg.version) version = pkg.version;
  } catch {
    // keep the placeholder version
  }
  let sha = "dev";
  try {
    sha = execSync("git rev-parse --short HEAD", {
      cwd: import.meta.dirname,
      stdio: ["ignore", "pipe", "ignore"],
    })
      .toString()
      .trim();
  } catch {
    // no .git in the build tree
  }
  return `${version}+${sha || "dev"}`;
}

/**
 * The first readable `version` among candidate package.json paths, or "".
 * Settings → Help & about shows Studio's own version and the TypeScript
 * SDK's (`src/lib/studio-version.ts`); the SDK is read from the INSTALLED
 * package first (the `file:` link in the monorepo, a real copy elsewhere)
 * and from the monorepo source as the fallback, so a build tree without
 * `../sdk` still names the SDK it compiled against.
 */
function packageVersion(...candidates: string[]): string {
  for (const candidate of candidates) {
    try {
      const pkg = JSON.parse(readFileSync(candidate, "utf8")) as {
        version?: string;
      };
      if (pkg.version) return pkg.version;
    } catch {
      // try the next candidate
    }
  }
  return "";
}

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
  // Inlined into both bundles at build time; read via `studioBuild()`,
  // `studioVersion()` and `sdkVersion()` (Settings → Help & about).
  env: {
    NEXT_PUBLIC_STUDIO_BUILD: studioBuildId(),
    NEXT_PUBLIC_STUDIO_VERSION: packageVersion(
      resolve(import.meta.dirname, "package.json"),
    ),
    NEXT_PUBLIC_SDK_VERSION: packageVersion(
      resolve(
        import.meta.dirname,
        "node_modules/@stacklok-oss/mecatl-sdk/package.json",
      ),
      resolve(import.meta.dirname, "../sdk/typescript/package.json"),
    ),
  },
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
