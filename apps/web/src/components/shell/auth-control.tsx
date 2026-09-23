// SPDX-License-Identifier: Apache-2.0

import { logoutAuthSession } from "@mecatl-studio/contracts/generated";
import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogOut } from "lucide-react";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { Button } from "../ui/button";

/**
 * Sign-in status and sign-out, surfaced inside Settings rather than the top
 * nav — the nav carries no status chips (Studio's rule), and Settings is
 * where Studio itself discloses sign-in state.
 */
export function AuthControl() {
  const queryClient = useQueryClient();
  const session = useQuery(getAuthSessionOptions());

  if (session.data?.status !== "authenticated") return null;

  return (
    <div className="flex items-center justify-between gap-3 py-3">
      <div>
        <p className="text-sm font-medium">Signed in</p>
        <p className="text-sm text-muted-foreground">You're signed in to this Mecatl deployment.</p>
      </div>
      <Button
        onClick={async () => {
          await logoutAuthSession({ throwOnError: true });
          queryClient.clear();
          clearUserScopedStorage();
          window.location.assign("/");
        }}
        variant="outline"
      >
        <LogOut aria-hidden="true" />
        Sign out
      </Button>
    </div>
  );
}
