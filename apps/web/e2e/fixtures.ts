// SPDX-License-Identifier: Apache-2.0

import { test as base, expect, type Request } from "@playwright/test";

export interface BffResponse {
  body?: string;
  contentType?: string;
  headers?: Record<string, string>;
  status?: number;
}

type BffHandler = (request: Request) => BffResponse | Promise<BffResponse>;

export interface OfflineBff {
  /** Replace the response for one BFF method and pathname. Query and body stay on the request. */
  on(method: string, pathname: string, handler: BffHandler): void;
  json(method: string, pathname: string, body: unknown, status?: number): void;
  fail(method: string, pathname: string): void;
  /** Serve the one explicit offline cross-origin issuer used by popup journeys. */
  onIssuer(pathname: string, handler: BffHandler): void;
  requestsFor(method: string, pathname: string): readonly Request[];
}

interface TestFixtures {
  offlineBff: OfflineBff;
}

function key(method: string, pathname: string): string {
  if (!pathname.startsWith("/") || pathname.includes("?")) {
    throw new Error(`BFF fixture expects a pathname: ${pathname}`);
  }
  return `${method.toUpperCase()} ${pathname}`;
}

export const test = base.extend<TestFixtures>({
  offlineBff: [
    async ({ baseURL, context }, use) => {
      if (baseURL === undefined) throw new Error("offline BFF fixture requires a baseURL");
      const appOrigin = new URL(baseURL).origin;
      const handlers = new Map<string, BffHandler | "fail">();
      const issuerHandlers = new Map<string, BffHandler>();
      const requests: Request[] = [];
      const unexpected: string[] = [];
      const external: string[] = [];

      const offlineBff: OfflineBff = {
        on(method, pathname, handler) {
          handlers.set(key(method, pathname), handler);
        },
        json(method, pathname, body, status = 200) {
          handlers.set(key(method, pathname), () => ({
            body: JSON.stringify(body),
            contentType: "application/json",
            status,
          }));
        },
        fail(method, pathname) {
          handlers.set(key(method, pathname), "fail");
        },
        onIssuer(pathname, handler) {
          issuerHandlers.set(key("GET", pathname), handler);
        },
        requestsFor(method, pathname) {
          const expected = key(method, pathname);
          return requests.filter(
            (request) => key(request.method(), new URL(request.url()).pathname) === expected,
          );
        },
      };

      await context.route("**/*", async (route) => {
        const request = route.request();
        const url = new URL(request.url());

        // Studio's CSS imports this stylesheet. Fulfill it locally so browser
        // journeys stay offline without treating the existing font as BFF traffic.
        if (url.origin === "https://fonts.googleapis.com" && url.pathname === "/css2") {
          await route.fulfill({ body: "", contentType: "text/css", status: 200 });
          return;
        }
        if (url.origin === "https://issuer.offline.invalid") {
          const handler = issuerHandlers.get(key(request.method(), url.pathname));
          if (handler === undefined) {
            unexpected.push(`${request.method()} ${url.href}`);
            await route.abort("blockedbyclient");
          } else {
            await route.fulfill(await handler(request));
          }
          return;
        }
        if (url.origin !== appOrigin) {
          external.push(`${request.method()} ${url.href}`);
          await route.abort("blockedbyclient");
          return;
        }
        if (!url.pathname.startsWith("/api/") && url.pathname !== "/oauth/callback") {
          await route.continue();
          return;
        }

        requests.push(request);
        const requestKey = key(request.method(), url.pathname);
        const handler = handlers.get(requestKey);
        if (handler === undefined) {
          unexpected.push(requestKey);
          await route.abort("blockedbyclient");
        } else if (handler === "fail") {
          await route.abort("failed");
        } else {
          await route.fulfill(await handler(request));
        }
      });

      await context.routeWebSocket("**/*", (socket) => {
        const url = new URL(socket.url());
        if (url.host !== new URL(appOrigin).host) {
          external.push(`WebSocket ${url.href}`);
          socket.close();
          return;
        }
        socket.connectToServer();
      });

      await use(offlineBff);
      await context.unrouteAll({ behavior: "wait" });
      expect(unexpected, "every BFF call must have an offline response").toEqual([]);
      expect(external, "browser tests attempted non-fixture network access").toEqual([]);
    },
    { auto: true },
  ],
});

export { expect };
