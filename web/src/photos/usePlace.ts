import { useCallback, useLayoutEffect, useRef, useState } from "react";

import type { AIHomeSnapshot } from "./AIHome";
import type { PhotoObjectsFound } from "./api";
import { type ImageResult, isEncoded } from "./imageSearch";
import type { Place } from "./model";

const placeKey = (place: Place) => {
  switch (place.kind) {
    case "album":
      return `album:${place.album.id}`;
    case "search":
      return `search:${place.viewing}:${place.query}`;
    case "cluster":
      return `cluster:${place.viewing}:${place.label.id}`;
    case "tag":
      return `tag:${place.viewing}:${place.tag.name}`;
    case "person":
      return `person:${place.person.id}`;
    case "similar":
      return `similar:${place.viewing}:${place.asset.id}`;
    case "image":
      return `image:${place.viewing}:${place.query.key}`;
    default:
      return `${place.kind}:${place.libraryId}`;
  }
};

// usePlace keeps the place the window shows and what its view reported about
// it: the count, how a search or a search by image went, the photos taken
// out of it. setPlace goes to another place and starts those over.
export function usePlace({
  clearSelection,
  setDrawer,
}: {
  clearSelection: () => void;
  setDrawer: (open: boolean) => void;
}) {
  const [place, setPlaceState] = useState<Place>();
  const [count, setCount] = useState<number>();
  // The videos among count, where the view knows them: the timeline.
  const [videoCount, setVideoCount] = useState(0);
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());
  // How many of the current search's results are the closest, as the photo
  // service says; the less related follow.
  const [closest, setClosest] = useState<number>();
  // The one library a search is narrowed to; empty searches them all.
  const [searchLibrary, setSearchLibrary] = useState("");
  // What the object detector found for it, when the query is about things.
  const [found, setFound] = useState<PhotoObjectsFound>();
  // The people whose photos the current search keeps to.
  const [searchPeople, setSearchPeople] = useState<string[]>();
  // Whether the photo similar ones are shown for has been organised.
  const [similarReady, setSimilarReady] = useState<boolean>();
  // How reading the image of a search by image went; trying again starts
  // its results over.
  const [imageResult, setImageResult] = useState<ImageResult>();
  const [imageAttempt, setImageAttempt] = useState(0);
  const [query, setQuery] = useState("");

  // Each view reports its own photos when it mounts, so going to the place
  // already shown changes nothing.
  const placeRef = useRef(place);
  useLayoutEffect(() => {
    placeRef.current = place;
  });
  // How AI 聚合 looked when it opened a group; kept while that group is open
  // so that going back returns to the same spot.
  const [aiHomeReturn, setAIHomeReturn] = useState<AIHomeSnapshot>();
  const setPlace = useCallback(
    (next: Place) => {
      setDrawer(false);
      if (placeKey(next) === (placeRef.current && placeKey(placeRef.current))) return;
      const leaving = placeRef.current;
      if (
        !(
          leaving &&
          (leaving.kind === "cluster" || leaving.kind === "tag" || leaving.kind === "person") &&
          next.kind === "search" &&
          !next.query
        )
      )
        setAIHomeReturn(undefined);
      setPlaceState(next);
      clearSelection();
      setHidden(new Set());
      setCount(undefined);
      setClosest(undefined);
      setFound(undefined);
      setSimilarReady(undefined);
      setImageResult(next.kind === "image" && isEncoded(next.query) ? {} : undefined);
      setQuery(next.kind === "search" ? next.query : "");
    },
    [clearSelection, setDrawer],
  );

  return {
    place,
    setPlace,
    setPlaceState,
    placeRef,
    count,
    setCount,
    videoCount,
    setVideoCount,
    hidden,
    setHidden,
    closest,
    setClosest,
    searchLibrary,
    setSearchLibrary,
    found,
    setFound,
    searchPeople,
    setSearchPeople,
    similarReady,
    setSimilarReady,
    imageResult,
    setImageResult,
    imageAttempt,
    setImageAttempt,
    query,
    setQuery,
    aiHomeReturn,
    setAIHomeReturn,
  };
}

export type PlaceState = ReturnType<typeof usePlace>;
