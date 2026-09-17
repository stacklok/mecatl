import { describe, expect, it } from "vitest";
import {
  canonicalResource,
  DEFAULT_DISCOVERED_SCOPES,
  DiscoveryError,
  discoverProtectedResource,
  hasDuplicateKeys,
  isPrivateHost,
  MAX_DISCOVERY_BODY_BYTES,
  metadataUrl,
  oidcMetadataUrl,
  parseProtectedResourceDocument,
  profileHash,
  safeDisplayValue,
  topLevelKeys,
  validateIssuerDocument,
  validateIssuerUrl,
  validScopeToken,
} from "./protected-resource";

/**
 * RFC 9728 discovery mirrors cmd/mecatui/resource_discovery.go field for
 * field: the five profile fields, exact resource + issuer matching,
 * duplicate-key refusal, no redirects, JSON media type, the 1 MiB bound, and
 * HTTPS unless the private-issuer opt-in admits a loopback / RFC 1918 host.
 */

const RESOURCE = "https://mecated.example.com:8443";
const ISSUER = "https://idp.example.com/realms/mecatl";

const goodResource = (over: Record<string, unknown> = {}) =>
  JSON.stringify({
    resource: RESOURCE,
    authorization_servers: [ISSUER],
    "com.stacklok.mecatl.client_id": "studio-client",
    "com.stacklok.mecatl.audience": "mecatl-daemon",
    scopes_supported: ["openid", "profile", "offline_access"],
    ...over,
  });

const goodIssuer = (issuer = ISSUER) =>
  JSON.stringify({
    issuer,
    authorization_endpoint: `${issuer}/protocol/openid-connect/auth`,
    token_endpoint: `${issuer}/protocol/openid-connect/token`,
  });

type Route = { status?: number; body: string; contentType?: string };

/** A fake fetch keyed by URL; records what was requested. */
function fakeFetch(routes: Record<string, Route>) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchImpl = async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    const route = routes[url];
    if (!route) return new Response("{}", { status: 404 });
    return new Response(route.body, {
      status: route.status ?? 200,
      headers: { "content-type": route.contentType ?? "application/json" },
    });
  };
  return { fetchImpl, calls };
}

const rejects = async (promise: Promise<unknown>, pattern: RegExp) => {
  await expect(promise).rejects.toBeInstanceOf(DiscoveryError);
  await expect(promise).rejects.toThrow(pattern);
};

describe("canonicalResource", () => {
  it("lower-cases the host, drops the default port and the root slash", () => {
    expect(canonicalResource("https://Mecated.Example.com:443/")).toBe(
      "https://mecated.example.com",
    );
    expect(canonicalResource("https://mecated.example.com:8443/api/")).toBe(
      "https://mecated.example.com:8443/api/",
    );
  });

  it("requires HTTPS unless the private opt-in admits a loopback / RFC 1918 host", () => {
    expect(() => canonicalResource("http://mecated.example.com")).toThrow(
      /must use HTTPS/,
    );
    expect(() => canonicalResource("http://mecated.example.com", true)).toThrow(
      /must use HTTPS/,
    );
    expect(canonicalResource("http://127.0.0.1:8081", true)).toBe(
      "http://127.0.0.1:8081",
    );
    expect(canonicalResource("http://10.1.2.3/", true)).toBe("http://10.1.2.3");
  });

  it("refuses credentials, queries, fragments and dot segments", () => {
    expect(() =>
      canonicalResource("https://u:p@mecated.example.com"),
    ).toThrow();
    expect(() =>
      canonicalResource("https://mecated.example.com/?x=1"),
    ).toThrow();
    expect(() => canonicalResource("https://mecated.example.com/#f")).toThrow();
    expect(() => canonicalResource("not a url")).toThrow(/not a valid URL/);
  });
});

describe("isPrivateHost", () => {
  it("classifies loopback, localhost, RFC 1918 and IPv6 private ranges", () => {
    for (const host of [
      "127.0.0.1",
      "127.9.9.9",
      "localhost",
      "api.localhost",
      "10.0.0.5",
      "172.16.0.1",
      "172.31.255.255",
      "192.168.1.1",
      "::1",
      "[::1]",
      "fd12:3456::1",
    ])
      expect(isPrivateHost(host), host).toBe(true);
    for (const host of [
      "8.8.8.8",
      "172.32.0.1",
      "169.254.169.254",
      "mecated.example.com",
      "2001:db8::1",
    ])
      expect(isPrivateHost(host), host).toBe(false);
  });
});

