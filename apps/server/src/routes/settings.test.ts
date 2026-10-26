// SPDX-License-Identifier: Apache-2.0

import { type Client, MecatlError } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";
import { createApp } from "../app";
import { type AuthenticationService, createAuthenticationService } from "../auth/service";
import { RuntimeNotReadyError } from "../mecatl/runtime";
import type { SettingsService } from "../mecatl/settings";
import { createMecatlSettingsService } from "../mecatl/settings";
import { fakeIssuer, resourceUrl } from "../testing/fake-issuer";
import { cookieHeader, fakeRuntime } from "../testing/fakes";

const settings: SettingsService = {
  async getProvider(providerId) {
    if (providerId !== "test") return null;
    return {
      displayEndpoint: null,
      models: [
        {
          contextLimit: "128000",
          displayName: "Example",
          id: "example",
          image: true,
          providerDisplayName: "Test",
          providerId: "test",
          reasoning: true,
        },
      ],
      provider: {
        availableNotDefault: false,
        defaultModelAutoSelected: true,
        displayName: "Test",
        hint: "",
        id: "test",
        modelCount: 1,
        state: "available",
      },
    };
  },
  async get() {
    return {
      buildId: "dev",
      management: {
        providerConfiguration: false,
        providerConfigurationReason: "Managed by the deployment.",
        routingConfiguration: false,
        routingConfigurationReason: "Managed by the deployment.",
      },
      models: [
        {
          contextLimit: "128000",
          displayName: "Example",
          id: "example",
          image: true,
          providerDisplayName: "Test",
          providerId: "test",
          reasoning: true,
        },
      ],
      modelsReason: "",
      modelsSupported: true,
      providerEndpoint: "",
      providers: [
        {
          availableNotDefault: false,
          defaultModelAutoSelected: true,
          displayName: "Test",
          hint: "",
          id: "test",
          modelCount: 1,
          state: "available",
        },
      ],
      serverImplementation: "mecated",
    };
  },
};

