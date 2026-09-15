/**
 * Origin trust for the Next server tier's API routes (CLAUDE.md rule 4's
 * browser-facing half): a request is served only when it arrived at an
 * allowlisted Studio origin (Host — the DNS-rebinding check) AND, when the
 * browser attached one, from an allowlisted Origin (the CSRF check).
 *
 * Extracted from `server-proxy.ts` so the OIDC auth routes share the exact
 * same table instead of growing a second, drifting copy — and so the check is
 * testable without importing a `server-only` module.
 */

/** The structural slice of `Request` the check reads — lets tests use plain
 * objects (undici's `Request` silently drops forbidden headers like `host`,
 * which would make a rebinding test vacuous). */
export type TrustCheckedRequest = {
  url: string;
  headers: { get(name: string): string | null };
};

export function studioAllowedOrigins(
  configured = process.env.MECATL_STUDIO_PUBLIC_ORIGIN,
): Set<string> {
  return new Set(
    (configured || "http://localhost:3000,http://127.0.0.1:3000")
      .split(",")
      .map((origin) => origin.trim().replace(/\/$/, ""))
      .filter(Boolean),
  );
}

export function requestIsTrusted(
  request: TrustCheckedRequest,
  allowed: Set<string> = studioAllowedOrigins(),
): boolean {
  const requestURL = new URL(request.url);
  const host = request.headers.get("host") || requestURL.host;
  const forwardedProtocol = request.headers
    .get("x-forwarded-proto")
    ?.split(",", 1)[0]
    ?.trim();
  const protocol =
    forwardedProtocol === "https" || forwardedProtocol === "http"
      ? `${forwardedProtocol}:`
      : requestURL.protocol;
  const requestOrigin = `${protocol}//${host}`;
  const browserOrigin = request.headers.get("origin");
  return (
    allowed.has(requestOrigin) && (!browserOrigin || allowed.has(browserOrigin))
  );
}
