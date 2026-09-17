/**
 * Studio's configuration surface as DATA: every environment variable the
 * server tier (the Next route handlers and proxies), the managed-mode local
 * controller (`scripts/local-controller.mjs`) or the build reads. Settings →
 * Help & about renders it as the in-app configuration reference — the web
 * analogue of mecatui's `--help-flags` — and `GET /api/studio/about`
 * reports which names THIS deployment has set (booleans only, never a
 * value), so the table cannot drift from what the route answers.
 *
 * `studio-config-reference.test.ts` scans the source for environment reads
 * (`process.env.<NAME>`) and fails when one is missing here, so a new knob
 * lands in the reference together with the code that reads it. The full
 * prose stays in studio/CLAUDE.md; a purpose here is one or two plain
 * sentences.
 */

/** Which process reads the variable, and when. */
type StudioEnvTier = "server" | "controller" | "build";

export interface StudioEnvEntry {
  readonly name: string;
  readonly tier: StudioEnvTier;
  /** What it does and its default, in one or two plain sentences. */
  readonly purpose: string;
  /** A credential: the reference says "Set (hidden)" and nothing more. */
  readonly secret: boolean;
}

export const STUDIO_ENV_REFERENCE: readonly StudioEnvEntry[] = [
  // ---- Server tier: read by the Next server (proxies, auth, branding). ----
  {
    name: "MECATL_BASE_URL",
    tier: "server",
    purpose:
      "Base URL of the external mecated deployment the /api/mecatl proxy forwards to. Set: external mode. Unset: managed mode, where the local controller spawns mecated.",
    secret: false,
  },
  {
    name: "MECATL_AUTH_TOKEN",
    tier: "server",
    purpose:
      "Bearer credential for the daemon. External mode: injected server-side on every proxied call. Managed mode: handed to the spawned mecated (a random token when unset). Never sent to the browser.",
    secret: true,
  },
  {
    name: "MECATL_WORKSPACE",
    tier: "server",
    purpose:
      "External mode only: the workspace path /api/mecatl-control/status reports as the deployment's placement label. Display only — placement is server-owned and Studio never sends it to the daemon.",
    secret: false,
  },
  {
    name: "MECATL_STUDIO_PUBLIC_ORIGIN",
    tier: "server",
    purpose:
      "Comma-separated origins Studio is served from; default http://localhost:3000,http://127.0.0.1:3000. A request reaching /api/* from another Host or Origin is refused (the DNS-rebinding and CSRF check). In development these hosts may also load the dev server's assets.",
    secret: false,
  },
  {
    name: "MECATL_OIDC_ISSUER",
    tier: "server",
    purpose:
      "OIDC issuer URL for signing in to a remote daemon (external mode). Set together with MECATL_OIDC_CLIENT_ID; one without the other is reported on Settings → Provider and ignored.",
    secret: false,
  },
  {
    name: "MECATL_OIDC_CLIENT_ID",
    tier: "server",
    purpose: "OIDC client id for remote-daemon sign-in.",
    secret: false,
  },
  {
    name: "MECATL_OIDC_AUDIENCE",
    tier: "server",
    purpose: "Audience requested with the OIDC token. Optional.",
    secret: false,
  },
  {
    name: "MECATL_OIDC_SCOPE",
    tier: "server",
    purpose:
      "Scopes requested at sign-in; default openid profile email offline_access.",
    secret: false,
  },
  {
    name: "MECATL_OIDC_REDIRECT_URI",
    tier: "server",
    purpose:
      "Callback URL the identity provider returns to; default derived from the public origin.",
    secret: false,
  },
  {
    name: "MECATL_OIDC_DISCOVERY",
    tier: "server",
    purpose:
      "1: discover the issuer and client id from the deployment's protected-resource metadata (RFC 9728) instead of explicit values. Needs MECATL_BASE_URL and no explicit issuer or client id.",
    secret: false,
  },
  {
    name: "MECATL_OIDC_CALLBACK_TIMEOUT",
    tier: "server",
    purpose:
      "Whole seconds a started sign-in stays valid before its callback is refused; default 600, clamped to 30 s – 24 h.",
    secret: false,
  },
  {
    name: "MECATL_AUTH_PREFER_STATIC",
    tier: "server",
    purpose:
      "1: send the static MECATL_AUTH_TOKEN instead of the signed-in OIDC token when both are configured. Default off.",
    secret: false,
  },
  {
    name: "MECATL_AUTH_ANONYMOUS",
    tier: "server",
    purpose:
      "1: call the daemon with no credential at all, so a daemon that requires one answers its own 401. Default off.",
    secret: false,
  },
  {
    name: "BRAND_NAME",
    tier: "server",
    purpose:
      "Product name used as the logo's alternative text; default Stacklok. Read when the page renders, so a prerendered route captures it at build time.",
    secret: false,
  },
  {
    name: "BRAND_LOGO_URL",
    tier: "server",
    purpose:
      "URL of a logo image served at /brand/logo instead of the built-in one.",
    secret: false,
  },
  {
    name: "FAVICON_URL",
    tier: "server",
    purpose: "URL of a favicon served instead of the built-in one.",
    secret: false,
  },
  {
    name: "BRAND_PALETTE",
    tier: "server",
    purpose:
      "The palette a browser with no stored choice starts with; default the built-in one.",
    secret: false,
  },
  {
    name: "STUDIO_PALETTE_DIR",
    tier: "server",
    purpose:
      "Directory of operator palette JSON files served by /api/palettes; re-read on every request, so an edited file shows up without a rebuild.",
    secret: false,
  },
  // ---- Local controller: managed mode's mecated supervisor. ----
  {
    name: "MECATL_STUDIO_PROVIDER",
    tier: "controller",
    purpose:
      "Pins the LLM provider the managed mecated starts with (a known provider kind). Wins at boot over the provider saved from Settings → Provider.",
    secret: false,
  },
  {
    name: "MECATL_STUDIO_STORE_DIR",
    tier: "controller",
    purpose:
      "Session store directory for the managed mecated, absolute or relative to the workspace; default .scratch/studio-sessions. Settings → Storage saves an override.",
    secret: false,
  },
  {
    name: "MECATL_STUDIO_NO_STORE",
    tier: "controller",
    purpose:
      "1: the managed mecated keeps sessions in memory (started without --store-dir).",
    secret: false,
  },
  {
    name: "MECATL_STUDIO_ORIGINS",
    tier: "controller",
    purpose:
      "Extra comma-separated origins the controller accepts requests from; the public origin is always allowed.",
    secret: false,
  },
  {
    name: "MECATL_ALLOW_INSECURE_LOOPBACK_MCP",
    tier: "controller",
    purpose:
      "1: the MCP gateway may connect to a plain-HTTP loopback server. HTTPS is required otherwise.",
    secret: false,
  },
  {
    name: "MECATL_PRODUCT_METRICS",
    tier: "controller",
    purpose:
      "false: the managed mecated starts with product metrics off. This environment opt-out (also DO_NOT_TRACK) always wins over the Settings → Diagnostics switch.",
    secret: false,
  },
  {
    name: "XDG_CONFIG_HOME",
    tier: "controller",
    purpose:
      "Where the controller finds mecatl's user configuration (<XDG_CONFIG_HOME>/mecatl/settings.yaml and auth.yaml); default ~/.config.",
    secret: false,
  },
  // ---- Build: read once at `next build`. ----
  {
    name: "MECATL_STUDIO_BUILD",
    tier: "build",
    purpose:
      "Names Studio's build stamp (a CI release name); default <package version>+<short git sha>, or +dev when the build tree has no .git.",
    secret: false,
  },
];

/**
 * Which reference names are set in `env` — `true` when the value is
 * non-blank. Names and booleans only: the value itself never leaves the
 * server, secret or not, and a secret is reported like any other name.
 */
export function configuredEnvNames(
  env: Readonly<Record<string, string | undefined>>,
): Record<string, boolean> {
  return Object.fromEntries(
    STUDIO_ENV_REFERENCE.map((entry) => [
      entry.name,
      Boolean(env[entry.name]?.trim()),
    ]),
  );
}

/** What `GET /api/studio/about` answers with. */
export interface StudioAbout {
  /** Studio's package version. */
  version: string;
  /** The TypeScript SDK version this build was compiled against. */
  sdkVersion: string;
  /** The bug-report build stamp (`studioBuild()`). */
  build: string;
  mode: "managed" | "external";
  /** {@link configuredEnvNames} over the server's environment. */
  configured: Record<string, boolean>;
}
