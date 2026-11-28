// What this device has already told the member, kept in localStorage under
// one key per kind: the last 200, so the list does not grow without end.
// Without storage, as in a private window, it may be told again next visit.

const LIMIT = 200;

export function toldList(storageKey: string) {
  const read = (): string[] => {
    try {
      return JSON.parse(localStorage.getItem(storageKey) ?? "[]") as string[];
    } catch {
      return [];
    }
  };
  const write = (told: string[]) => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(told.slice(-LIMIT)));
    } catch {
      // Storage is optional.
    }
  };
  return {
    /** Everything told so far. */
    read: () => new Set(read()),
    /** Remembers keys as told. */
    remember: (keys: string[]) => write([...new Set([...read(), ...keys])]),
    /** Whether key is news, remembering it if so. */
    tell: (key: string): boolean => {
      const told = read();
      if (told.includes(key)) return false;
      write([...told, key]);
      return true;
    },
  };
}
