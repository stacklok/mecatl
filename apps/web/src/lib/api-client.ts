// SPDX-License-Identifier: Apache-2.0

import { client } from "@mecatl-studio/contracts/client";

export const csrfCookieName = "studio_csrf";
export const csrfHeaderName = "X-Studio-CSRF";

const mutatingMethods = new Set(["POST", "PUT", "PATCH", "DELETE"]);

/** Reads the double-submit token the BFF issued, from a `document.cookie` string. */
export function readCsrfToken(cookieString: string): string | undefined {
  for (const pair of cookieString.split(";")) {
    const [name, ...rest] = pair.trim().split("=");
    if (name === csrfCookieName) {
      const value = rest.join("=");
      return value === "" ? undefined : value;
    }
  }
  return undefined;
}

/**
 * Every state-changing call to the BFF must carry `X-Studio-CSRF` equal to
 * the `studio_csrf` cookie (the BFF's double-submit check). The generated
 * client is shared by every feature, so the header is attached once here.
 */
export function installCsrfInterceptor(readCookies: () => string = () => document.cookie): void {
  client.interceptors.request.use((request) => {
    if (!mutatingMethods.has(request.method.toUpperCase())) return request;
    const token = readCsrfToken(readCookies());
    if (token !== undefined) request.headers.set(csrfHeaderName, token);
    return request;
  });
}
