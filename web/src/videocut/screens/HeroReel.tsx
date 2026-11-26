import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import { hasHero, heroPosterURL, heroURL } from "../api/client";
import styles from "./videocut.module.css";

const stillOnly = () =>
  typeof window === "undefined" ||
  typeof window.matchMedia !== "function" ||
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

// HeroReel is the hero's background, as the prototype's hero film: the
// device's own film when it keeps one (montage/hero/hero.mp4, never in the
// software: the repository is public), and nothing otherwise. It no longer
// plays the member's own videos: each of them came from the photo library
// every few seconds, which kept the library in use for as long as the window
// was open (more than a gigabyte in ten minutes on the Experimental NAS).
// A viewer who asked for less motion sees the film's poster still.
export function HeroReel() {
  // The device's film is asked for before it is played: a player that was
  // given a film the device does not have does not reliably say so.
  const device = useQuery({
    queryKey: ["montage", "hero"],
    queryFn: ({ signal }) => hasHero(signal),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const [unplayable, setUnplayable] = useState(false);
  const still = useMemo(() => stillOnly(), []);
  if (device.data !== true || unplayable) return null;
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
