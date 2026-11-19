import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { browserCapabilities } from "../media/api";
import { getVideoPlayback, thumbnailURL, videoOriginalURL, videoStreamURL } from "../photos/api";
import { hasHero, heroPosterURL, heroURL } from "./api";
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
  // The device's own film, when it has one (montage/hero/hero.mp4). It is
  // asked for before it is played: a player that was given a film the device
  // does not have does not reliably say so.
  const device = useQuery({
    queryKey: ["montage", "hero"],
    queryFn: ({ signal }) => hasHero(signal),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const [unplayable, setUnplayable] = useState(false);
  const deviceFilm = device.data === true && !unplayable;
  // Until the device has answered, nothing moves: the member's videos do
  // not start only to give way to the device's film.
  const settled = device.data !== undefined || device.isError;
  const [at, setAt] = useState(0);
  const caps = useMemo(() => browserCapabilities(), []);
  const still = useMemo(() => stillOnly(), []);
  const current = assetIds.length ? assetIds[at % assetIds.length] : undefined;
  // How a video plays is asked once: the reel comes round to it again and again.
  const playback = useQuery({
    queryKey: ["photos", "playback", current, caps],
    queryFn: () => getVideoPlayback(current as string, caps),
    enabled: Boolean(current) && !still && settled && !deviceFilm,
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const source =
    current && playback.data
      ? playback.data.mode === "direct"
        ? `${videoOriginalURL(current)}#t=${FROM_SECONDS}`
        : videoStreamURL(current, FROM_SECONDS, caps, playback.data.mode === "transcode")
      : undefined;

  useEffect(() => {
    if (assetIds.length < 2 || still || !settled || deviceFilm) return;
    const next = window.setTimeout(() => setAt((n) => n + 1), SHOT_MS);
    return () => window.clearTimeout(next);
  }, [at, assetIds.length, still, settled, deviceFilm]);

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
          onError={() => setUnplayable(true)}
        />
      </div>
    );
  }
  if (!current || !settled) return null;
  return (
    <div className={styles.mtReel} aria-hidden="true">
      <img key={`still-${current}`} src={thumbnailURL(current)} alt="" />
      {source && <video key={source} src={source} muted autoPlay playsInline loop preload="auto" />}
    </div>
  );
}
