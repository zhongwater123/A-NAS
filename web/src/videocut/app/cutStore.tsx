// The film page's shared state (useCutStore): the timeline, the player, the
// inspector and the assistant never call one another; they read and write
// this. Each reads only the fields it names, so the playhead, which moves
// every frame, redraws only what shows it. The page's mode is the shell's
// alone to write, from the project's state.
import { createContext, type ReactNode, useContext, useState, useSyncExternalStore } from "react";

import type { CutMode } from "../lib/pages";

export interface CutState {
  selectedSlot: number | null; // the shot track and the assistant's candidates write; the inspector and player read
  activePart: number; // the parts' overview writes; the shot track reads
  playhead: number; // seconds into the film; the player writes, the tracks read
  busy: boolean; // an edit is being made: one at a time
  mode: CutMode; // from the project's state; the shell writes it
  sample: boolean; // the sample every member has: it plays, and nothing changes it
  waitingSlots: number[]; // slots waiting for a video from this computer (M7-4)
}

export interface CutStore {
  get: () => CutState;
  set: (change: Partial<CutState>) => void;
  subscribe: (listener: () => void) => () => void;
}

export function createCutStore(initial: Partial<CutState> = {}): CutStore {
  let state: CutState = {
    selectedSlot: null,
    activePart: 1,
    playhead: 0,
    busy: false,
    mode: "generating",
    sample: false,
    waitingSlots: [],
    ...initial,
  };
  const listeners = new Set<() => void>();
  return {
    get: () => state,
    set: (change) => {
      state = { ...state, ...change };
      for (const listener of listeners) listener();
    },
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}

// readOnly is whether edits are held back: while one is being made,
// whenever the film is not there to edit, and always for the sample.
export const readOnly = (state: CutState) => state.busy || state.mode !== "ready" || state.sample;

const StoreContext = createContext<CutStore | null>(null);

export function CutProvider({ store, children }: { store?: CutStore; children: ReactNode }) {
  const [own] = useState(() => store ?? createCutStore());
  return <StoreContext.Provider value={own}>{children}</StoreContext.Provider>;
}

export function useCutStore(): CutStore {
  const store = useContext(StoreContext);
  if (!store) throw new Error("useCutStore is for the film page, under CutProvider");
  return store;
}

/** One field, or a value made of the state, redrawn only when it changes. */
export function useCut<T>(pick: (state: CutState) => T): T {
  const store = useCutStore();
  return useSyncExternalStore(store.subscribe, () => pick(store.get()));
}
