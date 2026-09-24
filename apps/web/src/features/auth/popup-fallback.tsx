// SPDX-License-Identifier: Apache-2.0

import { ExternalLink, RefreshCw } from "lucide-react";
import { Button } from "../../components/ui/button";
import { useAuthRecovery } from "./auth-recovery-context";

/** A manual new tab keeps the original workspace and its in-memory draft mounted. */
export function PopupFallback() {
  const { loginUrl, startPopupLogin } = useAuthRecovery();
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <Button onClick={startPopupLogin} size="sm" variant="outline">
        <RefreshCw aria-hidden="true" /> Retry sign-in
      </Button>
      <Button asChild size="sm" variant="outline">
        <a href={loginUrl} rel="noopener noreferrer" target="_blank">
          <ExternalLink aria-hidden="true" /> Open sign-in in a new tab
        </a>
      </Button>
    </span>
  );
}
