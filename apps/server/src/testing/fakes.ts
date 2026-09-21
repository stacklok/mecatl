// SPDX-License-Identifier: Apache-2.0

import type { RuntimeResponse, ServerCapabilitiesResponse } from "@mecatl-studio/contracts";
import type {
  ConnectionStatus,
  ConnectionStatusListener,
  ServerCompatibility,
} from "@stacklok-oss/mecatl-sdk";
import { vi } from "vitest";
import { createLogger, type Logger, type LogRecord } from "../log.js";
import type { MecatlRuntime, RuntimeClient } from "../mecatl/runtime.js";

export function memoryLogger(): { logger: Logger; records: LogRecord[] } {
  const records: LogRecord[] = [];
  return { logger: createLogger("debug", (record) => records.push(record)), records };
}

export function sampleCapabilities(): ServerCapabilitiesResponse {
  return {
    agents: true,
    audio: false,
    bash: true,
    debugMcp: false,
    image: true,
    learnedSkills: false,
    learningProposals: false,
    manualCompaction: true,
    mcp: true,
    mcpConnectorStatus: false,
    memory: true,
    modelSelection: true,
    posture: "strict",
    reflection: false,
    scheduling: true,
    sessionDebug: false,
    skills: true,
    slashCommands: true,
    soul: false,
    steer: true,
    storageCleanup: false,
    storageHealth: false,
    storageMigration: false,
    teams: true,
    userModel: false,
    workspaceEnrollment: false,
    worktrees: true,
  };
}

export function sampleCompatibility(): ServerCompatibility {
  return {
    apiMajor: 1,
    capabilities: sampleCapabilities(),
    deployment: "test",
    features: new Set(["server_info"]),
  } as unknown as ServerCompatibility;
}

export function sampleSnapshot(connection: ConnectionStatus = "online"): RuntimeResponse {
  return {
    apiMajor: 1,
    capabilities: sampleCapabilities(),
    connection,
    deployment: "test",
    features: ["server_info"],
    mock: false,
    source: "external",
  };
}

export interface FakeClient extends RuntimeClient {
  readonly compatibility: ReturnType<typeof vi.fn<() => Promise<ServerCompatibility>>>;
  emit(status: ConnectionStatus): void;
}

export function fakeClient(
  compatibility: () => Promise<ServerCompatibility> = async () => sampleCompatibility(),
): FakeClient {
  const listeners = new Set<ConnectionStatusListener>();
  let current: ConnectionStatus = "online";
  const compatibilityMock = vi.fn(compatibility);
  return {
    close: async () => undefined,
    compatibility: compatibilityMock,
    emit(status) {
      current = status;
      for (const listener of listeners) listener(status);
    },
    server: { compatibility: () => compatibilityMock() },
    status: {
      getSnapshot: () => current,
      subscribe(listener) {
        listeners.add(listener);
        return () => listeners.delete(listener);
      },
    },
  };
}

export function fakeRuntime(overrides: Partial<MecatlRuntime> = {}): MecatlRuntime {
  return {
    authMode: "none",
    client: fakeClient(),
    close: async () => undefined,
    ready: async () => undefined,
    runWithCredential: async (_accessToken, operation) => operation(),
    snapshot: () => sampleSnapshot(),
    verifyCredential: async () => undefined,
    ...overrides,
  };
}

/** Request headers that satisfy the same-origin + double-submit CSRF gate. */
export function csrfHeaders(token = "csrf-token", extra: Record<string, string> = {}) {
  return {
    Cookie: `studio_csrf=${token}`,
    "Sec-Fetch-Site": "same-origin",
    "X-Studio-CSRF": token,
    ...extra,
  };
}

export function cookieHeader(setCookies: readonly string[]): string {
  return setCookies
    .map((entry) => entry.split(";")[0] ?? "")
    .filter((pair) => !pair.endsWith("="))
    .join("; ");
}
