/**
 * Studio's OWN client identity for bug reports — the web analogue of
 * `mecatui --version` (internal/buildinfo prints the CLIENT binary's stamp;
 * Studio's About card and `/diagnostics` report print this one next to the
 * daemon's).
 *
 * The build stamp is inlined at `next build` from next.config.ts's `env`
 * block: `NEXT_PUBLIC_STUDIO_BUILD` = `MECATL_STUDIO_BUILD` when the build
 * environment sets it (CI images), else `<package.json version>+<short git
 * sha>`, else "dev" (no .git in the build tree). It is a BUILD-time fact:
 * `next start` in a different checkout still reports the stamp of the build
 * it serves, which is exactly what a bug report wants.
 *
 * The platform token is a coarse OS + browser-family hint (major version at
 * most). It is a report token for triage, not a fingerprint — nothing finer
 * than what the user agent already announces on every request.
 */

/** What the stamp reads when the build did not set one. */
export const UNKNOWN_BUILD = "dev";

/** The inlined build stamp, or {@link UNKNOWN_BUILD}. */
export function studioBuild(): string {
  // Next inlines the literal `process.env.NEXT_PUBLIC_*` expression; keep it
  // spelled out so both the server and the client bundle see the value.
  const stamp = (process.env.NEXT_PUBLIC_STUDIO_BUILD ?? "").trim();
  return stamp === "" ? UNKNOWN_BUILD : stamp;
}

/** The subset of `navigator` the platform token reads. */
export interface PlatformSource {
  userAgent?: string;
  platform?: string;
  userAgentData?: { platform?: string };
}

/** Recognised families, in the order the user-agent string must be tested
 *  (Chromium-based browsers all carry `Chrome/`; Safari carries `Safari/`
 *  and so does Chrome). */
const FAMILIES: readonly [family: string, marker: RegExp][] = [
  ["Edge", /\bEdg(?:e|A|iOS)?\/(\d+)/],
  ["Opera", /\bOPR\/(\d+)/],
  ["Firefox", /\b(?:Firefox|FxiOS)\/(\d+)/],
  ["Chrome", /\b(?:Chrome|CriOS)\/(\d+)/],
  ["Safari", /\bVersion\/(\d+)[^ ]* .*\bSafari\//],
];

/**
 * A coarse browser family with its major version (`Chrome-128`), or
 * "browser" when the user agent names none Studio recognises.
 */
export function browserFamily(userAgent: string): string {
  for (const [family, marker] of FAMILIES) {
    const match = marker.exec(userAgent);
    if (match) return match[1] ? `${family}-${match[1]}` : family;
  }
  return "browser";
}

/**
 * Restricts a token to the report's alphabet (`[A-Za-z0-9._/+-]`): every
 * other run of characters becomes one `-` so "Linux x86_64" survives the
 * report's sanitizer instead of reading "unavailable".
 */
function reportSafe(value: string): string {
  return value
    .replace(/[^A-Za-z0-9._/+-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
}

/**
 * The platform token for the diagnostics report: `<os>/<browser>`, e.g.
 * `macOS/Chrome-128` or `Linux-x86_64/Firefox-130`. Reads
 * `navigator.userAgentData.platform` (Chromium's coarse OS name) before the
 * legacy `navigator.platform`; "browser" when nothing is known (SSR, tests).
 */
export function studioPlatform(
  source: PlatformSource | undefined = typeof navigator === "undefined"
    ? undefined
    : (navigator as PlatformSource),
): string {
  if (!source) return "browser";
  const os = reportSafe(
    source.userAgentData?.platform?.trim() || source.platform?.trim() || "",
  );
  const family = browserFamily(source.userAgent ?? "");
  return os ? `${os}/${family}` : family;
}
