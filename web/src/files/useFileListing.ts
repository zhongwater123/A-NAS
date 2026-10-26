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

  // A completed operation invalidates the open listing, even if its caller
  // retained reload before navigating elsewhere. The effect owns the request.
  const [revision, setRevision] = useState(0);
  const reload = useCallback(() => setRevision((value) => value + 1), []);
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
  // Never show the previous folder's entries under the new breadcrumb.
  const [listedFor, setListedFor] = useState<{ spaceID: string; parentID: string; revision: number }>();
  if (listedFor?.spaceID !== spaceID || listedFor.parentID !== parentID || listedFor.revision !== revision) {
    if (listedFor?.spaceID !== spaceID || listedFor.parentID !== parentID) setEntries([]);
    setListedFor({ spaceID, parentID, revision });
    setLoading(Boolean(spaceID));
    setError("");
  }
  useEffect(() => {
    if (!spaceID) return;
    let live = true;
    void listEntries(spaceID, parentID)
      .then((items) => {
        if (live) {
          setEntries(items);
          setError("");
        }
      })
      .catch((caught: unknown) => {
        if (live) setError(messageOf(caught));
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [spaceID, parentID, revision]);
  // Files uploaded into this space show up without a manual refresh.
  const uploadedHere = useEffectEvent((spaceId: string) => spaceId === spaceID);
  const refreshHere = useEffectEvent(() => {
    reload();
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
    reload,
    refreshVolume,
  };
}
