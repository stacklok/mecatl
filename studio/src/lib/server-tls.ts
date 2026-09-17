/**
 * Upstream TLS policy for Studio's server tier — the `mecatui connect
 * --tls-ca` / `--insecure` / `login --private-issuer` analogues as deployment
 * env, applied to every server-side fetch that leaves this process for the
 * daemon or the identity provider (the same-origin proxy's `forward()`, the
 * OIDC discovery / token / revocation calls, RFC 9728 discovery):
 *
 * - `MECATL_TLS_CA`            — a PEM bundle (a file path, or the PEM text
 *                                inline) trusted for those connections IN
 *                                ADDITION to nothing else: a private CA
 *                                replaces the default roots, exactly like
 *                                `--tls-ca`.
 * - `MECATL_TLS_INSECURE=1`    — disables certificate verification. A
 *                                one-time stderr warning at first use and an
 *                                amber note in Settings; mutually exclusive
 *                                with the CA (the CA wins, verification stays
 *                                on, and the conflict is reported).
 * - `MECATL_OIDC_PRIVATE_ISSUER=1` — allows plain-HTTP loopback / RFC 1918
 *                                deployments and issuers in the OIDC modules
 *                                (read here so the three knobs report as one
 *                                transport policy).
 *
 * The scheme itself (`--tls`) is MECATL_BASE_URL's. The dispatcher is an npm
 * `undici` Agent handed to Node's global fetch as `dispatcher`; Next's fetch
 * patch spreads `init` through, and the hermetic suite proves the CA path
 * end to end against a TLS fake upstream.
 */
import { readFileSync } from "node:fs";
import { Agent, type Dispatcher } from "undici";

export type TransportPolicy = {
  tlsCa: boolean;
  insecure: boolean;
  privateIssuer: boolean;
  /** A configuration conflict or an unreadable CA bundle. */
  problem?: string;
};

/** The env slice read here (`process.env`-shaped so it can be passed as is):
 * MECATL_TLS_CA, MECATL_TLS_INSECURE, MECATL_OIDC_PRIVATE_ISSUER. */
export type TransportEnv = Record<string, string | undefined>;

export type UpstreamTransport = {
  policy: TransportPolicy;
  /** Undefined when the default trust store applies (byte-identical to the
   * pre-knob behaviour). */
  dispatcher?: Dispatcher;
};

const flag = (value: string | undefined) =>
  ["1", "true", "yes"].includes((value ?? "").trim().toLowerCase());

export function allowPrivateIssuer(env: TransportEnv = process.env): boolean {
  return flag(env.MECATL_OIDC_PRIVATE_ISSUER);
}

function readCaBundle(value: string): string {
  if (value.includes("-----BEGIN")) return value;
  return readFileSync(value, "utf8");
}

/** Build the transport for one env reading (no memoization, no warning). */
export function buildUpstreamTransport(
  env: TransportEnv = process.env,
): UpstreamTransport {
  const ca = (env.MECATL_TLS_CA ?? "").trim();
  const insecureRequested = flag(env.MECATL_TLS_INSECURE);
  const policy: TransportPolicy = {
    tlsCa: false,
    insecure: false,
    privateIssuer: allowPrivateIssuer(env),
  };
  if (ca) {
    let bundle: string;
    try {
      bundle = readCaBundle(ca);
    } catch (error) {
      const code = (error as NodeJS.ErrnoException)?.code;
      policy.problem = `MECATL_TLS_CA could not be read${code ? ` (${code})` : ""}; the default trust store applies.`;
      return { policy };
    }
    if (!bundle.includes("-----BEGIN CERTIFICATE-----")) {
      policy.problem =
        "MECATL_TLS_CA does not contain a PEM certificate; the default trust store applies.";
      return { policy };
    }
    policy.tlsCa = true;
    if (insecureRequested)
      policy.problem =
        "MECATL_TLS_CA and MECATL_TLS_INSECURE are mutually exclusive; the CA bundle is used and verification stays on.";
    return { policy, dispatcher: new Agent({ connect: { ca: bundle } }) };
  }
  if (insecureRequested) {
    policy.insecure = true;
    return {
      policy,
      dispatcher: new Agent({ connect: { rejectUnauthorized: false } }),
    };
  }
  return { policy };
}

type TransportCache = { signature: string; transport: UpstreamTransport };

const globalStash = globalThis as typeof globalThis & {
  __mecatlUpstreamTransport?: TransportCache | null;
};

const signatureOf = (env: TransportEnv) =>
  JSON.stringify([
    env.MECATL_TLS_CA ?? "",
    env.MECATL_TLS_INSECURE ?? "",
    env.MECATL_OIDC_PRIVATE_ISSUER ?? "",
  ]);

/**
 * The process-wide transport (memoized on globalThis so Next dev reloads
 * neither rebuild the Agent nor repeat the warning). The insecure warning
 * fires ONCE, at the first build of an insecure transport.
 */
export function upstreamTransport(
  env: TransportEnv = process.env,
  warn: (message: string) => void = (message) => console.warn(message),
): UpstreamTransport {
  const signature = signatureOf(env);
  const cached = globalStash.__mecatlUpstreamTransport;
  if (cached && cached.signature === signature) return cached.transport;
  const transport = buildUpstreamTransport(env);
  if (transport.policy.insecure)
    warn(
      "[mecatl-studio] MECATL_TLS_INSECURE=1: TLS certificate verification for the daemon and identity-provider connections is DISABLED. Use only on a trusted network.",
    );
  if (transport.policy.problem)
    warn(`[mecatl-studio] ${transport.policy.problem}`);
  globalStash.__mecatlUpstreamTransport = { signature, transport };
  return transport;
}

/** The `fetch` init fragment every upstream call spreads in. */
export function upstreamFetchInit(env: TransportEnv = process.env): {
  dispatcher?: Dispatcher;
} {
  const { dispatcher } = upstreamTransport(env);
  return dispatcher ? { dispatcher } : {};
}

/** Test seam: forget the memoized transport. */
export function resetUpstreamTransportForTests(): void {
  globalStash.__mecatlUpstreamTransport = null;
}
