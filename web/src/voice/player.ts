import { useCallback, useEffect, useRef, useState } from "react";

import { audioURL, type Recording } from "./voiceApi";

export interface Player {
  playing: boolean;
  // timeMs is the position in the recording; loading while it waits for audio.
  timeMs: number;
  loading: boolean;
  rate: number;
  failed: boolean;
  toggle: () => void;
  play: (fromMs?: number) => void;
  seek: (ms: number) => void;
  skip: (deltaMs: number) => void;
  setRate: (rate: number) => void;
}

export const rates = [1, 1.25, 1.5, 2, 0.75];

// usePlayer plays one recording. A file browsers can play is the element's
// source with ranges; one they cannot is decoded on the NAS from a start, so
// seeking asks for a new stream from there.
export function usePlayer(recording: Pick<Recording, "id" | "transcode" | "durationMs"> | undefined): Player {
  const audio = useRef<HTMLAudioElement>(undefined);
  if (!audio.current && typeof Audio !== "undefined") audio.current = new Audio();
  const offset = useRef(0);
  const [playing, setPlaying] = useState(false);
  const [timeMs, setTimeMs] = useState(0);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [rate, setRateState] = useState(1);
  const id = recording?.id;
  const transcode = recording?.transcode ?? false;

  useEffect(() => {
    const element = audio.current;
    if (!element || !id) return;
    offset.current = 0;
    element.preload = "metadata";
    element.src = audioURL(id, transcode ? 0 : undefined);
    setTimeMs(0);
    setPlaying(false);
    setFailed(false);
    let frame = 0;
    const tick = () => {
      setTimeMs(offset.current + element.currentTime * 1000);
      if (!element.paused) frame = requestAnimationFrame(tick);
    };
    const onPlay = () => { setPlaying(true); cancelAnimationFrame(frame); frame = requestAnimationFrame(tick); };
    const onPause = () => { setPlaying(false); cancelAnimationFrame(frame); setTimeMs(offset.current + element.currentTime * 1000); };
    const onWaiting = () => setLoading(true);
    const onReady = () => setLoading(false);
    const onError = () => { setLoading(false); setPlaying(false); setFailed(true); };
    const onTime = () => { if (element.paused) setTimeMs(offset.current + element.currentTime * 1000); };
    element.addEventListener("play", onPlay);
    element.addEventListener("pause", onPause);
    element.addEventListener("ended", onPause);
    element.addEventListener("waiting", onWaiting);
    element.addEventListener("playing", onReady);
    element.addEventListener("canplay", onReady);
    element.addEventListener("error", onError);
    element.addEventListener("timeupdate", onTime);
    return () => {
      cancelAnimationFrame(frame);
      element.pause();
      element.removeEventListener("play", onPlay);
      element.removeEventListener("pause", onPause);
      element.removeEventListener("ended", onPause);
      element.removeEventListener("waiting", onWaiting);
      element.removeEventListener("playing", onReady);
      element.removeEventListener("canplay", onReady);
      element.removeEventListener("error", onError);
      element.removeEventListener("timeupdate", onTime);
      element.removeAttribute("src");
      element.load();
    };
  }, [id, transcode]);

  const start = useCallback((element: HTMLAudioElement) => {
    setFailed(false);
    const started = element.play();
    if (started && typeof started.catch === "function") started.catch(() => setPlaying(false));
  }, []);

  const seek = useCallback((ms: number) => {
    const element = audio.current;
    if (!element || !id) return;
    const total = recording?.durationMs ?? 0;
    const target = Math.max(0, total ? Math.min(ms, total - 250) : ms);
    if (transcode) {
      const wasPlaying = !element.paused;
      offset.current = target;
      element.src = audioURL(id, target / 1000);
      element.playbackRate = rate;
      setTimeMs(target);
      if (wasPlaying) start(element);
    } else {
      element.currentTime = target / 1000;
      setTimeMs(target);
    }
  }, [id, transcode, recording?.durationMs, rate, start]);

  const play = useCallback((fromMs?: number) => {
    const element = audio.current;
    if (!element) return;
    if (fromMs !== undefined) seek(fromMs);
    element.playbackRate = rate;
    start(element);
  }, [rate, seek, start]);

  const toggle = useCallback(() => {
    const element = audio.current;
    if (!element) return;
    if (element.paused) play();
    else element.pause();
  }, [play]);

  const setRate = useCallback((next: number) => {
    setRateState(next);
    if (audio.current) audio.current.playbackRate = next;
  }, []);

  const skip = useCallback((deltaMs: number) => seek(timeMs + deltaMs), [seek, timeMs]);

  return { playing, timeMs, loading, rate, failed, toggle, play, seek, skip, setRate };
}
