import { describe, expect, it } from "vitest";
import {
  isDefaultOption,
  liveModelProvenance,
  modelProvenanceLabel,
  resolveDraftModel,
} from "./draft-model";

const options = [
  { id: "", label: "Default model" },
  { id: "m1", label: "Model One", providerId: "p" },
  { id: "m2", label: "Model Two", providerId: "p" },
  { id: "shared", label: "Shared id", providerId: "other" },
];

/**
 * The one resolver the picker's label and the workspace's create call both
 * use: an explicit pick wins, an untouched draft takes the Studio default
 * only when the inventory lists it, and a vanished default is REPORTED (so
 * the workspace can warn once) rather than silently degraded or rewritten.
 */
describe("resolveDraftModel", () => {
  const studioDefault = { modelId: "m2", providerId: "p" };

  it("an explicit pick wins over the Studio default", () => {
    expect(resolveDraftModel("m1", studioDefault, options)).toEqual({
      id: "m1",
      providerId: "p",
      provenance: "pick",
      staleDefault: false,
    });
  });

  it("an explicit auto pick is a pick for the daemon default, not the Studio default", () => {
    expect(resolveDraftModel("", studioDefault, options)).toEqual({
      id: "",
      provenance: "pick",
      staleDefault: false,
    });
  });

  it("applies the Studio default to an untouched draft when the inventory lists it", () => {
    expect(resolveDraftModel(null, studioDefault, options)).toEqual({
      id: "m2",
      providerId: "p",
      provenance: "studio-default",
      staleDefault: false,
    });
  });

  it("matches the default by provider as well as id", () => {
    expect(
      resolveDraftModel(null, { modelId: "shared", providerId: "p" }, options),
    ).toMatchObject({
      id: "",
      provenance: "daemon-default",
      staleDefault: true,
    });
  });

  it("flags a default the loaded inventory lacks and falls back to the daemon default", () => {
    expect(
      resolveDraftModel(null, { modelId: "gone", providerId: "p" }, options),
    ).toEqual({ id: "", provenance: "daemon-default", staleDefault: true });
  });

  it("does not call a default stale while the inventory is empty (still loading / offline)", () => {
    expect(resolveDraftModel(null, studioDefault, [{ id: "" }])).toEqual({
      id: "",
      provenance: "daemon-default",
      staleDefault: false,
    });
    expect(resolveDraftModel(null, studioDefault, [])).toEqual({
      id: "",
      provenance: "daemon-default",
      staleDefault: false,
    });
  });

  it("degrades a pick that left the inventory to the daemon default", () => {
    expect(resolveDraftModel("gone", studioDefault, options)).toEqual({
      id: "",
      provenance: "daemon-default",
      staleDefault: false,
    });
  });

  it("untouched with no default is the daemon default", () => {
    expect(resolveDraftModel(null, null, options)).toEqual({
      id: "",
      provenance: "daemon-default",
      staleDefault: false,
    });
  });
});

describe("liveModelProvenance", () => {
  const studioDefault = { modelId: "m2", providerId: "p" };

  it("reads an empty model as the daemon default", () => {
    expect(liveModelProvenance("", studioDefault, options)).toBe(
      "daemon-default",
    );
  });

  it("reads the Studio default's model as the Studio default", () => {
    expect(liveModelProvenance("m2", studioDefault, options)).toBe(
      "studio-default",
    );
    // An unlisted id equal to the default is trusted by id.
    expect(liveModelProvenance("m2", studioDefault, [])).toBe("studio-default");
  });

  it("reads a same id on a different provider as a pick", () => {
    expect(
      liveModelProvenance(
        "shared",
        { modelId: "shared", providerId: "p" },
        options,
      ),
    ).toBe("pick");
  });

  it("reads any other model as a pick", () => {
    expect(liveModelProvenance("m1", studioDefault, options)).toBe("pick");
    expect(liveModelProvenance("m1", null, options)).toBe("pick");
  });
});

describe("isDefaultOption / modelProvenanceLabel", () => {
  it("never marks the auto row as the default", () => {
    expect(isDefaultOption({ id: "" }, { modelId: "", providerId: "p" })).toBe(
      false,
    );
  });

  it("labels the three provenances", () => {
    expect(modelProvenanceLabel("pick")).toBe("your pick");
    expect(modelProvenanceLabel("studio-default")).toBe("Studio default");
    expect(modelProvenanceLabel("daemon-default")).toBe("daemon default");
  });
});