describe("settings routes", () => {
  it.each(["unauthenticated", "authentication"] as const)(
    "clears a still-live OIDC session when provider info rejects it with %s",
    async (code) => {
      const publicUrl = new URL("https://studio.example.com");
      const issuer = fakeIssuer();
      const authentication = (await createAuthenticationService(
        { baseUrl: resourceUrl, resourceUrl, source: "external" },
        {
          fetch: issuer.fetch,
          publicUrl,
          sessionSecret: "0123456789abcdef0123456789abcdef",
        },
      )) as AuthenticationService;
      let activeToken: string | undefined;
      const list = vi.fn(async () => {
        expect(activeToken).toMatch(/^access-\d+-/);
        return {
          models: [
            {
              contextLimit: 128_000n,
              displayName: "Visible",
              id: "visible",
              image: false,
              providerId: "visible/provider",
              reasoning: false,
            },
          ],
          providerStatus: [],
        };
      });
      const info = vi.fn().mockRejectedValue(
        new MecatlError("Bearer revoked-secret rejected", {
          code,
          status: 16,
          transport: "grpc",
        }),
      );
      const client = { models: { list }, server: { info } } as unknown as Client;
      const app = createApp({
        authentication,
        runtime: fakeRuntime({
          runWithCredential: async (token, operation) => {
            activeToken = token;
            try {
              return await operation();
            } finally {
              activeToken = undefined;
            }
          },
        }),
        security: { publicUrl },
        settings: createMecatlSettingsService(client, {
          modelSelection: true,
          serverInfo: true,
        }),
      });
      const login = await app.request("https://studio.example.com/api/v1/auth/login");
      const transaction = cookieHeader(login.headers.getSetCookie());
      const state = new URL(login.headers.get("location") ?? "").searchParams.get("state");
      const callback = await app.request(
        `https://studio.example.com/api/v1/auth/callback?code=code-1&state=${state}`,
        { headers: { Cookie: transaction } },
      );
      expect(callback.status).toBe(302);
      const session = cookieHeader(callback.headers.getSetCookie());
      const request = (providerId: string) =>
        app.request(
          `https://studio.example.com/api/v1/settings/provider?providerId=${encodeURIComponent(providerId)}`,
          {
            headers: { Cookie: session },
          },
        );

      const hidden = await request("hidden/provider");
      expect(hidden.status).toBe(404);
      await expect(hidden.json()).resolves.toMatchObject({ code: "provider_not_found" });
      expect(info).not.toHaveBeenCalled();

      const rejected = await request("visible/provider");
      expect(rejected.status).toBe(401);
      const body = await rejected.json();
      expect(body).toMatchObject({ code: "session_expired" });
      expect(JSON.stringify(body)).not.toContain("revoked-secret");
      // An upstream rejection gets one refresh and one retry; a credential
      // provider failure is local and gets none.
      const attempts = code === "unauthenticated" ? 2 : 1;
      expect(info).toHaveBeenCalledTimes(attempts);
      expect(info).toHaveBeenCalledWith({ providerId: "visible/provider" });
      expect(issuer.refreshCalls()).toBe(attempts - 1);
      for (const name of ["studio_access", "studio_refresh"]) {
        expect(
          rejected.headers
            .getSetCookie()
            .some((cookie) => cookie.startsWith(`${name}=;`) && cookie.includes("Max-Age=0")),
          name,
        ).toBe(true);
      }
    },
  );

  it("does not expose provider details to anonymous callers", async () => {
    const list = vi.fn().mockResolvedValue({
      models: [
        {
          contextLimit: 100n,
          displayName: "Model",
          id: "model",
          image: false,
          providerId: "team/provider",
          reasoning: false,
          credential: "secret",
        },
      ],
      providerStatus: [
        {
          availableNotDefault: false,
          defaultModelAutoSelected: true,
          hint: "ready",
          modelCount: 1,
          providerId: "team/provider",
          state: "available",
          rawEndpoint: "https://user:secret@private.example",
        },
      ],
    });
    const info = vi.fn().mockResolvedValue({
      llmProviderDisplayEndpoint: "https://public.example/v1",
      rawEndpoint: "https://user:secret@private.example",
    });
    const client = { models: { list }, server: { info } } as unknown as Client;
    const service = createMecatlSettingsService(client, { modelSelection: true, serverInfo: true });
    const auth = {
      clear: () => undefined,
      freshTokenPerSessionOpen: false,
      refresh: () => Promise.reject(new Error("refresh unavailable")),
      completeLogin: async () => {
        throw new Error("unused");
      },
      credential: async () => ({ status: "anonymous" as const }),
      logout: async () => undefined,
      noteLoginComplete: () => undefined,
      noteLoginFailure: () => undefined,
      save: async () => undefined,
      signInRequired: async () => true,
      startLogin: async () => "https://issuer.example.com/authorize",
    };
    const gated = createApp({ authentication: auth, runtime: fakeRuntime(), settings: service });
    const denied = await gated.request("/api/v1/settings/provider?providerId=team%2Fprovider");
    expect(denied.status).toBe(401);
    await expect(denied.json()).resolves.toMatchObject({ code: "unauthenticated" });
    expect(list).not.toHaveBeenCalled();
    expect(info).not.toHaveBeenCalled();

    // Static/no-auth mode follows the existing shared-principal BFF opt-in.
    const shared = createApp({ runtime: fakeRuntime(), settings: service });
    const response = await shared.request("/api/v1/settings/provider?providerId=team%2Fprovider");
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      displayEndpoint: "https://public.example/v1",
      models: [
        {
          contextLimit: "100",
          displayName: "Model",
          id: "model",
          image: false,
          providerDisplayName: "Team/provider",
          providerId: "team/provider",
          reasoning: false,
        },
      ],
      provider: {
        availableNotDefault: false,
        defaultModelAutoSelected: true,
        displayName: "Team/provider",
        hint: "ready",
        id: "team/provider",
        modelCount: 1,
        state: "available",
      },
    });
    expect(info).toHaveBeenCalledExactlyOnceWith({ providerId: "team/provider" });
    expect(
      JSON.stringify(
        await (await shared.request("/api/v1/settings/provider?providerId=team%2Fprovider")).json(),
      ),
    ).not.toContain("secret");

    info.mockClear();
    const unknown = await shared.request("/api/v1/settings/provider?providerId=absent");
    expect(unknown.status).toBe(404);
    await expect(unknown.json()).resolves.toMatchObject({ code: "provider_not_found" });
    expect(info).not.toHaveBeenCalled();

    for (const query of ["", "?providerId=", "?providerId=team%2Fprovider&providerId=other"]) {
      const invalid = await shared.request(`/api/v1/settings/provider${query}`);
      expect(invalid.status).toBe(400);
      await expect(invalid.json()).resolves.toMatchObject({ code: "invalid_request" });
    }

    const disabledList = vi.fn();
    const disabledInfo = vi.fn();
    const disabled = createApp({
      runtime: fakeRuntime(),
      settings: createMecatlSettingsService(
        { models: { list: disabledList }, server: { info: disabledInfo } } as unknown as Client,
        { modelSelection: false, serverInfo: true },
      ),
    });
    const unsupported = await disabled.request(
      "/api/v1/settings/provider?providerId=team%2Fprovider",
    );
    expect(unsupported.status).toBe(501);
    await expect(unsupported.json()).resolves.toMatchObject({ code: "models_unsupported" });
    expect(disabledList).not.toHaveBeenCalled();
    expect(disabledInfo).not.toHaveBeenCalled();

    const unready = createApp({
      readinessTimeoutMs: 1,
      runtime: fakeRuntime({
        ready: () => new Promise(() => undefined),
        snapshot: () => {
          throw new RuntimeNotReadyError();
        },
      }),
      settings: service,
    });
    const pending = await unready.request("/api/v1/settings/provider?providerId=team%2Fprovider");
    expect(pending.status).toBe(503);
    await expect(pending.json()).resolves.toMatchObject({ code: "runtime_unavailable" });

    info.mockRejectedValueOnce(
      new MecatlError("Bearer secret at https://private.example", {
        code: "internal",
        status: 13,
        transport: "grpc",
      }),
    );
    const failed = await shared.request("/api/v1/settings/provider?providerId=team%2Fprovider");
    expect(failed.status).toBe(503);
    const failedBody = await failed.json();
    expect(failedBody).toMatchObject({ code: "runtime_unavailable" });
    expect(JSON.stringify(failedBody)).not.toContain("secret");
  });
  it("keeps both settings responses' provider and model fields over HTTP", async () => {
    // Pins the JSON the browser received before provider display names
    // (#2260), for an operator-defined provider with no display name.
    const list = vi.fn().mockResolvedValue({
      models: [
        {
          contextLimit: 64_000n,
          displayName: "Gateway Model",
          id: "gateway-model",
          image: true,
          providerId: "team-gateway",
          reasoning: false,
        },
      ],
      providerStatus: [
        {
          availableNotDefault: false,
          defaultModelAutoSelected: true,
          hint: "",
          modelCount: 1,
          providerId: "team-gateway",
          state: "ok",
        },
      ],
    });
    const info = vi.fn().mockResolvedValue({ llmProviderDisplayEndpoint: "" });
    const app = createApp({
      runtime: fakeRuntime(),
      settings: createMecatlSettingsService(
        { models: { list }, server: { info } } as unknown as Client,
        { modelSelection: true, serverInfo: true },
      ),
    });
    const provider = {
      availableNotDefault: false,
      defaultModelAutoSelected: true,
      hint: "",
      id: "team-gateway",
      modelCount: 1,
      state: "ok",
    };
    const model = {
      contextLimit: "64000",
      displayName: "Gateway Model",
      id: "gateway-model",
      image: true,
      providerId: "team-gateway",
      reasoning: false,
    };

    const runtime = await app.request("/api/v1/settings/runtime");
    expect(runtime.status).toBe(200);
    expect(await runtime.json()).toMatchObject({ models: [model], providers: [provider] });

    const detail = await app.request("/api/v1/settings/provider?providerId=team-gateway");
    expect(detail.status).toBe(200);
    expect(await detail.json()).toMatchObject({
      displayEndpoint: null,
      models: [model],
      provider,
    });
  });
  it("serves provider display names over HTTP, title-casing an unmapped ID", async () => {
    const list = vi.fn().mockResolvedValue({
      models: [
        {
          contextLimit: 128_000n,
          displayName: "GPT-5",
          id: "gpt-5",
          image: true,
          providerId: "openai",
          reasoning: true,
        },
        {
          contextLimit: 64_000n,
          displayName: "Gateway Model",
          id: "gateway-model",
          image: false,
          providerId: "team-gateway",
          reasoning: false,
        },
      ],
      providerStatus: [],
    });
    const app = createApp({
      runtime: fakeRuntime(),
      settings: createMecatlSettingsService(
        { models: { list }, server: { info: vi.fn() } } as unknown as Client,
        { modelSelection: true, serverInfo: false },
      ),
    });

    const runtime = await (await app.request("/api/v1/settings/runtime")).json();
    expect(runtime.providers).toEqual([
      expect.objectContaining({ displayName: "OpenAI", id: "openai" }),
      expect.objectContaining({ displayName: "Team Gateway", id: "team-gateway" }),
    ]);
    expect(runtime.models).toEqual([
      expect.objectContaining({ id: "gpt-5", providerDisplayName: "OpenAI" }),
      expect.objectContaining({ id: "gateway-model", providerDisplayName: "Team Gateway" }),
    ]);

    const detail = await (await app.request("/api/v1/settings/provider?providerId=openai")).json();
    expect(detail.provider).toMatchObject({ displayName: "OpenAI", id: "openai" });
    expect(detail.models).toEqual([expect.objectContaining({ providerDisplayName: "OpenAI" })]);
  });
  it("returns only safe runtime and model inventory", async () => {
    const response = await createApp({ settings }).request("/api/v1/settings/runtime");
    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({
      management: { providerConfiguration: false },
      models: [{ id: "example", providerId: "test" }],
    });
  });
  it("answers 503 without a runtime and 401 without a session", async () => {
    const detached = await createApp().request("/api/v1/settings/runtime");
    expect(detached.status).toBe(503);
    await expect(detached.json()).resolves.toMatchObject({ code: "runtime_unavailable" });

    const gated = createApp({
      authentication: {
        clear: () => undefined,
        freshTokenPerSessionOpen: false,
        refresh: () => Promise.reject(new Error("refresh unavailable")),
        completeLogin: async () => {
          throw new Error("unused");
        },
        credential: async () => ({ status: "anonymous" }),
        logout: async () => undefined,
        noteLoginComplete: () => undefined,
        noteLoginFailure: () => undefined,
        save: async () => undefined,
        signInRequired: async () => true,
        startLogin: async () => "https://issuer.example.com/authorize",
      },
      runtime: fakeRuntime(),
      settings,
    });
    const response = await gated.request("/api/v1/settings/runtime");
    expect(response.status).toBe(401);
    await expect(response.json()).resolves.toMatchObject({ code: "unauthenticated" });
  });
});
