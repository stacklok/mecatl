import "server-only";

import { resolveExternalAuthorization } from "@/lib/oidc-session";
import { requestIsTrusted } from "@/lib/request-trust";

const controllerBaseURL = "http://127.0.0.1:8788";
const forwardedRequestHeaders = [
  "accept",
  "content-type",
  "last-event-id",
  "mcp-protocol-version",
  "mcp-session-id",
];
const forwardedResponseHeaders = [
  "cache-control",
  "content-type",
  "mcp-session-id",
  "www-authenticate",
];
const externalBaseURL = () =>
  process.env.MECATL_BASE_URL?.trim().replace(/\/$/, "") || "";

function forbidden() {
  return Response.json(
    { error: "request origin is not allowed" },
    { status: 403 },
  );
}

function copyRequestHeaders(request: Request) {
  const headers = new Headers();
  for (const name of forwardedRequestHeaders) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }
  return headers;
}

function copyResponse(upstream: Response) {
  const headers = new Headers();
  for (const name of forwardedResponseHeaders) {
    const value = upstream.headers.get(name);
    if (value) headers.set(name, value);
  }
  return new Response(upstream.body, { status: upstream.status, headers });
}

async function forward(request: Request, target: URL, headers: Headers) {
  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  try {
    const upstream = await fetch(target, {
      method: request.method,
      headers,
      body: hasBody ? await request.arrayBuffer() : undefined,
      cache: "no-store",
      redirect: "manual",
    });
    return copyResponse(upstream);
  } catch {
    return Response.json(
      { error: "The Mecatl service is unavailable. It may be restarting." },
      { status: 503 },
    );
  }
}

// Session placement is SERVER-OWNED (ADR 0291): the daemon binds every session
// it creates to its deployment's own environment, `POST /v1/sessions` rejects
// unknown body fields, the schedule contract reserves the old `workspace`
// field, and `GET /v1/commands` is keyed by `session_id`. The proxy therefore
// forwards every daemon request body and query string VERBATIM — it injects
// authentication only. Studio reaches the daemon exclusively through the
// TypeScript SDK (`@stacklok-oss/mecatl-sdk`) pointed at this same-origin
// route, so the credential never leaves the server tier.
export async function proxyMecatl(request: Request, path: string[]) {
  if (!requestIsTrusted(request)) return forbidden();
  const external = externalBaseURL();
  const base = external || `${controllerBaseURL}/mecatl`;
  const target = new URL(`${base}/${path.map(encodeURIComponent).join("/")}`);
  target.search = new URL(request.url).search;
  const headers = copyRequestHeaders(request);
  if (external) {
    // Bearer injection (rule 3 — credentials never come from the browser):
    // an OIDC-configured deployment injects the CURRENT access token
    // (refreshed server-side on demand, requirement H3); without OIDC config
    // this is the static MECATL_AUTH_TOKEN path, unchanged. A signed-out or
    // expired OIDC session answers 401 with actionable copy instead of
    // forwarding a request the daemon would reject opaquely.
    const auth = await resolveExternalAuthorization();
    if (auth.kind === "unauthorized")
      return Response.json(
        { error: auth.error, code: auth.code },
        { status: auth.status },
      );
    if (auth.kind === "unavailable")
      return Response.json({ error: auth.error }, { status: auth.status });
    if (auth.kind === "bearer")
      headers.set("authorization", `Bearer ${auth.token}`);
  } else {
    headers.set("x-mecatl-studio-request", "1");
  }

  return forward(request, target, headers);
}

export async function proxyControl(request: Request, path: string[]) {
  if (!requestIsTrusted(request)) return forbidden();
  const external = externalBaseURL();
  if (external) {
    if (request.method === "GET" && path.join("/") === "status") {
      return Response.json({
        mode: "external",
        provider: "external daemon",
        running: true,
        workspace: process.env.MECATL_WORKSPACE?.trim() || "",
        gateway: null,
        modelRouter: null,
        operatorSettings: true,
        skills: null,
        memory: null,
      });
    }
    return Response.json(
      { error: "This setting is owned by the external mecated deployment." },
      { status: 409 },
    );
  }

  const target = new URL(
    `${controllerBaseURL}/${path.map(encodeURIComponent).join("/")}`,
  );
  target.search = new URL(request.url).search;
  const headers = copyRequestHeaders(request);
  headers.set("x-mecatl-studio-request", "1");
  return forward(request, target, headers);
}
