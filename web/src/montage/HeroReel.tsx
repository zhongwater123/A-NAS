import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import { hasHero, heroPosterURL, heroURL } from "./api";
import styles from "./montage.module.css";

const stillOnly = () =>
  typeof window === "undefined" ||
  typeof window.matchMedia !== "function" ||
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

// HeroReel is the hero's background, as the prototype's hero film: the
// device's own film when it keeps one (montage/hero/hero.mp4, never in the
// software: the repository is public). It does not play the member's own
// videos: each of them came from the photo library every few seconds, which
// kept the library in use for as long as the window was open (more than a
// gigabyte in ten minutes on the Experimental NAS). A viewer who asked for
// less motion sees the film's poster still. Without the device's film, and
// until the device has answered, the hero is drawn in the window's colours:
// three tracks of clips under a playhead, as an editing timeline, moving
// slowly unless the viewer asked for less motion. It is drawn, never a
// bundled picture or film: the repository is public.
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
  if (device.data !== true || unplayable) return <Timeline />;
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

const TRACKS = ["media", "tune", "cut"] as const;
const CLIPS = 12;

function Timeline() {
  return (
    <div className={`${styles.mtReel} ${styles.mtReelDrawn}`} data-reel="drawn" aria-hidden="true">
      <div className={styles.mtTracks}>
        {TRACKS.map((tone) => (
          <div key={tone} className={`${styles.mtTrack} ${styles[tone]}`}>
            {Array.from({ length: CLIPS }, (_, i) => (
              <span key={i} />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
