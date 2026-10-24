import { useCallback, useEffect, useEffectEvent, useState } from "react";

import { FileEntry, Space, Volume, listEntries, listSpaces, listVolumes } from "../api";
import { transferStore } from "../transfers/store";
import { FileFolder, messageOf } from "./model";

// useFileListing follows the open space and folder: the spaces there are,
// the folder's entries and the data volume, each read again as it changes.
export function useFileListing() {
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [spaceID, setSpaceID] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [volume, setVolume] = useState<Volume>();
  const [trail, setTrail] = useState<FileFolder[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const parentID = trail.at(-1)?.id ?? "";

  const list = useCallback(
    (selected: string, parent: string) =>
      listEntries(selected, parent)
        .then((items) => {
          setEntries(items);
          setError("");
        })
        .catch((caught: unknown) => {
          setError(messageOf(caught));
        })
        .finally(() => setLoading(false)),
    [],
  );
  const refresh = useCallback(
    async (selected: string, parent: string) => {
      if (!selected) {
        setEntries([]);
        return;
      }
      setLoading(true);
      return list(selected, parent);
    },
    [list],
  );
  const refreshVolume = useCallback(
    () =>
      listVolumes()
        .then((volumes) => setVolume(volumes.find((item) => item.id === "volume:data") ?? volumes[0]))
        .catch(() => setVolume(undefined)),
    [],
  );
  const loadSpaces = useCallback(
    () =>
      listSpaces()
        .then((items) => {
          setSpaces(items);
          setSpaceID((current) => (items.some((space) => space.id === current) ? current : (items[0]?.id ?? "")));
        })
        .catch((caught: unknown) => {
          setError(messageOf(caught));
        }),
    [],
  );
  useEffect(() => {
    void loadSpaces();
    void refreshVolume();
  }, [loadSpaces, refreshVolume]);
  // Each folder opened lists at once.
  const [listedFor, setListedFor] = useState<{ spaceID: string; parentID: string }>();
  if (listedFor?.spaceID !== spaceID || listedFor.parentID !== parentID) {
    setListedFor({ spaceID, parentID });
    if (spaceID) setLoading(true);
  }
  useEffect(() => {
    if (spaceID) void list(spaceID, parentID);
  }, [spaceID, parentID, list]);
  // Files uploaded into this space show up without a manual refresh.
  const uploadedHere = useEffectEvent((spaceId: string) => spaceId === spaceID);
  const refreshHere = useEffectEvent(() => {
    void refresh(spaceID, parentID);
    void refreshVolume();
  });
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const stop = transferStore.onUploaded((target) => {
      if (!uploadedHere(target.spaceId)) return;
      clearTimeout(timer);
      timer = setTimeout(() => refreshHere(), 400);
    });
    return () => {
      stop();
      clearTimeout(timer);
    };
  }, []);

  return {
    spaces,
    spaceID,
    setSpaceID,
    entries,
    volume,
    trail,
    setTrail,
    parentID,
    loading,
    error,
    setError,
    loadSpaces,
    // reload lists the open folder again.
    reload: () => refresh(spaceID, parentID),
    refreshVolume,
  };
}
