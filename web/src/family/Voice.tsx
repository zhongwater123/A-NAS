import { Pause, Play } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import WaveSurfer from "wavesurfer.js";

import { audioURL, type FamilyRecord } from "./api";
import styles from "./family.module.css";

// The browser decodes a recording for its waveform only up to this size; a
// longer one, up to two hours, keeps drawn bars and plays all the same.
const decodeLimit = 30 * 1024 * 1024;

// Voice plays a recording from a pill: a play button, its waveform and its
// title and time. The waveform is the recording's own, drawn by wavesurfer
// once the entry comes near the view; dragging along it seeks.
export function Voice({ record }: { record: FamilyRecord }) {
  const audio = useRef<HTMLAudioElement>(null);
  const host = useRef<HTMLDivElement>(null);
  const [drawn, setDrawn] = useState(false);
  const [playing, setPlaying] = useState(false);
  const [duration, setDuration] = useState<number>();
  const [time, setTime] = useState(0);
  const decodable = (record.recording?.sizeBytes ?? 0) <= decodeLimit && typeof IntersectionObserver !== "undefined";

  useEffect(() => {
    const element = host.current;
    const media = audio.current;
    if (!element || !media || !decodable) return;
    let surfer: WaveSurfer | undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        if (surfer || !entries.some((entry) => entry.isIntersecting)) return;
        observer.disconnect();
        const style = getComputedStyle(element);
        surfer = WaveSurfer.create({
          container: element,
          media,
          url: audioURL(record.id),
          height: 28,
          barWidth: 2,
          barGap: 2,
          barRadius: 2,
          barMinHeight: 2,
          normalize: true,
          cursorWidth: 0,
          dragToSeek: true,
          waveColor: style.getPropertyValue("--fc-wave").trim() || "#cfc5bc",
          progressColor: style.getPropertyValue("--fc-accent").trim() || "#c4553a",
        });
        surfer.on("ready", () => setDrawn(true));
        surfer.on("error", () => setDrawn(false));
      },
      { rootMargin: "240px" },
    );
    observer.observe(element);
    return () => {
      observer.disconnect();
      surfer?.destroy();
    };
  }, [record.id, decodable]);

  const toggle = () => {
    const media = audio.current;
    if (!media) return;
    if (media.paused) void media.play().catch(() => setPlaying(false));
    else media.pause();
  };
  // Until the waveform is drawn, the placeholder bars seek too.
  const seek = (event: React.PointerEvent<HTMLElement>) => {
    const media = audio.current;
    if (!media || !media.duration) return;
    const box = event.currentTarget.getBoundingClientRect();
    media.currentTime = Math.min(1, Math.max(0, (event.clientX - box.left) / box.width)) * media.duration;
  };
  return (
    <div className={`${styles.fcVoice} ${playing ? styles.playing : ""}`}>
      <button type="button" className={styles.fcPlay} onClick={toggle} aria-label={playing ? "暂停" : "播放录音"}>
        {playing ? <Pause size={14} fill="currentColor" /> : <Play size={14} fill="currentColor" />}
      </button>
      <div className={styles.fcWaveBox}>
        <div ref={host} className={`${styles.fcWaveDrawn} ${drawn ? styles.shown : ""}`} />
        {!drawn && <Bars id={record.id} progress={duration ? time / duration : 0} onSeek={seek} />}
      </div>
      <span className={styles.fcVoiceTitle}>
        <b>{record.text || record.recording?.fileName || "录音"}</b>
        <small>{playing || time > 0 ? `${clock(time)} / ${clock(duration)}` : clock(duration)}</small>
      </span>
      {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- a family's own voice recording has no captions */}
      <audio
        ref={audio}
        preload="metadata"
        src={audioURL(record.id)}
        onPlay={() => setPlaying(true)}
        onPause={() => setPlaying(false)}
        onEnded={() => {
          setPlaying(false);
          setTime(0);
        }}
        onLoadedMetadata={(event) => setDuration(event.currentTarget.duration)}
        onTimeUpdate={(event) => setTime(event.currentTarget.currentTime)}
      />
    </div>
  );
}

// Bars stand in for the waveform before it is drawn, or for a recording too
// long to decode: the same bars for the same recording each time.
function Bars({
  id,
  progress,
  onSeek,
}: {
  id: string;
  progress: number;
  onSeek: (event: React.PointerEvent<HTMLElement>) => void;
}) {
  const bars = useMemo(() => placeholder(id), [id]);
  return (
    <span className={styles.fcWave} aria-hidden="true" onPointerDown={onSeek}>
      {bars.map((height, index) => (
        <i
          // biome-ignore lint/suspicious/noArrayIndexKey: the bars are a fixed decoration
          key={index}
          style={{ height: `${height}%` }}
          className={index / bars.length < progress ? styles.played : undefined}
        />
      ))}
    </span>
  );
}

function placeholder(id: string): number[] {
  let seed = [...id].reduce((sum, character) => (sum * 31 + character.charCodeAt(0)) >>> 0, 7);
  const bars: number[] = [];
  for (let index = 0; index < 40; index++) {
    seed = (seed * 1103515245 + 12345) >>> 0;
    bars.push(25 + (seed % 70));
  }
  return bars;
}

function clock(seconds?: number): string {
  if (seconds === undefined || !Number.isFinite(seconds)) return "";
  const whole = Math.round(seconds);
  return `${Math.floor(whole / 60)}:${String(whole % 60).padStart(2, "0")}`;
}
