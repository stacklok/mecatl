/**
 * RFC 9728 protected-resource discovery for Studio's server tier — the web
 * analogue of `mecatui login ADDRESS` (cmd/mecatui/resource_discovery.go),
 * ported field for field: the deployment's
 * `/.well-known/oauth-protected-resource` names its authorization server and
 * the Stacklok profile extensions (`com.stacklok.mecatl.client_id`,
 * `com.stacklok.mecatl.audience`, `scopes_supported`), and the issuer's own
 * `/.well-known/openid-configuration` must echo that issuer EXACTLY.
 *
 * Pure and fetch-injected (no process state, no env reads) so every rejection
 * below is unit-testable offline. The discipline mirrors the TUI's bootstrap
 * client: anonymous requests only, ANY redirect rejects (a well-known URI has
 * no legitimate reason to redirect), bodies are bounded at 1 MiB, JSON media
 * type required, duplicate security keys reject (JSON.parse would silently
 * keep the last one), and every error message is harness-authored — a value
 * the daemon or issuer served is never echoed into an error.
 */
import { createHash } from "node:crypto";

export const MAX_DISCOVERY_BODY_BYTES = 1 << 20;
const DISCOVERY_TIMEOUT_MS = 15_000;
/** mecatui's `defaultOIDCScopes` for a profile that advertises none. */
export const DEFAULT_DISCOVERED_SCOPES = [
  "openid",
  "profile",
  "offline_access",
];

export class DiscoveryError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "DiscoveryError";
  }
}

export type DiscoveredProfile = {
  /** The canonical RFC 9728 resource identity (server-side only — never
   * rendered; the browser never sees a daemon address). */
  resource: string;
  /** Exact issuer spelling from the document (RFC 8414 compares exactly). */
  issuer: string;
  clientId: string;
  audience: string;
  scopes: string[];
  /** false when the document had no `scopes_supported` and the default set
   * stands. */
  scopesDiscovered: boolean;
};

/** The structural slice of `fetch` the discovery needs, so tests inject a
 * fake and the server tier can pass its TLS dispatcher through. */
type DiscoveryFetch = (
  url: string,
  init: RequestInit & { dispatcher?: unknown },
) => Promise<Response>;

export type DiscoveryOptions = {
  fetchImpl?: DiscoveryFetch;
  /** MECATL_OIDC_PRIVATE_ISSUER=1: allow plain-HTTP loopback / RFC 1918
   * deployments and issuers (the `--private-issuer` analogue). */
  allowPrivateHttp?: boolean;
  /** An undici dispatcher (custom CA / insecure) threaded into every fetch. */
  dispatcher?: unknown;
  timeoutMs?: number;
};

const reject = (why: string) =>
  new DiscoveryError(`Discovery rejected: ${why}`);

const PRIVATE_V4 = [
  [0x7f000000, 0xff000000], // 127.0.0.0/8
  [0x0a000000, 0xff000000], // 10.0.0.0/8
  [0xac100000, 0xfff00000], // 172.16.0.0/12
  [0xc0a80000, 0xffff0000], // 192.168.0.0/16
] as const;

function parseV4(host: string): number | null {
  const parts = host.split(".");
  if (parts.length !== 4) return null;
  let value = 0;
  for (const part of parts) {
    if (!/^\d{1,3}$/.test(part)) return null;
    const octet = Number(part);
    if (octet > 255) return null;
    value = value * 256 + octet;
  }
  return value >>> 0;
}

/** Loopback, `localhost`, RFC 1918 and IPv6 loopback / unique-local hosts —
 * the only hosts a plain-HTTP deployment or issuer may live on under the
 * private-issuer opt-in. */
export function isPrivateHost(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/^\[|\]$/g, "");
  if (host === "localhost" || host.endsWith(".localhost")) return true;
  const v4 = parseV4(host);
  if (v4 !== null) {
    return PRIVATE_V4.some(([net, mask]) => (v4 & mask) >>> 0 === net);
  }
  if (host === "::1") return true;
  if (/^f[cd][0-9a-f]{2}:/i.test(host)) return true; // fc00::/7
  return false;
}

