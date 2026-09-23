// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { managementNotes } from "./management-notes";

describe("managementNotes", () => {
  it("names each deployment-managed control with the reason the BFF gives", () => {
    expect(
      managementNotes({
        providerConfiguration: false,
        providerConfigurationReason: "Providers are configured by the operator.",
        routingConfiguration: false,
        routingConfigurationReason: "",
      }),
    ).toEqual([
      "Providers are configured by the operator.",
      "Model routing is managed by the deployment.",
    ]);
    expect(
      managementNotes({
        providerConfiguration: true,
        providerConfigurationReason: "",
        routingConfiguration: true,
        routingConfigurationReason: "",
      }),
    ).toEqual([]);
  });
});