describe("metadata URLs", () => {
  it("places the RFC 9728 well-known path before the resource path", () => {
    expect(metadataUrl("https://mecated.example.com")).toBe(
      "https://mecated.example.com/.well-known/oauth-protected-resource",
    );
    expect(metadataUrl("https://mecated.example.com:8443/api")).toBe(
      "https://mecated.example.com:8443/.well-known/oauth-protected-resource/api",
    );
    expect(oidcMetadataUrl("https://idp.example.com/realms/x/")).toBe(
      "https://idp.example.com/realms/x/.well-known/openid-configuration",
    );
  });
});

describe("validateIssuerUrl", () => {
  it("returns the exact spelling and demands an HTTPS DNS-name authority", () => {
    expect(validateIssuerUrl("https://idp.example.com/realms/x/")).toBe(
      "https://idp.example.com/realms/x/",
    );
    expect(() => validateIssuerUrl("http://idp.example.com")).toThrow(/HTTPS/);
    expect(() => validateIssuerUrl("https://1.2.3.4")).toThrow(/DNS name/);
    expect(() => validateIssuerUrl("https://idp.example.com/?a=1")).toThrow();
    expect(() => validateIssuerUrl("https://idp.example.com/#x")).toThrow();
    expect(() => validateIssuerUrl("https://u@idp.example.com")).toThrow();
    expect(() => validateIssuerUrl("https://-bad-.example.com")).toThrow();
    expect(() => validateIssuerUrl("")).toThrow(/safe value/);
    expect(() => validateIssuerUrl("https://idp.example.com/\u0000")).toThrow();
  });

  it("admits a plain-HTTP loopback issuer only under the private opt-in", () => {
    expect(validateIssuerUrl("http://127.0.0.1:9000", true)).toBe(
      "http://127.0.0.1:9000",
    );
    expect(() => validateIssuerUrl("http://127.0.0.1:9000")).toThrow();
    expect(() => validateIssuerUrl("http://8.8.8.8", true)).toThrow();
  });
});

describe("safeDisplayValue / validScopeToken", () => {
  it("refuses controls, format characters, separators and oversize", () => {
    expect(safeDisplayValue("studio-client")).toBe(true);
    expect(safeDisplayValue("with space ok")).toBe(true);
    expect(safeDisplayValue("")).toBe(false);
    expect(safeDisplayValue("a\nb")).toBe(false);
    expect(safeDisplayValue("a\u200bb")).toBe(false);
    expect(safeDisplayValue("a\u2028b")).toBe(false);
    expect(safeDisplayValue("a\u00a0b")).toBe(false);
    expect(safeDisplayValue("x".repeat(1025))).toBe(false);
    expect(safeDisplayValue(42)).toBe(false);
  });

  it("scope tokens follow RFC 6749 section 3.3", () => {
    expect(validScopeToken("openid")).toBe(true);
    expect(validScopeToken("urn:x:y")).toBe(true);
    expect(validScopeToken("open id")).toBe(false);
    expect(validScopeToken('a"b')).toBe(false);
    expect(validScopeToken("a\\b")).toBe(false);
    expect(validScopeToken("")).toBe(false);
  });
});

describe("duplicate-key detection", () => {
  it("lists top-level keys with duplicates, ignoring nested objects", () => {
    expect(
      topLevelKeys(
        '{"a":1,"b":{"a":2,"c":[{"a":3}]},"a":"x\\"y","d":[1,{"e":5}]}',
      ),
    ).toEqual(["a", "b", "a", "d"]);
    expect(
      hasDuplicateKeys('{"resource":"x","other":1,"resource":"y"}', [
        "resource",
      ]),
    ).toBe(true);
    expect(
      hasDuplicateKeys('{"resource":"x","other":1,"other":2}', ["resource"]),
    ).toBe(false);
  });
});

