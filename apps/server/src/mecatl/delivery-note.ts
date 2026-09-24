// SPDX-License-Identifier: Apache-2.0

export interface DeliveryProjection {
  delivery: {
    fireId: string;
    kind: "started" | "completed";
    scheduleName: string;
    stop?: string;
  };
  text: string;
}

export function projectDeliveryNote(text: string): DeliveryProjection | undefined {
  const opener = "<<<UNTRUSTED\n";
  const closer = "\n<<<UNTRUSTED\n";
  if (!text.startsWith(opener) || !text.endsWith(closer)) return undefined;
  const content = text.slice(opener.length, -closer.length);
  // FenceUntrusted neutralizes markers inside its body, so an additional
  // literal marker means this is not a canonical renderer output.
  if (content.includes("<<<UNTRUSTED")) return undefined;

  const complete =
    /^\[scheduled task ([\s\S]+?) \(fire ([^)\n]+)\) completed with stop reason: ([^\]\n]+)\]\n([\s\S]+)$/.exec(
      content,
    );
  if (complete) {
    const [, scheduleName, fireId, stop, body] = complete;
    if (scheduleName?.trim() && fireId?.trim() && stop?.trim() && body !== undefined) {
      return { delivery: { fireId, kind: "completed", scheduleName, stop }, text: body };
    }
  }

  const start = /^\[scheduled task ([\s\S]+) started \(fire ([^)\n]+)\)\]$/.exec(content);
  if (!start) return undefined;
  const [, scheduleName, fireId] = start;
  if (!scheduleName?.trim() || !fireId?.trim()) return undefined;
  return { delivery: { fireId, kind: "started", scheduleName }, text: "" };
}
