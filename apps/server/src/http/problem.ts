// SPDX-License-Identifier: Apache-2.0

import { problemDetailsSchema } from "@mecatl-studio/contracts";
import type { Context } from "hono";
import type { ContentfulStatusCode } from "hono/utils/http-status";

export const maximumDetailLength = 400;

export function problem<const TStatus extends ContentfulStatusCode>(
  context: Context,
  status: TStatus,
  code: string,
  title: string,
  detail: string,
) {
  const body = problemDetailsSchema.parse({
    code,
    detail: clampDetail(detail),
    instance: context.req.path,
    status,
    title,
    type: `urn:mecatl-studio:problem:${code}`,
  });

  return context.json(body, status, {
    "Content-Type": "application/problem+json",
  });
}

/**
 * AC2.8: a `detail` derived from an upstream error is bounded and never
 * carries bearer material or upstream addresses. Callers pass the raw message;
 * URLs and bearer-shaped tokens are redacted before clamping.
 */
export function sanitizeUpstreamDetail(message: string, secrets: readonly string[] = []): string {
  let detail = message;
  for (const secret of secrets) {
    if (secret !== "") detail = detail.split(secret).join("[redacted]");
  }
  detail = detail
    .replace(/\b[Bb]earer\s+[A-Za-z0-9\-._~+/]+=*/gu, "Bearer [redacted]")
    .replace(/\b(?:https?|grpcs?|wss?):\/\/[^\s"'<>]+/gu, "[upstream]")
    .replace(/\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b/gu, "[upstream]");
  return clampDetail(detail);
}

function clampDetail(detail: string): string {
  const runes = [...detail];
  return runes.length <= maximumDetailLength
    ? detail
    : `${runes.slice(0, maximumDetailLength - 1).join("")}…`;
}

/**
 * A validation `detail` naming the first few invalid fields. The values are
 * the caller's own input, so the paths and messages are safe to return.
 */
export function validationDetail(
  issues: readonly { readonly message: string; readonly path: readonly PropertyKey[] }[],
): string {
  const named = issues.slice(0, 3).map((issue) => {
    const path = issue.path.map(String).join(".");
    return path === "" ? issue.message : `${path}: ${issue.message}`;
  });
  const more = issues.length > 3 ? ` (and ${issues.length - 3} more)` : "";
  return `${named.join("; ") || "The request is invalid."}${more}`;
}
