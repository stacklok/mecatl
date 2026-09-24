// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { adoptSessionTitle } from "./session-title";

describe("session title adoption", () => {
  it("adopts only newer title revisions", () => {
    const generated = {
      id: "chat-1",
      title: "Generated title",
      titleProvenance: "generated",
      titleRevision: "9007199254740993",
    };
    const first = adoptSessionTitle(undefined, generated);
    expect(first).toEqual(generated);

    const renamed = {
      id: "chat-1",
      title: "Operator title",
      titleProvenance: "operator",
      titleRevision: "9007199254740994",
    };
    expect(adoptSessionTitle(first, renamed)).toEqual(renamed);
    expect(adoptSessionTitle(renamed, generated)).toEqual(renamed);
    expect(adoptSessionTitle(renamed, { ...generated, titleRevision: "9007199254740994" })).toEqual(
      renamed,
    );
    expect(adoptSessionTitle(renamed, { ...generated, titleRevision: "9007199254740995" })).toEqual(
      { ...generated, titleRevision: "9007199254740995" },
    );
    expect(adoptSessionTitle(renamed, { ...generated, id: "chat-2" })).toEqual(renamed);
    expect(
      adoptSessionTitle(renamed, { ...generated, title: "", titleRevision: "9999999999999999" }),
    ).toEqual(renamed);
    expect(adoptSessionTitle(renamed, { ...generated, titleRevision: "0" })).toEqual(renamed);
    expect(adoptSessionTitle(undefined, { ...generated, titleRevision: "0" })).toEqual({
      ...generated,
      titleRevision: "0",
    });
  });
});
