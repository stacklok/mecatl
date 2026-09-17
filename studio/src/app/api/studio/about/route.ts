import { requestIsTrusted } from "@/lib/request-trust";
import { studioBuild } from "@/lib/studio-build";
import {
  configuredEnvNames,
  type StudioAbout,
} from "@/lib/studio-config-reference";
import { sdkVersion, studioVersion } from "@/lib/studio-version";

// force-dynamic: the environment is a runtime fact of THIS process (Helm,
// a container, a shell), read per request so the answer is never a build's
// stale snapshot.
export const dynamic = "force-dynamic";

/**
 * `GET /api/studio/about` → {@link StudioAbout}: Studio's own version, the
 * SDK version and the build stamp, the managed/external server mode, and
 * which of the configuration reference's environment variables THIS
 * deployment has set — names and booleans only, never a value (a secret is
 * reported exactly like any other name). Same-origin only, like the
 * proxies: a foreign Host or Origin is a 403.
 */
export function GET(request: Request): Response {
  if (!requestIsTrusted(request)) {
    return Response.json(
      { error: "request origin is not allowed" },
      { status: 403 },
    );
  }
  const body: StudioAbout = {
    version: studioVersion(),
    sdkVersion: sdkVersion(),
    build: studioBuild(),
    mode: process.env.MECATL_BASE_URL?.trim() ? "external" : "managed",
    configured: configuredEnvNames(process.env),
  };
  return Response.json(body, { headers: { "Cache-Control": "no-store" } });
}
