// The film page's preview (M6-1). Playing the shots' proxies one after the
// other is the player feature's; the skeleton has where the playhead is and
// that it follows the selected shot: clicking a shot card only selects it,
// and the player, not the shot track, moves to its start.
import { useEffect } from "react";

import type { Shot } from "../api/client";
import { useCut, useCutStore } from "./cutStore";
import { useTimeline } from "./features";

// startOf is where the shot in slot starts, if the film has one there.
const startOf = (shots: Shot[] | undefined, slot: number | null) => shots?.find((s) => s.slot === slot)?.start;

export function usePlayer(workId: string) {
  const store = useCutStore();
  const playhead = useCut((state) => state.playhead);
  const shots = useTimeline(workId, false).data?.shots;
  const seekToSlot = (slot: number) => {
    const start = startOf(shots, slot);
    if (start !== undefined) store.set({ playhead: start });
  };
  return { playhead, seekToSlot };
}

/** Mounted once by the film page: the playhead goes to a shot when it is selected. */
export function useFollowSelection(workId: string) {
  const store = useCutStore();
  const selected = useCut((state) => state.selectedSlot);
  const shots = useTimeline(workId, false).data?.shots;
  useEffect(() => {
    const start = startOf(shots, selected);
    if (start !== undefined) store.set({ playhead: start });
  }, [selected, shots, store]);
}
