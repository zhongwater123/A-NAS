import { X } from "lucide-react";
import { useEffect, useEffectEvent, useMemo, useRef, useState } from "react";

import { browserCapabilities } from "../media/api";
import { getVideoPlayback, videoOriginalURL, videoStreamURL } from "../photos/api";
import { clock } from "../photos/model";
import { draftSongURL, type MontageDraft, type MontageDraftShot } from "./api";
import styles from "./montage.module.css";

// RoughCut plays a draft as it stands, without rendering it
// (docs/specs/montage-center.md, 粗预览): the version's song from its
// excerpt, and over it each shot's piece of its video, muted, changing at
// the slots' cuts. The music keeps the time: two video elements take turns,
// the next shot loaded and at its start before its cut comes. A video the
// browser cannot play comes converted by the photo library, which cannot
// seek, so it is asked for from the piece's start; there the cut may show
// black for a moment.

interface Source {
  url: string;
  // Where the piece starts in what url plays.
  at: number;
}

// sources decides, once a video, whether the browser plays its original.
function useSources() {
  const caps = useMemo(() => browserCapabilities(), []);
  const decided = useRef(new Map<string, Promise<boolean>>());
  return async (shot: MontageDraftShot): Promise<Source> => {
    let direct = decided.current.get(shot.assetId);
    if (!direct) {
      direct = getVideoPlayback(shot.assetId, caps).then(
        (decision) => decision.mode === "direct",
        () => false,
      );
      decided.current.set(shot.assetId, direct);
    }
    return (await direct)
      ? { url: videoOriginalURL(shot.assetId), at: shot.from }
      : { url: videoStreamURL(shot.assetId, shot.from, caps, false), at: 0 };
  };
}

// play starts an element; one that will not play waits for the next cut.
const play = (element: HTMLMediaElement | null | undefined) =>
  void Promise.resolve()
    .then(() => element?.play())
    .catch(() => undefined);

export function RoughCut({ draft, from, onClose }: { draft: MontageDraft; from: number; onClose: () => void }) {
  const audio = useRef<HTMLAudioElement>(null);
  const first = useRef<HTMLVideoElement>(null);
  const second = useRef<HTMLVideoElement>(null);
  const source = useSources();
  const begin = Math.max(0, from - 1);
  const [showing, setShowing] = useState(0);
  const [index, setIndex] = useState(begin);
  const [time, setTime] = useState(draft.shots[begin]?.start ?? 0);
  const [failed, setFailed] = useState(false);
  // Which shot each element holds, and the element showing now.
  const holds = useRef<[number, number]>([-1, -1]);
  const front = useRef(0);
  const player = (e: number) => (e === 0 ? first : second).current;

  // load puts shot k into element e, at the start of its piece.
  const load = useEffectEvent(async (e: number, k: number) => {
    const shot = draft.shots[k];
    const element = player(e);
    if (!shot || !element || holds.current[e] === k) return;
    holds.current[e] = k;
    const { url, at } = await source(shot);
    if (holds.current[e] !== k) return;
    element.src = url;
    if (at > 0)
      element.addEventListener(
        "loadedmetadata",
        () => {
          element.currentTime = at;
        },
        { once: true },
      );
    element.load();
  });

  // tick follows the music: at a cut the element holding the next shot comes
  // to the front, and the other loads the shot after it.
  const tick = useEffectEvent(() => {
    const music = audio.current;
    if (!music) return;
    const at = music.currentTime - draft.songStart;
    setTime(at);
    if (at >= draft.seconds) {
      music.pause();
      player(0)?.pause();
      player(1)?.pause();
      return;
    }
    const k = draft.shots.findIndex((shot) => at >= shot.start && at < shot.end);
    if (k < 0 || holds.current[front.current] === k) return;
    const next = holds.current[1 - front.current] === k ? 1 - front.current : front.current;
    if (next === front.current) void load(next, k);
    player(1 - next)?.pause();
    play(player(next));
    front.current = next;
    setShowing(next);
    setIndex(k);
    void load(1 - next, k + 1);
  });

  const start = useEffectEvent(() => {
    void load(0, begin).then(() => play(player(0)));
    void load(1, begin + 1);
    const music = audio.current;
    if (!music) return;
    const go = () => {
      music.currentTime = draft.songStart + (draft.shots[begin]?.start ?? 0);
      void Promise.resolve()
        .then(() => music.play())
        .catch(() => setFailed(true));
    };
    if (music.readyState >= 1) go();
    else music.addEventListener("loadedmetadata", go, { once: true });
  });

  useEffect(() => {
    start();
    let frame = 0;
    const loop = () => {
      tick();
      frame = requestAnimationFrame(loop);
    };
    frame = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(frame);
  }, []);

  const shot = draft.shots[index];
  return (
    <div className={styles.mtRough} role="dialog" aria-label="粗预览">
      <div className={styles.mtRoughStage}>
        <video ref={first} muted playsInline preload="auto" data-front={showing === 0 || undefined} />
        <video ref={second} muted playsInline preload="auto" data-front={showing === 1 || undefined} />
      </div>
      <audio ref={audio} src={draftSongURL(draft.id)} preload="auto" />
      <div className={styles.mtRoughBar}>
        <span>
          {shot ? `第 ${shot.number} 镜` : ""} · {clock(Math.max(0, time))} / {clock(draft.seconds)}
        </span>
        <span className={styles.mtFaint}>粗预览，不含放慢、竖拍垫底与淡出</span>
        {failed && <span className={styles.mtError}>配乐没能播放，点一下页面后重试</span>}
        <button type="button" className={`${styles.mtBtn} ${styles.small}`} aria-label="关闭粗预览" onClick={onClose}>
          <X aria-hidden="true" />
          关闭
        </button>
      </div>
    </div>
  );
}
