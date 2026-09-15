// SPDX-License-Identifier: Apache-2.0
// Copyright (c) Stacklok, Inc. All rights reserved.

import { expect, it, vi } from "vitest";
import { GET } from "./icon";

it("returns a Response when FAVICON_URL is not set", async () => {
  vi.stubEnv("FAVICON_URL", "");
  await expect(GET()).resolves.toBeInstanceOf(Response);
});

it("returns a Response when FAVICON_URL is set", async () => {
  vi.stubEnv("FAVICON_URL", "https://www.google.com/favicon.ico");
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(new Uint8Array([0]))),
  );
  await expect(GET()).resolves.toBeInstanceOf(Response);
});
