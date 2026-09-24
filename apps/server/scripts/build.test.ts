// SPDX-License-Identifier: Apache-2.0

import { readFile } from "node:fs/promises";
import { fileURLToPath, pathToFileURL } from "node:url";
import { build } from "esbuild";
import { describe, expect, it, vi } from "vitest";
import { fakeRuntime } from "../src/testing/fakes.js";
import { installedSdkPackageVersion } from "../src/testing/sdk-package.js";
import { serverBuildDefinitions } from "./build-options.js";

async function bundledBuildId(releaseTag: string | undefined) {
  const result = await build({
    bundle: true,
    define: serverBuildDefinitions({ STUDIO_BUILD_ID: releaseTag }),
    entryPoints: [fileURLToPath(new URL("../src/build-info.ts", import.meta.url))],
    format: "esm",
    platform: "node",
    write: false,
  });
  const output = result.outputFiles[0]?.text;
  if (!output) throw new Error("esbuild produced no output");
  const moduleUrl = `data:text/javascript;base64,${Buffer.from(output).toString("base64")}`;
  return (await import(moduleUrl)) as { studioBuildId: string | undefined };
}

async function bundledApp(releaseTag: string | undefined) {
  const manifest = JSON.parse(
    await readFile(new URL("../package.json", import.meta.url), "utf8"),
  ) as {
    dependencies: Record<string, string>;
  };
  const outfile = fileURLToPath(
    new URL(
      `../dist/app-build-probe-${releaseTag === undefined ? "local" : "release"}.mjs`,
      import.meta.url,
    ),
  );
  await build({
    bundle: true,
    define: serverBuildDefinitions({ STUDIO_BUILD_ID: releaseTag }),
    entryPoints: [fileURLToPath(new URL("../src/app.ts", import.meta.url))],
    external: Object.keys(manifest.dependencies).filter(
      (name) => !name.startsWith("@mecatl-studio/"),
    ),
    format: "esm",
    outfile,
    platform: "node",
    target: "node24",
  });
  return (await import(pathToFileURL(outfile).href)) as {
    createApp: (dependencies: { runtime: ReturnType<typeof fakeRuntime> }) => {
      request: (path: string) => Promise<Response>;
    };
  };
}

describe("Studio image build stamp", () => {
  it("omits the stamp in source mode even when the runtime environment sets it", async () => {
    const original = process.env.STUDIO_BUILD_ID;
    try {
      process.env.STUDIO_BUILD_ID = "runtime-impostor";
      vi.resetModules();
      const { studioBuildId } = await import("../src/build-info.js");
      expect(studioBuildId).toBeUndefined();
      const { createApp } = await import("../src/app.js");
      const response = await createApp({ runtime: fakeRuntime() }).request("/api/v1/runtime");
      expect(response.status).toBe(200);
      expect(await response.json()).not.toHaveProperty("studioBuildId");
    } finally {
      if (original === undefined) Reflect.deleteProperty(process.env, "STUDIO_BUILD_ID");
      else process.env.STUDIO_BUILD_ID = original;
      vi.resetModules();
    }
  });

  it("bakes the release tag into the BFF and omits it in local builds", async () => {
    const original = process.env.STUDIO_BUILD_ID;
    try {
      const released = await bundledBuildId("v1.2.3");
      const local = await bundledBuildId(undefined);
      process.env.STUDIO_BUILD_ID = "runtime-impostor";
      expect(released.studioBuildId).toBe("v1.2.3");
      expect(local.studioBuildId).toBeUndefined();
    } finally {
      if (original === undefined) Reflect.deleteProperty(process.env, "STUDIO_BUILD_ID");
      else process.env.STUDIO_BUILD_ID = original;
    }
  });

  it("reports the baked release tag through the bundled BFF despite a runtime override", async () => {
    const original = process.env.STUDIO_BUILD_ID;
    try {
      const released = await bundledApp("v1.2.3");
      const local = await bundledApp(undefined);
      process.env.STUDIO_BUILD_ID = "runtime-impostor";
      const runtime = fakeRuntime();
      const releaseResponse = await released.createApp({ runtime }).request("/api/v1/runtime");
      const localResponse = await local.createApp({ runtime }).request("/api/v1/runtime");
      expect(releaseResponse.status).toBe(200);
      expect(localResponse.status).toBe(200);
      await expect(releaseResponse.json()).resolves.toMatchObject({
        sdkVersion: installedSdkPackageVersion(),
        studioBuildId: "v1.2.3",
      });
      const localBody = (await localResponse.json()) as Record<string, unknown>;
      expect(localBody).not.toHaveProperty("studioBuildId");
      expect(localBody.sdkVersion).toBe(installedSdkPackageVersion());
    } finally {
      if (original === undefined) Reflect.deleteProperty(process.env, "STUDIO_BUILD_ID");
      else process.env.STUDIO_BUILD_ID = original;
    }
  }, 20_000);
});
