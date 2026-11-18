import { useEffect, useMemo, useState } from "react";

import { browserCapabilities } from "../media/api";
import { getVideoPlayback, thumbnailURL, videoOriginalURL, videoStreamURL } from "../photos/api";
import styles from "./montage.module.css";

// How long each video plays behind the hero, from how far into it.
const SHOT_MS = 5_000;
const FROM_SECONDS = 1;

const stillOnly = () =>
  typeof window === "undefined" ||
  typeof window.matchMedia !== "function" ||
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

// HeroReel is the hero's background, as the prototype's hero film: a few
// of the member's own videos, one after another, muted. Each plays the
// way the photo library plays it (the original, or its stream); a viewer
// who asked for less motion sees the first one still.
export function HeroReel({ assetIds }: { assetIds: string[] }) {
  const [at, setAt] = useState(0);
  const [source, setSource] = useState<{ id: string; url: string }>();
  const caps = useMemo(() => browserCapabilities(), []);
  const still = useMemo(() => stillOnly(), []);
  const current = assetIds.length ? assetIds[at % assetIds.length] : undefined;

  useEffect(() => {
    if (!current || still) return;
    let live = true;
    getVideoPlayback(current, caps).then(
      (decision) => {
        if (!live) return;
        const url =
          decision.mode === "direct"
            ? `${videoOriginalURL(current)}#t=${FROM_SECONDS}`
            : videoStreamURL(current, FROM_SECONDS, caps, decision.mode === "transcode");
        setSource({ id: current, url });
      },
      () => live && setSource(undefined),
    );
    return () => {
      live = false;
    };
  }, [current, caps, still]);

  useEffect(() => {
    if (assetIds.length < 2 || still) return;
    const next = window.setTimeout(() => setAt((n) => n + 1), SHOT_MS);
    return () => window.clearTimeout(next);
  }, [at, assetIds.length, still]);

  if (!current) return null;
  return (
    <div className={styles.mtReel} aria-hidden="true">
      <img key={`still-${current}`} src={thumbnailURL(current)} alt="" />
      {source?.id === current && (
        <video key={source.url} src={source.url} muted autoPlay playsInline loop preload="auto" />
      )}
    </div>
  );
}
