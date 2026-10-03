// SPDX-License-Identifier: Apache-2.0

import type { SessionWorktreesResponse, SoulInspectionResponse } from "@mecatl-studio/contracts";
import type { Client } from "@stacklok-oss/mecatl-sdk";

export interface InspectionService {
  soul(): Promise<SoulInspectionResponse>;
  worktrees(sessionId: string): Promise<SessionWorktreesResponse>;
}

/** Projects only the public inspection fields from the published SDK. */
export function createMecatlInspectionService(client: Client): InspectionService {
  return {
    async soul() {
      const response = await client.soul.get({ $typeName: "mecatl.v1.GetSoulRequest" });
      const soul = response.soul;
      if (soul === undefined) throw new Error("Mecatl returned no soul snapshot");
      return {
        content: soul.content,
        drifted: soul.drifted,
        present: soul.present,
        provenance: soulProvenance(soul.provenance),
        sha256: soul.sha256,
        sizeBytes: soul.sizeBytes.toString(),
        trusted: soul.trusted,
      };
    },

    async worktrees(sessionId) {
      const response = await client.worktrees.list({
        $typeName: "mecatl.v1.ListWorktreesRequest",
        sessionId,
      });
      return {
        items: response.worktrees.map((worktree) => ({
          bare: worktree.bare,
          branch: worktree.branch,
          kind: worktree.kind,
          label: worktree.label,
          revision: worktree.revision,
          selector: worktree.selector,
        })),
      };
    },
  };
}

function soulProvenance(provenance: number): SoulInspectionResponse["provenance"] {
  switch (provenance) {
    case 1:
      return "user";
    case 2:
      return "project";
    case 3:
      return "driver";
    default:
      return "unspecified";
  }
}
