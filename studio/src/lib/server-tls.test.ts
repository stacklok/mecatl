import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  allowPrivateIssuer,
  buildUpstreamTransport,
  resetUpstreamTransportForTests,
  upstreamFetchInit,
  upstreamTransport,
} from "./server-tls";

/**
 * The upstream TLS knobs: no dispatcher (default trust) unless MECATL_TLS_CA
 * or MECATL_TLS_INSECURE is set, the CA can be a path or inline PEM, the
 * two are mutually exclusive (CA wins, conflict reported), an unreadable CA
 * is reported rather than silently ignored, and the insecure warning fires
 * exactly once per process.
 */

const PEM = `-----BEGIN CERTIFICATE-----
MIIBszCCAVoCCQC5kX7Zq9oR2zAKBggqhkjOPQQDAjBFMQswCQYDVQQGEwJBVTET
-----END CERTIFICATE-----
`;

let dir: string;
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "mecatl-tls-"));
  resetUpstreamTransportForTests();
});
afterEach(() => {
  rmSync(dir, { recursive: true, force: true });
  resetUpstreamTransportForTests();
});

describe("buildUpstreamTransport", () => {
  it("builds no dispatcher when neither knob is set", () => {
    const transport = buildUpstreamTransport({});
    expect(transport.dispatcher).toBeUndefined();
    expect(transport.policy).toEqual({
      tlsCa: false,
      insecure: false,
      privateIssuer: false,
    });
  });

  it("builds a CA dispatcher from a PEM file path or inline PEM", () => {
    const path = join(dir, "ca.pem");
    writeFileSync(path, PEM);
    const fromFile = buildUpstreamTransport({ MECATL_TLS_CA: path });
    expect(fromFile.dispatcher).toBeDefined();
    expect(fromFile.policy).toEqual({
      tlsCa: true,
      insecure: false,
      privateIssuer: false,
    });
    const inline = buildUpstreamTransport({ MECATL_TLS_CA: PEM });
    expect(inline.dispatcher).toBeDefined();
    expect(inline.policy.tlsCa).toBe(true);
  });

  it("reports an unreadable or non-PEM CA instead of silently ignoring it", () => {
    const missing = buildUpstreamTransport({
      MECATL_TLS_CA: join(dir, "nope.pem"),
    });
    expect(missing.dispatcher).toBeUndefined();
    expect(missing.policy.problem).toMatch(/could not be read \(ENOENT\)/);
    const path = join(dir, "not-pem.txt");
    writeFileSync(path, "hello");
    const bad = buildUpstreamTransport({ MECATL_TLS_CA: path });
    expect(bad.dispatcher).toBeUndefined();
    expect(bad.policy.problem).toMatch(/does not contain a PEM certificate/);
  });

  it("builds an insecure dispatcher, and lets the CA win when both are set", () => {
    const insecure = buildUpstreamTransport({ MECATL_TLS_INSECURE: "1" });
    expect(insecure.dispatcher).toBeDefined();
    expect(insecure.policy).toEqual({
      tlsCa: false,
      insecure: true,
      privateIssuer: false,
    });
    const both = buildUpstreamTransport({
      MECATL_TLS_CA: PEM,
      MECATL_TLS_INSECURE: "true",
    });
    expect(both.policy.tlsCa).toBe(true);
    expect(both.policy.insecure).toBe(false);
    expect(both.policy.problem).toMatch(/mutually exclusive/);
  });

  it("reads the private-issuer opt-in as part of the policy", () => {
    expect(
      buildUpstreamTransport({ MECATL_OIDC_PRIVATE_ISSUER: "1" }).policy
        .privateIssuer,
    ).toBe(true);
    expect(allowPrivateIssuer({ MECATL_OIDC_PRIVATE_ISSUER: "yes" })).toBe(
      true,
    );
    expect(allowPrivateIssuer({ MECATL_OIDC_PRIVATE_ISSUER: "0" })).toBe(false);
    expect(allowPrivateIssuer({})).toBe(false);
  });
});

describe("upstreamTransport (memoized)", () => {
  it("warns about MECATL_TLS_INSECURE exactly once and reuses the dispatcher", () => {
    const warn = vi.fn();
    const env = { MECATL_TLS_INSECURE: "1" };
    const first = upstreamTransport(env, warn);
    const second = upstreamTransport(env, warn);
    expect(first.dispatcher).toBe(second.dispatcher);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0]?.[0]).toMatch(/verification .* DISABLED/);
    expect(upstreamFetchInit(env)).toEqual({ dispatcher: first.dispatcher });
  });

  it("rebuilds when the env changes and yields an empty init without knobs", () => {
    const warn = vi.fn();
    expect(upstreamFetchInit({})).toEqual({});
    upstreamTransport({ MECATL_TLS_INSECURE: "1" }, warn);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(upstreamTransport({}, warn).dispatcher).toBeUndefined();
    expect(warn).toHaveBeenCalledTimes(1);
  });
});
