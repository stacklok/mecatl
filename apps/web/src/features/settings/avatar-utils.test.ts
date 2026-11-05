// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { avatarImageBlob, scaledAvatarSize } from "./avatar-utils";

describe("scaledAvatarSize", () => {
  it("preserves small images and scales the longest edge", () => {
    expect(scaledAvatarSize(128, 96)).toEqual({ height: 96, width: 128 });
    expect(scaledAvatarSize(2_000, 1_000)).toEqual({ height: 256, width: 512 });
    expect(scaledAvatarSize(500, 1_000)).toEqual({ height: 512, width: 256 });
  });

  it("never returns an empty canvas", () => {
    expect(scaledAvatarSize(0, 0)).toEqual({ height: 1, width: 1 });
  });
});

describe("avatarImageBlob", () => {
  it("decodes inline base64 image data URLs into typed image bytes", async () => {
    const blob = avatarImageBlob("data:image/jpeg;base64,aGk=");
    if (!blob) throw new Error("expected an image blob");
    expect(blob.type).toBe("image/jpeg");
    expect(new Uint8Array(await blob.arrayBuffer())).toEqual(new Uint8Array([104, 105]));
  });

  it("rejects other schemes, non-image or unknown types, and malformed payloads", () => {
    expect(avatarImageBlob("javascript:alert(1)")).toBeUndefined();
    expect(avatarImageBlob("https://example.test/a.png")).toBeUndefined();
    expect(avatarImageBlob("data:text/html;base64,PHA+")).toBeUndefined();
    expect(avatarImageBlob("data:image/svg;base64,PHA+")).toBeUndefined();
    expect(avatarImageBlob("data:image/png;base64,<script>")).toBeUndefined();
    expect(avatarImageBlob("data:image/png,raw")).toBeUndefined();
  });
});
