import type { Interceptor } from "@connectrpc/connect";
import type { TransportKind } from "./errors.js";
import { AuthenticationError } from "./errors.js";

/** @public */
export type CredentialProvider = () => HeadersInit | Promise<HeadersInit>;

/** @public */
export interface CredentialOptions {
  /** Headers copied once at transport construction. */
  headers?: HeadersInit;
  /** Invoked for every request, after static headers have been copied. */
  credentialProvider?: CredentialProvider;
}

export async function credentialHeaders(
  options: CredentialOptions,
  transport: TransportKind,
  requestHeaders?: HeadersInit,
): Promise<Headers> {
  const headers = new Headers(options.headers);
  new Headers(requestHeaders).forEach((value, key) => {
    headers.set(key, value);
  });
  if (options.credentialProvider === undefined) return headers;
  try {
    new Headers(await options.credentialProvider()).forEach((value, key) => {
      headers.set(key, value);
    });
    return headers;
  } catch (cause) {
    throw new AuthenticationError("The credential provider failed", { cause, transport });
  }
}

export function credentialInterceptor(options: CredentialOptions): Interceptor {
  return (next) => async (request) => {
    const headers = await credentialHeaders(options, "grpc", request.header);
    headers.forEach((value, key) => {
      request.header.set(key, value);
    });
    return next(request);
  };
}
