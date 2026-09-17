// SPDX-License-Identifier: Apache-2.0
// Copyright (c) Stacklok, Inc. All rights reserved.

import * as fs from "node:fs";

// force-dynamic prevents Next.js from caching this route at build time.
// FAVICON_URL is a runtime environment variable — it is set via Helm values
// and must be read on each request, not baked into the build output.
export const dynamic = "force-dynamic";

async function customFavicon(url: string): Promise<Response> {
  const upstream = await fetch(url);
  const body = await upstream.arrayBuffer();
  return new Response(body, {
    headers: {
      "Content-Type": upstream.headers.get("Content-Type") ?? "image/x-icon",
    },
  });
}

function defaultFavicon(): Response {
  const pngContent = fs.readFileSync(
    `${process.cwd()}/public/stacklok-favicon.png`,
  );
  return new Response(pngContent, {
    headers: { "Content-Type": "image/png" },
  });
}

export async function GET(): Promise<Response> {
  const faviconUrl = process.env.FAVICON_URL;
  return faviconUrl ? customFavicon(faviconUrl) : defaultFavicon();
}

export default GET;
