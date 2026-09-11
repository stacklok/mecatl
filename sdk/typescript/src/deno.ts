/**
 * Deno mecatl SDK entry point.
 *
 * @packageDocumentation
 */

export type { Query, QueryOptions } from "./deno-query.js";
export { query } from "./deno-query.js";
export type { DaemonInfo, SpawnedClient, SpawnOptions } from "./deno-spawn.js";
export { spawn } from "./deno-spawn.js";
export * from "./index.js";
