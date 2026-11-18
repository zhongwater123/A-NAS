import { useEffect, useMemo, useState } from "react";

import { browserCapabilities } from "../media/api";
import { getVideoPlayback, thumbnailURL, videoOriginalURL, videoStreamURL } from "../photos/api";
import { heroPosterURL, heroURL } from "./api";
import styles from "./montage.module.css";

// How long each video plays behind the hero, from how far into it.
const SHOT_MS = 5_000;
const FROM_SECONDS = 1;

const stillOnly = () =>
  typeof window === "undefined" ||
  typeof window.matchMedia !== "function" ||
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

// HeroReel is the hero's background, as the prototype's hero film: the
// device's own film when it keeps one, as the prototype's hero.mp4 (never
// in the software: the repository is public); otherwise a few of the
// member's own videos, one after another, muted. Each plays the
// way the photo library plays it (the original, or its stream); a viewer
// who asked for less motion sees the first one still.
export function HeroReel({ assetIds }: { assetIds: string[] }) {
  // The device's own film, when it has one (montage/hero/hero.mp4).
  const [deviceFilm, setDeviceFilm] = useState(true);
  const [at, setAt] = useState(0);
  const [source, setSource] = useState<{ id: string; url: string }>();
  const caps = useMemo(() => browserCapabilities(), []);
  const still = useMemo(() => stillOnly(), []);
  const current = assetIds.length ? assetIds[at % assetIds.length] : undefined;

  useEffect(() => {
    if (!current || still || deviceFilm) return;
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
  }, [current, caps, still, deviceFilm]);

  useEffect(() => {
    if (assetIds.length < 2 || still || deviceFilm) return;
    const next = window.setTimeout(() => setAt((n) => n + 1), SHOT_MS);
    return () => window.clearTimeout(next);
  }, [at, assetIds.length, still, deviceFilm]);

  if (deviceFilm) {
    return (
      <div className={styles.mtReel} aria-hidden="true">
        <video
          src={`${heroURL}#t=0.7`}
          poster={heroPosterURL}
          muted
          autoPlay={!still}
          playsInline
          loop
          preload="auto"
          onError={() => setDeviceFilm(false)}
        />
      </div>
    );
  }
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
