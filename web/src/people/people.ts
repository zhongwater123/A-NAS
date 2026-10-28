import { useSyncExternalStore } from "react";

import type { Profile } from "../api.generated";
import { listProfiles } from "./api";

// Every account's nickname and avatar, read once per desktop and shared by
// all windows, so a name looks the same in the log, the feedback list and
// the photo library. Records that only carry a username (who wrote a
// feedback item, who reset a password) are looked up here by that name.

export interface Person {
  id?: string;
  username: string;
  nickname?: string;
  avatarVersion?: string;
}

export interface People {
  byId: (id: string | undefined) => Profile | undefined;
  byUsername: (username: string | undefined) => Profile | undefined;
}

let current = directory([]);
const listeners = new Set<() => void>();

function directory(profiles: Profile[]): People & { profiles: Profile[] } {
  const ids = new Map(profiles.map((profile) => [profile.id, profile]));
  const names = new Map(profiles.map((profile) => [profile.username, profile]));
  return {
    profiles,
    byId: (id) => (id ? ids.get(id) : undefined),
    byUsername: (username) => (username ? names.get(username) : undefined),
  };
}

function publish(profiles: Profile[]) {
  current = directory(profiles);
  for (const listener of listeners) listener();
}

// loadPeople reads the profiles again. A failure keeps what was there:
// names then fall back to account names, which is never wrong, only plainer.
export async function loadPeople(): Promise<void> {
  try {
    publish(await listProfiles());
  } catch {
    /* keep the last directory */
  }
}

// rememberPerson updates one account after its owner changed it here, without
// another round trip.
export function rememberPerson(profile: Profile) {
  const { id, username, nickname, avatarVersion } = profile;
  const next = current.profiles.filter((item) => item.id !== id);
  publish([...next, { id, username, nickname, avatarVersion }]);
}

export function forgetPeople() {
  publish([]);
}

export function usePeople(): People {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    () => current,
  );
}

// person completes what a record knows about an account with its profile.
export function person(people: People, known: Person): Person {
  const profile = (known.id && people.byId(known.id)) || people.byUsername(known.username);
  return profile ? { ...known, ...profile } : known;
}

// displayName is the nickname if there is one, else the account name.
export function displayName(who: { username: string; nickname?: string }): string {
  return who.nickname || who.username;
}

// fullName keeps the account name beside a nickname where the account itself
// matters: managing users, and notices about what an administrator did.
export function fullName(who: { username: string; nickname?: string }): string {
  return who.nickname && who.nickname !== who.username ? `${who.nickname}（${who.username}）` : who.username;
}

// nameOf resolves a username from a record to the name to show.
export function nameOf(people: People, username: string): string {
  return displayName(people.byUsername(username) ?? { username });
}
