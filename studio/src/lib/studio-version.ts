/**
 * Studio's plain version numbers for Settings → Help & about — the web
 * analogue of the version line `mecatui --version` prints. `studioBuild()`
 * (studio-build.ts) is the bug-report STAMP (`<version>+<sha>`); these are
 * the two package versions on their own: Studio's `package.json` and the
 * TypeScript SDK's (`@stacklok-oss/mecatl-sdk`) that Studio reaches the
 * daemon through.
 *
 * Both are inlined at `next build` from next.config.ts's `env` block, so
 * `next start` names the versions of the build it serves. Unset (a unit
 * test, or a build whose tree had no SDK package) reads "unknown" — never
 * an empty cell.
 */

const UNKNOWN = "unknown";

/** Studio's own package version, e.g. `0.1.0`. */
export function studioVersion(): string {
  // Next inlines the literal `process.env.NEXT_PUBLIC_*` expression; keep it
  // spelled out so both the server and the client bundle see the value.
  const version = (process.env.NEXT_PUBLIC_STUDIO_VERSION ?? "").trim();
  return version === "" ? UNKNOWN : version;
}

/** The version of the TypeScript SDK this build was compiled against. */
export function sdkVersion(): string {
  const version = (process.env.NEXT_PUBLIC_SDK_VERSION ?? "").trim();
  return version === "" ? UNKNOWN : version;
}
