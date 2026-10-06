// SPDX-License-Identifier: Apache-2.0

/** Per-request variables the security middleware sets and every layer may read. */
export interface AppEnv {
  Variables: {
    /** Client address after honouring `STUDIO_TRUSTED_PROXY_HOPS`. */
    clientAddress: string;
    requestId: string;
  };
}
