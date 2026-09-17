import { loadOperatorPalettes } from "@/lib/operator-palettes";
import { requestIsTrusted } from "@/lib/request-trust";

// force-dynamic: STUDIO_PALETTE_DIR is a runtime env var (Helm/Kubernetes),
// and the directory is re-read per request so an edited palette shows up
// without a rebuild — the same posture as /brand/logo.
export const dynamic = "force-dynamic";

/**
 * `GET /api/palettes` → `{ palettes: PaletteDocument[] }`: the operator's
 * palettes from `STUDIO_PALETTE_DIR`, validated server-side (broken files
 * are skipped with a server log line and never described to the browser).
 * Unset → `{ palettes: [] }`. Same-origin only, like the proxies.
 */
export async function GET(request: Request): Promise<Response> {
  if (!requestIsTrusted(request)) {
    return Response.json(
      { error: "request origin is not allowed" },
      { status: 403 },
    );
  }
  // A literal read: studio-config-reference.test.ts scans for exactly this
  // form, so the knob cannot leave the in-app configuration reference.
  const palettes = await loadOperatorPalettes(process.env.STUDIO_PALETTE_DIR);
  return Response.json(
    { palettes },
    { headers: { "Cache-Control": "no-store" } },
  );
}