function isIpLiteral(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/g, "");
  return parseV4(host) !== null || host.includes(":");
}

/** RFC 1123 host-name labels (the TUI's `validDNSName`). */
function validDnsName(host: string): boolean {
  if (!host || host.length > 253 || host.endsWith(".")) return false;
  return host
    .split(".")
    .every(
      (label) =>
        label.length > 0 &&
        label.length <= 63 &&
        /^[A-Za-z0-9-]+$/.test(label) &&
        !label.startsWith("-") &&
        !label.endsWith("-"),
    );
}

/** The TUI's `safeDisplayValue`: non-empty, at most 1024 characters, and
 * nothing that is not printable (controls, format characters, separators
 * other than the ASCII space, surrogates, unassigned). */
export function safeDisplayValue(value: unknown): value is string {
  if (typeof value !== "string" || value === "" || value.length > 1024)
    return false;
  if (/[\p{C}\u2028\u2029]/u.test(value)) return false;
  return !/\p{Z}/u.test(value.replaceAll(" ", ""));
}

/** RFC 6749 section 3.3 scope-token grammar. */
export function validScopeToken(scope: unknown): scope is string {
  return typeof scope === "string" && /^[\x21\x23-\x5B\x5D-\x7E]+$/.test(scope);
}

function schemeAllowed(url: URL, allowPrivateHttp: boolean): boolean {
  if (url.protocol === "https:") return true;
  return (
    url.protocol === "http:" && allowPrivateHttp && isPrivateHost(url.hostname)
  );
}

/**
 * The canonical resource identity of the deployment's base URL (the TUI's
 * `resourceurl.Canonical`): HTTPS (or private plain HTTP under the opt-in),
 * no credentials / query / fragment, lower-case host, default port dropped,
 * the root path spelled as "", no dot segments.
 */
export function canonicalResource(
  baseUrl: string,
  allowPrivateHttp = false,
): string {
  let url: URL;
  try {
    url = new URL(baseUrl.trim());
  } catch {
    throw reject("MECATL_BASE_URL is not a valid URL");
  }
  if (url.username || url.password || url.search || url.hash)
    throw reject(
      "MECATL_BASE_URL must not carry credentials, a query or a fragment",
    );
  if (!schemeAllowed(url, allowPrivateHttp))
    throw reject(
      "MECATL_BASE_URL must use HTTPS (set MECATL_OIDC_PRIVATE_ISSUER=1 for a plain-HTTP loopback or RFC 1918 deployment)",
    );
  if (
    url.pathname
      .split("/")
      .some((segment) => segment === "." || segment === "..")
  )
    throw reject("MECATL_BASE_URL path must not contain dot segments");
  const path = url.pathname === "/" ? "" : url.pathname;
  return `${url.protocol}//${url.host}${path}`;
}

/** RFC 9728 section 3: the metadata URL for a canonical resource. */
export function metadataUrl(resource: string): string {
  const url = new URL(resource);
  const path = url.pathname === "/" ? "" : url.pathname;
  url.pathname = `/.well-known/oauth-protected-resource${path}`;
  return url.toString();
}

/**
 * Validate an issuer identifier the way the TUI's `parseIssuer` does and
 * return it VERBATIM (RFC 8414 compares issuers exactly; validation must not
 * rewrite the spelling): HTTPS, a DNS-name host (an IP literal only under the
 * private opt-in), no credentials / query / fragment / opaque part.
 */
export function validateIssuerUrl(
  raw: unknown,
  allowPrivateHttp = false,
): string {
  if (!safeDisplayValue(raw)) throw reject("issuer is not a safe value");
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw reject("issuer is not a valid URL");
  }
  if (
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    raw.endsWith("?")
  )
    throw reject("issuer must not carry credentials, a query or a fragment");
  if (!schemeAllowed(url, allowPrivateHttp))
    throw reject("issuer must use HTTPS");
  if (isIpLiteral(url.hostname)) {
    if (!allowPrivateHttp || !isPrivateHost(url.hostname))
      throw reject("issuer host must be a DNS name");
  } else if (!validDnsName(url.hostname)) {
    throw reject("issuer host is not a valid DNS name");
  }
  return raw;
}

