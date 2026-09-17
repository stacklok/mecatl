/**
 * The daemon's external-authorization lifecycle grammar (engine/session
 * `AuthorizationStatus`): a closed set of statuses of which only `pending`
 * keeps a run parked. Everything else is terminal — the run either resumes
 * (granted) or records the parked call's failure and moves on.
 */

/** True while the authorization still waits on the operator's sign-in. An
 *  unset status reads as pending: the daemon never omits a terminal one. */
export function isPendingAuthorizationStatus(status: string): boolean {
  return status === "" || status === "pending";
}

/** Plain-language label for a terminal authorization status. */
export function authorizationStatusLabel(status: string): string {
  switch (status) {
    case "pending":
      return "still waiting";
    case "granted":
      return "signed in";
    case "denied":
      return "denied";
    case "cancelled":
      return "cancelled";
    case "expired":
      return "expired";
    case "interrupted":
      return "interrupted";
    case "failed":
      return "failed";
    case "closed":
      return "closed";
    default:
      return status || "resolved";
  }
}
