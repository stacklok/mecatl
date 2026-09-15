export function isLoopbackHost(hostname) {
  const normalized = hostname.replace(/^\[|\]$/g, "").toLowerCase();
  return (
    normalized === "localhost" ||
    normalized === "127.0.0.1" ||
    normalized === "::1"
  );
}

export function requestIsAllowed(
  request,
  requestURL,
  { allowedOrigins, mcpProxyPrefix },
) {
  let host;
  try {
    host = new URL(`http://${request.headers.host || ""}`).hostname;
  } catch {
    return false;
  }
  if (!isLoopbackHost(host)) return false;
  const origin = request.headers.origin;
  if (origin && !allowedOrigins.has(origin)) return false;

  const callback =
    request.method === "GET" && requestURL.pathname === "/oauth/callback";
  const internalMCP = requestURL.pathname.startsWith(mcpProxyPrefix);
  const readOnly =
    request.method === "GET" &&
    ["/status", "/model-router"].includes(requestURL.pathname);
  return (
    callback ||
    internalMCP ||
    readOnly ||
    request.headers["x-mecatl-studio-request"] === "1"
  );
}

/**
 * The daemon's skill activation-name grammar, mirrored byte-for-byte from
 * `engine/adapter/skillfs/name.go` (`ValidSkillName`): lowercase
 * letters/digits/underscore/hyphen, 1-64 chars, starting with a lowercase
 * letter or digit. Names under this grammar cannot contain a path separator,
 * a dot, or whitespace, so a valid name is safe to join onto the pinned
 * skills directory — the controller AND the browser client validate through
 * this ONE regex so the two tiers cannot drift.
 *
 * @param {string} name
 * @returns {boolean}
 */
export function validSkillName(name) {
  return /^[a-z0-9][a-z0-9_-]{0,63}$/.test(name);
}

export function validateGatewayURL(value, { allowLoopbackHTTP = false } = {}) {
  const parsed = new URL(value);
  if (parsed.username || parsed.password)
    throw new Error("Gateway URLs must not contain credentials");
  if (parsed.protocol === "https:") return parsed;
  if (
    parsed.protocol === "http:" &&
    isLoopbackHost(parsed.hostname) &&
    allowLoopbackHTTP
  )
    return parsed;
  throw new Error(
    "Gateway URLs must use HTTPS. Loopback HTTP requires MECATL_ALLOW_INSECURE_LOOPBACK_MCP=1 on the controller.",
  );
}
