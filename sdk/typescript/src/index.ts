/**
 * Transport-neutral mecatl SDK entry point.
 *
 * @packageDocumentation
 */

export type { Transport } from "@connectrpc/connect";
export type { CredentialOptions, CredentialProvider } from "./credentials.js";
export type {
  MecatlErrorCode,
  MecatlErrorOptions,
  SDKErrorCode,
  ServerErrorCode,
  TransportKind,
} from "./errors.js";
export {
  AuthenticationError,
  IncompatibleServerError,
  InvalidStateError,
  MECATL_ERROR_CODES,
  MecatlError,
  ProtocolError,
  ServerError,
  TransportError,
  UnsupportedFeatureError,
} from "./errors.js";
export type { HttpTransportOptions } from "./http.js";
export { createHttpTransport } from "./http.js";
export type { RawClient, RawClientOptions } from "./raw.js";
export { createRawClient, getRawJson, SUPPORTED_API_MAJOR } from "./raw.js";