/** The issuer's OIDC discovery document URL (one trailing slash folded). */
export function oidcMetadataUrl(issuer: string): string {
  return `${issuer.replace(/\/+$/, "")}/.well-known/openid-configuration`;
}

/**
 * The keys of a JSON text's top-level object, in order and WITH duplicates —
 * `JSON.parse` keeps only the last spelling of a repeated key, which is
 * exactly the smuggling an RFC 9728 client must refuse. Assumes the text was
 * already accepted by `JSON.parse` (well-formed), so a tiny scanner suffices.
 */
export function topLevelKeys(raw: string): string[] {
  const keys: string[] = [];
  let depth = 0;
  let expectKey = false;
  let i = 0;
  while (i < raw.length) {
    const ch = raw[i];
    if (ch === '"') {
      const start = i;
      i++;
      while (i < raw.length && raw[i] !== '"') {
        if (raw[i] === "\\") i++;
        i++;
      }
      const literal = raw.slice(start, i + 1);
      i++;
      if (depth === 1 && expectKey) {
        keys.push(JSON.parse(literal) as string);
        expectKey = false;
      }
      continue;
    }
    if (ch === "{" || ch === "[") {
      depth++;
      if (depth === 1 && ch === "{") expectKey = true;
    } else if (ch === "}" || ch === "]") {
      depth--;
    } else if (ch === "," && depth === 1) {
      expectKey = true;
    }
    i++;
  }
  return keys;
}

export function hasDuplicateKeys(
  raw: string,
  fields: Iterable<string>,
): boolean {
  const watched = new Set(fields);
  const seen = new Set<string>();
  for (const key of topLevelKeys(raw)) {
    if (!watched.has(key)) continue;
    if (seen.has(key)) return true;
    seen.add(key);
  }
  return false;
}

function parseObject(raw: string, label: string): Record<string, unknown> {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    throw reject(`${label} is not valid JSON`);
  }
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw reject(`${label} is not a JSON object`);
  return value as Record<string, unknown>;
}

const PROTECTED_RESOURCE_SECURITY_FIELDS = [
  "resource",
  "authorization_servers",
  "com.stacklok.mecatl.audience",
  "com.stacklok.mecatl.client_id",
  "scopes_supported",
];

/** The TUI's `parseProfileDocument`, over the raw text so duplicates can be
 * refused. */
export function parseProtectedResourceDocument(
  raw: string,
  expectedResource: string,
  allowPrivateHttp = false,
): DiscoveredProfile {
  const label = "protected-resource metadata";
  const doc = parseObject(raw, label);
  if (hasDuplicateKeys(raw, PROTECTED_RESOURCE_SECURITY_FIELDS))
    throw reject(`${label} repeats a security field`);
  if (doc.resource !== expectedResource)
    throw reject(`${label} names a different resource than MECATL_BASE_URL`);
  const servers = doc.authorization_servers;
  if (!Array.isArray(servers) || servers.length !== 1)
    throw reject(`${label} must name exactly one authorization server`);
  const issuer = validateIssuerUrl(servers[0], allowPrivateHttp);
  const clientId = doc["com.stacklok.mecatl.client_id"];
  const audience = doc["com.stacklok.mecatl.audience"];
  if (!safeDisplayValue(clientId))
    throw reject(`${label} has no usable com.stacklok.mecatl.client_id`);
  if (!safeDisplayValue(audience))
    throw reject(`${label} has no usable com.stacklok.mecatl.audience`);
  const scopesDiscovered = "scopes_supported" in doc;
  let scopes = DEFAULT_DISCOVERED_SCOPES.slice();
  if (scopesDiscovered) {
    const advertised = doc.scopes_supported;
    if (!Array.isArray(advertised) || advertised.length === 0)
      throw reject(`${label} advertises an empty scopes_supported`);
    const seen = new Set<string>();
    for (const scope of advertised) {
      if (!validScopeToken(scope) || seen.has(scope))
        throw reject(`${label} advertises an invalid or repeated scope`);
      seen.add(scope);
    }
    scopes = advertised as string[];
  }
  return {
    resource: expectedResource,
    issuer,
    clientId,
    audience,
    scopes,
    scopesDiscovered,
  };
}

