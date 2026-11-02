export type Character = "naiwa" | "glow" | "chestnut";
export type Place = "home" | "desk" | "hidden";

// Only local presentation preferences. These keys never contain chat,
// filenames, photos or a NAS credential, and never imply cross-device sync.
export interface Preferences {
  character: Character;
  place: Place;
  quiet: boolean;
}

export const initialPreferences: Preferences = { character: "naiwa", place: "home", quiet: false };

export function preferenceKey(owner: string) {
  return `anas:companion:v1:${encodeURIComponent(owner)}:`;
}

export function readPreferences(owner: string, storage: Pick<Storage, "getItem">): Preferences {
  const prefix = preferenceKey(owner);
  try {
    const character = storage.getItem(`${prefix}character`);
    const place = storage.getItem(`${prefix}place`);
    return {
      character: character === "chestnut" || character === "glow" ? character : "naiwa",
      place: place === "desk" || place === "hidden" ? place : "home",
      quiet: storage.getItem(`${prefix}quiet`) === "true",
    };
  } catch {
    return { ...initialPreferences };
  }
}

export function writePreferences(owner: string, value: Preferences, storage: Pick<Storage, "setItem">) {
  const prefix = preferenceKey(owner);
  try {
    storage.setItem(`${prefix}character`, value.character);
    storage.setItem(`${prefix}place`, value.place);
    storage.setItem(`${prefix}quiet`, String(value.quiet));
  } catch {
    // Private browsing or a full disk must not prevent local play.
  }
}
