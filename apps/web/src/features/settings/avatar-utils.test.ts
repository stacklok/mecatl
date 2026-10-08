// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { scaledAvatarSize } from "./avatar-utils";

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