describe("parseProtectedResourceDocument", () => {
  it("reads the five fields", () => {
    expect(parseProtectedResourceDocument(goodResource(), RESOURCE)).toEqual({
      resource: RESOURCE,
      issuer: ISSUER,
      clientId: "studio-client",
      audience: "mecatl-daemon",
      scopes: ["openid", "profile", "offline_access"],
      scopesDiscovered: true,
    });
  });

  it("falls back to the TUI's default scopes when none are advertised", () => {
    const doc = JSON.parse(goodResource()) as Record<string, unknown>;
    delete doc.scopes_supported;
    const profile = parseProtectedResourceDocument(
      JSON.stringify(doc),
      RESOURCE,
    );
    expect(profile.scopes).toEqual(DEFAULT_DISCOVERED_SCOPES);
    expect(profile.scopesDiscovered).toBe(false);
  });

  it("rejects a repeated security key even when JSON.parse would accept it", () => {
    const raw = goodResource().replace(
      '"resource":',
      `"resource":"https://evil.example",\n"resource":`,
    );
    expect(() => parseProtectedResourceDocument(raw, RESOURCE)).toThrow(
      /repeats a security field/,
    );
  });

  it("rejects a resource that is not MECATL_BASE_URL", () => {
    expect(() =>
      parseProtectedResourceDocument(
        goodResource({ resource: "https://other.example.com" }),
        RESOURCE,
      ),
    ).toThrow(/different resource/);
  });

  it("rejects anything but exactly one valid authorization server", () => {
    expect(() =>
      parseProtectedResourceDocument(
        goodResource({ authorization_servers: [ISSUER, "https://b.example"] }),
        RESOURCE,
      ),
    ).toThrow(/exactly one/);
    expect(() =>
      parseProtectedResourceDocument(
        goodResource({ authorization_servers: ["http://idp.example.com"] }),
        RESOURCE,
      ),
    ).toThrow(/HTTPS/);
  });

  it("rejects a missing or unsafe client id / audience and bad scopes", () => {
    expect(() =>
      parseProtectedResourceDocument(
        goodResource({ "com.stacklok.mecatl.client_id": "" }),
        RESOURCE,
      ),
    ).toThrow(/client_id/);
    expect(() =>
      parseProtectedResourceDocument(
        goodResource({ "com.stacklok.mecatl.audience": "a\u0007b" }),
        RESOURCE,
      ),
    ).toThrow(/audience/);
    expect(() =>
      parseProtectedResourceDocument(
        goodResource({ scopes_supported: [] }),
        RESOURCE,
      ),
    ).toThrow(/empty scopes_supported/);
    expect(() =>
      parseProtectedResourceDocument(
        goodResource({ scopes_supported: ["openid", "openid"] }),
        RESOURCE,
      ),
    ).toThrow(/repeated scope/);
    expect(() => parseProtectedResourceDocument("[1,2]", RESOURCE)).toThrow(
      /not a JSON object/,
    );
  });

  it("never echoes a served value into the error", () => {
    let message = "";
    try {
      parseProtectedResourceDocument(
        goodResource({ resource: "https://attacker.example/<script>" }),
        RESOURCE,
      );
    } catch (error) {
      message = (error as Error).message;
    }
    expect(message).not.toContain("attacker");
  });
});

describe("validateIssuerDocument", () => {
  it("requires the exact issuer spelling and refuses a duplicate issuer key", () => {
    expect(() => validateIssuerDocument(goodIssuer(), ISSUER)).not.toThrow();
    expect(() =>
      validateIssuerDocument(goodIssuer(`${ISSUER}/`), ISSUER),
    ).toThrow(/does not match/);
    expect(() =>
      validateIssuerDocument(
        `{"issuer":"https://evil.example","issuer":${JSON.stringify(ISSUER)}}`,
        ISSUER,
      ),
    ).toThrow(/repeats the issuer/);
  });
});

