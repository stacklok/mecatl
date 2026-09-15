"use client";

import { useCallback, useEffect, useState } from "react";
import {
  createHarnessSkill,
  createHarnessSkillFiles,
  type DisabledSkillInfo,
  deleteHarnessSkill,
  fetchHarnessSkillBody,
  fetchHarnessSkillFile,
  type HarnessSkillInfo,
  type HarnessSkillUploadFile,
  listDisabledHarnessSkills,
  listHarnessSkillFiles,
  listHarnessSkills,
  saveHarnessSkillBody,
  setHarnessSkillEnabled,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";

/**
 * The daemon's resolved skill inventory (`GET /v1/skills`) plus, in managed
 * mode, the controller-owned management surface: the `.disabled/` holding
 * area, SKILL.md body read/write, enable/disable, and delete.
 *
 * The inventory itself stays metadata-only by design: the model sees each
 * skill's name and one-line summary until it chooses to load one. Management
 * goes through the local controller (the daemon has no skill write API — its
 * skills snapshot is resolved once at startup), so every mutation may restart
 * the daemon and is unavailable in external mode (`manageable` is false and
 * the controller would answer 409 anyway).
 */
export function useAgentSkills() {
  const { connected, mode } = useRuntimeStatus();
  const manageable = mode === "managed";
  const [skills, setSkills] = useState<HarnessSkillInfo[]>([]);
  const [disabled, setDisabled] = useState<DisabledSkillInfo[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  // isLoading starts true and is never re-raised: post-action re-reads keep
  // the table rendered instead of flashing it back to skeletons.
  const load = useCallback(
    async (signal?: AbortSignal) => {
      // The disabled list is best-effort decoration: a controller hiccup (or
      // external mode racing the first status probe) must never break the
      // read-only inventory, so its failure renders as "no disabled skills".
      const disabledInventory = manageable
        ? listDisabledHarnessSkills(signal).catch(
            () => [] as DisabledSkillInfo[],
          )
        : Promise.resolve([] as DisabledSkillInfo[]);
      try {
        const inventory = await listHarnessSkills(signal);
        if (signal?.aborted) return;
        setSkills(inventory);
        setError(null);
      } catch (caught) {
        if (signal?.aborted) return;
        setSkills([]);
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        if (!signal?.aborted) setIsLoading(false);
      }
      const parked = await disabledInventory;
      if (!signal?.aborted) setDisabled(parked);
    },
    [manageable],
  );

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [connected, load]);

  const refresh = useCallback(async () => {
    await load();
  }, [load]);

  /** Runs one management action then re-reads durable state; refusals surface
   *  verbatim via `actionError`. Mutations restart the daemon, and the
   *  controller holds the response until it is back up, so the re-read lands
   *  on the restarted inventory. */
  const perform = useCallback(
    async (action: () => Promise<void>) => {
      setActionError(null);
      try {
        await action();
      } catch (caught) {
        setActionError(
          caught instanceof Error ? caught.message : String(caught),
        );
        throw caught;
      } finally {
        await load();
      }
    },
    [load],
  );

  /** Reads a skill's SKILL.md (works for disabled skills too). */
  const fetchBody = useCallback(
    (name: string, signal?: AbortSignal) => fetchHarnessSkillBody(name, signal),
    [],
  );

  /** Lists the files bundled in a skill's folder (managed mode only). */
  const fetchFiles = useCallback(
    (name: string, signal?: AbortSignal) => listHarnessSkillFiles(name, signal),
    [],
  );

  /** Reads one bundled text file from a skill's folder (managed mode only). */
  const fetchFile = useCallback(
    (name: string, path: string, signal?: AbortSignal) =>
      fetchHarnessSkillFile(name, path, signal),
    [],
  );

  /** Creates a new skill (enabled). Like every mutation, restarts the daemon. */
  const create = useCallback(
    async (name: string, body: string) => {
      await perform(() => createHarnessSkill(name, body));
    },
    [perform],
  );

  /** Creates a whole folder skill from a zip/folder upload's files. */
  const createFiles = useCallback(
    async (name: string, files: HarnessSkillUploadFile[]) => {
      await perform(() => createHarnessSkillFiles(name, files));
    },
    [perform],
  );

  const saveBody = useCallback(
    async (name: string, body: string) => {
      await perform(() => saveHarnessSkillBody(name, body));
    },
    [perform],
  );

  const setEnabled = useCallback(
    async (name: string, enabled: boolean) => {
      await perform(() => setHarnessSkillEnabled(name, enabled));
    },
    [perform],
  );

  const remove = useCallback(
    async (name: string) => {
      await perform(() => deleteHarnessSkill(name));
    },
    [perform],
  );

  return {
    skills,
    /** Skills parked in the controller's `.disabled/` holding area. */
    disabled,
    /** False in external mode: the deployment owns its skills dir. */
    manageable,
    isLoading: isLoading && connected,
    error,
    /** The controller's own words for a refused management action. */
    actionError,
    create,
    createFiles,
    fetchBody,
    fetchFiles,
    fetchFile,
    saveBody,
    setEnabled,
    remove,
    refresh,
  };
}
