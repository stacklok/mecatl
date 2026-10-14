// SPDX-License-Identifier: Apache-2.0

import { getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";

/**
 * Whether messages can steer a running agent. Only a daemon that says it cannot
 * (`capabilities.steer === false`) turns steering off; while the runtime is
 * loading or offline the stored preference stands, so the option does not flicker.
 */
export function useSteerAvailable(): boolean {
  const runtime = useQuery(getRuntimeOptions());
  return runtime.data?.capabilities.steer !== false;
}
