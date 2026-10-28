import { type Dispatch, type SetStateAction, useCallback, useEffect, useMemo, useState } from "react";

import { usePeople } from "../people/people";
import { listPhotoLibraries, type PhotoLibrary } from "./api";
import { type Caller, isUnavailable, messageOf, type Place, placeLibrary } from "./model";

// useLibraries loads the libraries the caller sees and keeps the place on one
// of them: a place whose library is gone gives way to the caller's own
// timeline. failure says why the photo service did not answer.
export function useLibraries({
  userId,
  isAdmin,
  setPlaceState,
}: {
  userId: string;
  isAdmin: boolean;
  setPlaceState: Dispatch<SetStateAction<Place | undefined>>;
}) {
  const [libraries, setLibraries] = useState<PhotoLibrary[]>();
  const [failure, setFailure] = useState("");
  // The photo service knows owners by account name; pages show nicknames.
  const people = usePeople();
  const named = useMemo(
    () =>
      libraries?.map((library) => {
        const owner = people.byId(library.ownerUserId);
        return owner?.nickname ? { ...library, ownerName: owner.nickname } : library;
      }),
    [libraries, people],
  );
  const caller: Caller = useMemo(() => ({ userId, isAdmin, libraries: named ?? [] }), [userId, isAdmin, named]);

  const loadLibraries = useCallback(
    () =>
      listPhotoLibraries()
        .then((items) => {
          setLibraries(items);
          setFailure("");
          setPlaceState((current) => {
            const library = current && placeLibrary(current);
            if (current && (!library || items.some((item) => item.id === library))) return current;
            const home =
              items.find((item) => item.kind === "private" && item.ownerUserId === userId && !item.viewing) ?? items[0];
            return home ? { kind: "timeline", libraryId: home.id } : undefined;
          });
        })
        .catch((caught: unknown) => {
          setFailure(
            isUnavailable(caught)
              ? "相册暂时不可用：数据卷未就绪，或此设备尚未启用相册。恢复后点击重试。"
              : messageOf(caught),
          );
        }),
    [userId, setPlaceState],
  );
  useEffect(() => {
    void loadLibraries();
  }, [loadLibraries]);

  return { libraries, setLibraries, failure, caller, loadLibraries };
}
