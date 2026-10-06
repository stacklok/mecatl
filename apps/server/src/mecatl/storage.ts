// SPDX-License-Identifier: Apache-2.0

import type { StorageHealthResponse } from "@mecatl-studio/contracts";
import type { Client } from "@stacklok-oss/mecatl-sdk";

export interface StorageService {
  readonly supported: boolean;
  getHealth(): Promise<StorageHealthResponse>;
}

export function createMecatlStorageService(
  client: Client,
  supported: boolean | (() => boolean),
): StorageService {
  const isSupported = typeof supported === "function" ? supported : () => supported;

  return {
    get supported() {
      return isSupported();
    },

    async getHealth() {
      if (!isSupported()) {
        return {
          activeJob: false,
          available: false,
          childCount: "0",
          corruptCount: "0",
          currentBytes: null,
          lastFailure: false,
          mainCount: "0",
          reclaimableBytes: null,
          scheduledCount: "0",
          sessionCount: "0",
          supported: false,
          unknownCount: "0",
        };
      }

      const health = await client.storage.getHealth({
        $typeName: "mecatl.v1.GetStorageHealthRequest",
      });
      return {
        activeJob: health.activeJob !== "",
        available: health.available,
        childCount: health.childCount.toString(),
        corruptCount: health.corruptCount.toString(),
        currentBytes: health.currentBytesAvailable ? health.currentBytes.toString() : null,
        lastFailure: health.lastFailure !== "",
        mainCount: health.mainCount.toString(),
        reclaimableBytes: health.reclaimableBytesAvailable
          ? health.reclaimableBytes.toString()
          : null,
        scheduledCount: health.scheduledCount.toString(),
        sessionCount: health.sessionCount.toString(),
        supported: true,
        unknownCount: health.unknownCount.toString(),
      };
    },
  };
}
