import { describe, expect, it } from "vitest";

import { loadConfig } from "../src/env.js";

const BASE_ENV: NodeJS.ProcessEnv = {
  SLACK_APP_TOKEN: "xapp-test",
  SLACK_BOT_TOKEN: "xoxb-test",
};

describe("loadConfig", () => {
  it("defaults to a plain http:// target when MECATL_GRPC_TLS is unset", () => {
    const config = loadConfig({ ...BASE_ENV, MECATL_GRPC_ADDRESS: "127.0.0.1:50051" });
    expect(config.mecatlTarget).toStrictEqual({ baseUrl: "http://127.0.0.1:50051" });
  });

  it("builds an https:// target with a bearer-attaching credentialProvider when TLS is on", async () => {
    const config = loadConfig({
      ...BASE_ENV,
      MECAK8S_OIDC_CLIENT_ID: "client-id",
      MECAK8S_OIDC_CLIENT_SECRET: "client-secret",
      MECAK8S_OIDC_TOKEN_URL: "https://example.okta.com/oauth2/abc/v1/token",
      MECATL_GRPC_ADDRESS: "mecak8s.stacklok.dev:443",
      MECATL_GRPC_TLS: "true",
    });
    expect(config.mecatlTarget.baseUrl).toBe("https://mecak8s.stacklok.dev:443");
    expect(config.mecatlTarget.credentialProvider).toBeInstanceOf(Function);
  });

  it("fails fast when TLS is on but an OIDC variable is missing", () => {
    expect(() =>
      loadConfig({
        ...BASE_ENV,
        MECAK8S_OIDC_CLIENT_ID: "client-id",
        MECATL_GRPC_ADDRESS: "mecak8s.stacklok.dev:443",
        MECATL_GRPC_TLS: "true",
        // MECAK8S_OIDC_CLIENT_SECRET and MECAK8S_OIDC_TOKEN_URL intentionally omitted.
      }),
    ).toThrow(/MECAK8S_OIDC_CLIENT_SECRET/);
  });

  it("never attaches a credentialProvider on the socketPath (local/Compose) path", () => {
    const config = loadConfig({ ...BASE_ENV, MECATL_SOCKET_PATH: "/tmp/mecated.sock" });
    expect(config.mecatlTarget).toStrictEqual({ socketPath: "/tmp/mecated.sock" });
  });

  it("leaves allowedChannelIds undefined when SLACK_ALLOWED_CHANNEL_IDS is unset", () => {
    const config = loadConfig({ ...BASE_ENV, MECATL_SOCKET_PATH: "/tmp/mecated.sock" });
    expect(config.allowedChannelIds).toBeUndefined();
  });

  it("parses SLACK_ALLOWED_CHANNEL_IDS into a set, preserving case", () => {
    const config = loadConfig({
      ...BASE_ENV,
      MECATL_SOCKET_PATH: "/tmp/mecated.sock",
      SLACK_ALLOWED_CHANNEL_IDS: " C0123ABCDEF, C0456GHIJKL ,,",
    });
    expect(config.allowedChannelIds).toStrictEqual(new Set(["C0123ABCDEF", "C0456GHIJKL"]));
  });
});
