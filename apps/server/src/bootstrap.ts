// SPDX-License-Identifier: Apache-2.0

import { createApp } from "./app.js";
import { AuthenticationError, createAuthenticationService } from "./auth/service.js";
import { ConfigurationError, type StudioConfig, studioConfigFromEnvironment } from "./config.js";
import { createLogger, type Logger } from "./log.js";
import {
  type MecatlRuntime,
  type RuntimeDependencies,
  runtimeConfigFromEnvironment,
  startMecatlRuntime,
} from "./mecatl/runtime.js";

export interface BootstrapOptions {
  readonly environment?: Readonly<NodeJS.ProcessEnv>;
  readonly fetch?: typeof fetch;
  readonly logger?: Logger;
  readonly runtime?: RuntimeDependencies;
}

export interface Bootstrapped {
  readonly app: ReturnType<typeof createApp>;
  readonly config: StudioConfig;
  readonly logger: Logger;
  readonly runtime: MecatlRuntime;
}

/**
 * Startup, fail-closed: configuration errors, discovery failures other than a
 * clean `404`, and an unauthenticated runtime inside the image all throw
 * before the server listens.
 */
export async function bootstrap(options: BootstrapOptions = {}): Promise<Bootstrapped> {
  const environment = options.environment ?? process.env;
  const config = studioConfigFromEnvironment(environment);
  const logger = options.logger ?? createLogger(config.logLevel);
  const runtimeConfig = runtimeConfigFromEnvironment(environment);
  const runtime = await startMecatlRuntime(runtimeConfig, { logger, ...options.runtime });
  try {
    const authentication = await createAuthenticationService(runtimeConfig, {
      ...(options.fetch === undefined ? {} : { fetch: options.fetch }),
      logger,
      ...(config.publicUrl === undefined ? {} : { publicUrl: config.publicUrl }),
      ...(config.sessionSecret === undefined ? {} : { sessionSecret: config.sessionSecret }),
      verifyCredential: runtime.verifyCredential,
    });

    if (authentication === undefined) {
      // AC2.3: static-token and no-auth runtimes make every browser one principal.
      logger.warn("auth.unauthenticated_runtime", {
        mode: runtime.authMode,
        detail:
          runtime.authMode === "static"
            ? "every browser reaching Studio acts as the MECATL_AUTH_TOKEN principal"
            : "the mecatl runtime advertises no authentication; every browser is anonymous",
      });
      if (config.image && !config.allowUnauthenticated) {
        throw new ConfigurationError(
          "STUDIO_ALLOW_UNAUTHENTICATED",
          `required (=1) to run a ${runtime.authMode === "static" ? "static-token" : "no-auth"} runtime inside the Studio image`,
        );
      }
      if (config.sessionSecret === undefined) {
        logger.warn("auth.session_secret_unused", {
          detail:
            "STUDIO_SESSION_SECRET is not set; interactive login is inactive so none is needed",
        });
      }
    } else {
      runtime.authMode = "oidc";
      if (config.publicUrl === undefined) {
        logger.warn("auth.public_url_unset", {
          detail: "STUDIO_PUBLIC_URL is unset; only the 127.0.0.1:18473 development callback works",
        });
      }
    }

    return {
      app: createApp({
        activity: config.activity,
        ...(authentication === undefined ? {} : { authentication }),
        logger,
        runtime,
        security: {
          ...(config.publicUrl === undefined ? {} : { publicUrl: config.publicUrl }),
          rateLimit: config.rateLimit,
          trustedProxyHops: config.trustedProxyHops,
        },
        ...(config.webDist === undefined ? {} : { webDist: config.webDist }),
      }),
      config,
      logger,
      runtime,
    };
  } catch (error) {
    await runtime.close();
    throw error;
  }
}

/** Renders a startup failure for stderr, naming the variable when one is at fault. */
export function describeStartupFailure(error: unknown): string {
  if (error instanceof ConfigurationError) return `configuration error: ${error.message}`;
  if (error instanceof AuthenticationError) {
    return `authentication setup failed (${error.code}): ${error.message}`;
  }
  return error instanceof Error ? error.message : String(error);
}
