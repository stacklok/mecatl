/**
 * An in-memory `Storage` for tests that exercise localStorage/sessionStorage
 * preferences. This vitest environment ships a method-less storage shim
 * (Node's `--localstorage-file` stub shadows jsdom's), so a test stubs a real
 * Storage with `vi.stubGlobal("localStorage", memoryStorage())`; the global
 * `afterEach` in vitest.setup.ts unstubs it again.
 */
export function memoryStorage(): Storage {
  let store = new Map<string, string>();
  return {
    get length() {
      return store.size;
    },
    clear: () => {
      store = new Map();
    },
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => [...store.keys()][index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, String(value));
    },
  };
}