/** The TUI's `validateIssuerDocument`: the issuer's own discovery document
 * must spell `issuer` exactly as the protected resource named it. */
export function validateIssuerDocument(
  raw: string,
  expectedIssuer: string,
): void {
  const label = "issuer metadata";
  const doc = parseObject(raw, label);
  if (hasDuplicateKeys(raw, ["issuer"]))
    throw reject(`${label} repeats the issuer field`);
  if (!safeDisplayValue(doc.issuer) || doc.issuer !== expectedIssuer)
    throw reject(`${label} does not match the advertised authorization server`);
}

function jsonMediaType(contentType: string | null): boolean {
  const mediaType = (contentType ?? "").split(";", 1)[0]?.trim().toLowerCase();
  return (
    mediaType === "application/json" || Boolean(mediaType?.endsWith("+json"))
  );
}

async function readBoundedText(
  response: Response,
  max: number,
): Promise<string> {
  const declared = Number(response.headers.get("content-length"));
  if (Number.isFinite(declared) && declared > max)
    throw reject("document is too large");
  const body = response.body;
  if (!body) throw reject("document is empty");
  const reader = body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > max) {
      await reader.cancel().catch(() => {});
      throw reject("document is too large");
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw reject("document is not UTF-8");
  }
}

async function fetchDocument(
  url: string,
  options: DiscoveryOptions,
): Promise<string> {
  const fetchImpl: DiscoveryFetch =
    options.fetchImpl ?? ((u, init) => fetch(u, init));
  let response: Response;
  try {
    response = await fetchImpl(url, {
      method: "GET",
      headers: { accept: "application/json" },
      cache: "no-store",
      redirect: "manual",
      signal: AbortSignal.timeout(options.timeoutMs ?? DISCOVERY_TIMEOUT_MS),
      ...(options.dispatcher ? { dispatcher: options.dispatcher } : {}),
    });
  } catch {
    throw reject("the well-known document could not be fetched");
  }
  if (response.status !== 200)
    throw reject(
      response.status >= 300 && response.status < 400
        ? "the well-known URL redirected"
        : `the well-known URL answered HTTP ${response.status}`,
    );
  if (!jsonMediaType(response.headers.get("content-type")))
    throw reject("the well-known document is not JSON");
  return readBoundedText(response, MAX_DISCOVERY_BODY_BYTES);
}

/**
 * Discover the deployment's sign-in profile: canonicalize MECATL_BASE_URL,
 * fetch and validate its protected-resource metadata, then fetch the named
 * issuer's OIDC metadata and require the issuer to match exactly. The result
 * is DEFAULT-DENY for the caller: nothing signs in until a human has reviewed
 * these values (`oidc-session.ts` keeps the confirmation).
 */
export async function discoverProtectedResource(
  baseUrl: string,
  options: DiscoveryOptions = {},
): Promise<DiscoveredProfile> {
  const allowPrivateHttp = options.allowPrivateHttp === true;
  const resource = canonicalResource(baseUrl, allowPrivateHttp);
  const resourceDocument = await fetchDocument(metadataUrl(resource), options);
  const profile = parseProtectedResourceDocument(
    resourceDocument,
    resource,
    allowPrivateHttp,
  );
  const issuerDocument = await fetchDocument(
    oidcMetadataUrl(profile.issuer),
    options,
  );
  validateIssuerDocument(issuerDocument, profile.issuer);
  return profile;
}

/**
 * A stable digest of the identity a login would bind to — issuer, client id,
 * audience and scopes. It is the confirmation token the settings card POSTs
 * back (so a profile that changed between review and click is refused) and
 * the binding a persisted credential carries (so tokens minted for one
 * profile are never refreshed against another).
 */
export function profileHash(profile: {
  source: string;
  issuer: string;
  clientId: string;
  audience: string;
  scope: string;
}): string {
  return createHash("sha256")
    .update(
      JSON.stringify([
        "mecatl-studio-oidc-profile/1",
        profile.source,
        profile.issuer,
        profile.clientId,
        profile.audience,
        profile.scope,
      ]),
    )
    .digest("hex");
}