describe("discoverProtectedResource", () => {
  const metadata = metadataUrl(RESOURCE);
  const issuerMetadata = oidcMetadataUrl(ISSUER);

  it("fetches both documents anonymously with redirects disabled and returns the profile", async () => {
    const { fetchImpl, calls } = fakeFetch({
      [metadata]: { body: goodResource() },
      [issuerMetadata]: {
        body: goodIssuer(),
        contentType: "application/jwk-set+json",
      },
    });
    const profile = await discoverProtectedResource(RESOURCE, { fetchImpl });
    expect(profile.clientId).toBe("studio-client");
    expect(profile.issuer).toBe(ISSUER);
    expect(calls.map((c) => c.url)).toEqual([metadata, issuerMetadata]);
    for (const { init } of calls) {
      expect(init.redirect).toBe("manual");
      expect(init.cache).toBe("no-store");
      expect(new Headers(init.headers).has("authorization")).toBe(false);
    }
  });

  it("threads the TLS dispatcher into every fetch", async () => {
    const dispatcher = { tag: "agent" };
    const { fetchImpl, calls } = fakeFetch({
      [metadata]: { body: goodResource() },
      [issuerMetadata]: { body: goodIssuer() },
    });
    await discoverProtectedResource(RESOURCE, { fetchImpl, dispatcher });
    for (const { init } of calls)
      expect((init as { dispatcher?: unknown }).dispatcher).toBe(dispatcher);
  });

  it("rejects a redirecting well-known URL", async () => {
    const { fetchImpl } = fakeFetch({
      [metadata]: { status: 302, body: "" },
    });
    await rejects(
      discoverProtectedResource(RESOURCE, { fetchImpl }),
      /redirected/,
    );
  });

  it("rejects a non-JSON media type and a non-200 answer", async () => {
    const html = fakeFetch({
      [metadata]: { body: goodResource(), contentType: "text/html" },
    });
    await rejects(
      discoverProtectedResource(RESOURCE, { fetchImpl: html.fetchImpl }),
      /not JSON/,
    );
    const missing = fakeFetch({});
    await rejects(
      discoverProtectedResource(RESOURCE, { fetchImpl: missing.fetchImpl }),
      /HTTP 404/,
    );
  });

  it("rejects an oversized body", async () => {
    const { fetchImpl } = fakeFetch({
      [metadata]: {
        body: `{"pad":"${"x".repeat(MAX_DISCOVERY_BODY_BYTES)}"}`,
      },
    });
    await rejects(
      discoverProtectedResource(RESOURCE, { fetchImpl }),
      /too large/,
    );
  });

  it("rejects an issuer document that names another issuer", async () => {
    const { fetchImpl } = fakeFetch({
      [metadata]: { body: goodResource() },
      [issuerMetadata]: { body: goodIssuer("https://evil.example.com") },
    });
    await rejects(
      discoverProtectedResource(RESOURCE, { fetchImpl }),
      /does not match/,
    );
  });

  it("rejects a plain-HTTP deployment without the private-issuer opt-in, and admits a loopback one with it", async () => {
    await rejects(
      discoverProtectedResource("http://127.0.0.1:8081", {
        fetchImpl: fakeFetch({}).fetchImpl,
      }),
      /must use HTTPS/,
    );
    const resource = "http://127.0.0.1:8081";
    const issuer = "http://127.0.0.1:9000";
    const { fetchImpl } = fakeFetch({
      [metadataUrl(resource)]: {
        body: goodResource({ resource, authorization_servers: [issuer] }),
      },
      [oidcMetadataUrl(issuer)]: { body: goodIssuer(issuer) },
    });
    const profile = await discoverProtectedResource(resource, {
      fetchImpl,
      allowPrivateHttp: true,
    });
    expect(profile.issuer).toBe(issuer);
  });

  it("maps a thrown fetch to a harness-authored rejection", async () => {
    const fetchImpl = async () => {
      throw new Error("ECONNREFUSED 10.0.0.1");
    };
    await rejects(
      discoverProtectedResource(RESOURCE, { fetchImpl }),
      /could not be fetched/,
    );
  });
});

describe("profileHash", () => {
  it("is stable for equal identities and differs on any field", () => {
    const base = {
      source: "discovery",
      issuer: ISSUER,
      clientId: "studio-client",
      audience: "mecatl-daemon",
      scope: "openid profile",
    };
    expect(profileHash(base)).toBe(profileHash({ ...base }));
    expect(profileHash(base)).toMatch(/^[0-9a-f]{64}$/);
    expect(profileHash({ ...base, clientId: "other" })).not.toBe(
      profileHash(base),
    );
    expect(profileHash({ ...base, source: "env" })).not.toBe(profileHash(base));
  });
});
