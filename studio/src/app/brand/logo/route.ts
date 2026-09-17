// SPDX-License-Identifier: Apache-2.0

import * as fsPromises from "node:fs/promises";

// force-dynamic: BRAND_LOGO_URL is a runtime env var set via Helm/Kubernetes.
export const dynamic = "force-dynamic";

async function serveFallback(): Promise<Response> {
  const content = await fsPromises.readFile(
    `${process.cwd()}/public/stacklok-logo.svg`,
  );
  return new Response(content, {
    headers: { "Content-Type": "image/svg+xml" },
  });
}

export async function GET(): Promise<Response> {
  const brandLogoUrl = process.env.BRAND_LOGO_URL;
  if (!brandLogoUrl) return serveFallback();
  try {
    const upstream = await fetch(brandLogoUrl);
    if (!upstream.ok) return new Response(null, { status: 404 });
    const contentType = upstream.headers.get("Content-Type") ?? "image/svg+xml";
    return new Response(upstream.body, {
      headers: { "Content-Type": contentType },
    });
  } catch {
    return new Response(null, { status: 404 });
  }
}
