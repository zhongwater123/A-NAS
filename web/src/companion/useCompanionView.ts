import { useState } from "react";

import type { CompanionClientKind } from "../api.generated";
import type { Character } from "./preferences";
import { useCompanion } from "./store";
import { useRemote } from "./useRemote";

export function useCompanionView(owner: string, connected: boolean, kind: CompanionClientKind = "web") {
  const [value, update] = useCompanion(owner);
  const remote = useRemote(owner, connected, kind);
  const [localPlay, setLocalPlay] = useState(false);
  const local = !connected || localPlay;
  const character: Character = "naiwa";
  const quiet = local ? value.quiet : (remote.state?.quiet ?? value.quiet);
  return {
    value,
    update,
    remote,
    local,
    character,
    quiet,
    connected,
    canPlay: local || remote.here,
    playCount: local ? undefined : remote.state?.playCount,
    stageKey: `${local ? "local" : (remote.state?.active?.epoch ?? 0)}:${local || remote.here}`,
    claim: () => {
      setLocalPlay(false);
      void remote.sync?.claim();
    },
    playLocal: () => {
      update({ character, quiet });
      setLocalPlay(true);
    },
    play: () => {
      if (!local) void remote.sync?.act("ball");
    },
    rest: () => {
      if (!local) void remote.sync?.release();
    },
    toggleQuiet: () => {
      if (local) update({ quiet: !quiet });
      else void remote.sync?.act(quiet ? "wake" : "quiet");
    },
    choose: (chosen: Character) => {
      if (local) update({ character: chosen });
      else void remote.sync?.act("character", chosen);
    },
  };
}
export type CompanionView = ReturnType<typeof useCompanionView>;
