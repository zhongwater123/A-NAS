import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";

import { artistLine, shuffled, unknownAlbum } from "./library";
import { coverURL, streamURL, type Track } from "./musicApi";

export type Repeat = "off" | "all" | "one";

interface QueueState {
  // Track IDs in playing order; a track may appear more than once.
  queue: string[];
  index: number;
  // The order before shuffling, restored when shuffle is turned off.
  original?: string[];
  shuffle: boolean;
  repeat: Repeat;
  volume: number;
  muted: boolean;
}

interface Saved extends QueueState { position: number }

const initialQueue: QueueState = { queue: [], index: -1, shuffle: false, repeat: "off", volume: 0.8, muted: false };

// A play counts toward the play history after this many seconds of listening.
const countAfterSeconds = 10;

// Time moves several times a second; only the progress bar and lyrics
// subscribe to it, so the rest of the window does not re-render.
export interface PlaybackTime { position: number; duration: number; buffered: number }

class TimeStore {
  value: PlaybackTime = { position: 0, duration: 0, buffered: 0 };
  private listeners = new Set<() => void>();
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  get = () => this.value;
  set(next: Partial<PlaybackTime>) {
    const value = { ...this.value, ...next };
    if (value.position === this.value.position && value.duration === this.value.duration && value.buffered === this.value.buffered) return;
    this.value = value;
    for (const listener of this.listeners) listener();
  }
}

export interface Player {
  queue: string[];
  index: number;
  current?: Track;
  playing: boolean;
  loading: boolean;
  shuffle: boolean;
  repeat: Repeat;
  volume: number;
  muted: boolean;
  time: TimeStore;
  play: (ids: string[], start?: number) => void;
  playShuffled: (ids: string[]) => void;
  playNext: (ids: string[]) => void;
  enqueue: (ids: string[]) => void;
  toggle: () => void;
  next: () => void;
  previous: () => void;
  seek: (seconds: number) => void;
  jump: (index: number) => void;
  remove: (index: number) => void;
  move: (from: number, to: number) => void;
  clearUpcoming: () => void;
  setVolume: (volume: number) => void;
  toggleMute: () => void;
  toggleShuffle: () => void;
  cycleRepeat: () => void;
}

export function usePlaybackTime(player: Pick<Player, "time">): PlaybackTime {
  return useSyncExternalStore(player.time.subscribe, player.time.get);
}

interface Options {
  storageKey: string;
  tracks: Map<string, Track>;
  // Whether the library has loaded, so a restored queue can drop tracks
  // that are gone.
  ready: boolean;
  onPlayed: (trackId: string) => void;
  onError: (track: Track) => void;
}

function load(key: string): Saved | undefined {
  try {
    const value = JSON.parse(window.localStorage.getItem(key) ?? "null") as Saved | null;
    return value && Array.isArray(value.queue) ? value : undefined;
  } catch { return undefined; }
}

// usePlayer plays the queue through one audio element that lives as long as
// the music window, so playback continues while the window is minimized.
export function usePlayer({ storageKey, tracks, ready, onPlayed, onError }: Options): Player {
  const saved = useMemo(() => load(storageKey), [storageKey]);
  const [state, setState] = useState<QueueState>(() => saved ? { ...initialQueue, ...saved, queue: saved.queue, index: saved.index } : initialQueue);
  const [playing, setPlaying] = useState(false);
  const [loading, setLoading] = useState(false);
  const time = useMemo(() => new TimeStore(), []);
  const audioRef = useRef<HTMLAudioElement>(null);
  if (!audioRef.current) audioRef.current = new Audio();
  const audio = audioRef.current;
  const stateRef = useRef(state);
  stateRef.current = state;
  const loaded = useRef<{ generation: number; id: string }>(undefined);
  const wantPlay = useRef(false);
  const resumeAt = useRef(saved?.position ?? 0);
  const listened = useRef({ seconds: 0, last: 0, counted: false });
  const errors = useRef(0);
  const callbacks = useRef({ onPlayed, onError });
  callbacks.current = { onPlayed, onError };
  const tracksRef = useRef(tracks);
  tracksRef.current = tracks;

  const currentId = state.index >= 0 ? state.queue[state.index] : undefined;
  const current = currentId ? tracks.get(currentId) : undefined;

  // Drop restored tracks that are no longer in the library.
  const pruned = useRef(false);
  useEffect(() => {
    if (!ready || pruned.current) return;
    pruned.current = true;
    setState((value) => {
      const keep = value.queue.map((id, position) => ({ id, position })).filter((item) => tracks.has(item.id));
      if (keep.length === value.queue.length) return value;
      const index = keep.findIndex((item) => item.position >= value.index);
      return { ...value, queue: keep.map((item) => item.id), index: keep.length ? Math.max(0, index) : -1, original: value.original?.filter((id) => tracks.has(id)) };
    });
  }, [ready, tracks]);

  const start = useCallback(() => {
    wantPlay.current = true;
    const result = audio.play();
    if (result) result.catch((error: unknown) => {
      // A newer load interrupts an earlier play; that is not a failure.
      if (error instanceof DOMException && error.name === "AbortError") return;
      wantPlay.current = false;
      setPlaying(false);
    });
  }, [audio]);

  // Load the current track whenever it changes, or when go() chose it again
  // (a new generation), so choosing the playing track restarts it.
  const [generation, setGeneration] = useState(0);
  useEffect(() => {
    if (!currentId || !ready) return;
    if (loaded.current?.generation === generation && loaded.current.id === currentId) return;
    loaded.current = { generation, id: currentId };
    audio.preload = "metadata";
    audio.src = streamURL(currentId);
    listened.current = { seconds: 0, last: 0, counted: false };
    time.set({ position: resumeAt.current, duration: (tracksRef.current.get(currentId)?.durationMs ?? 0) / 1000, buffered: 0 });
    if (wantPlay.current) start();
  }, [audio, currentId, generation, ready, start, time]);

  useEffect(() => {
    audio.volume = state.volume;
    audio.muted = state.muted;
  }, [audio, state.volume, state.muted]);

  const save = useCallback(() => {
    try { window.localStorage.setItem(storageKey, JSON.stringify({ ...stateRef.current, position: audio.currentTime || resumeAt.current })); } catch { /* storage is optional */ }
  }, [audio, storageKey]);
  useEffect(() => { save(); }, [state, save]);

  const go = useCallback((index: number, play = true) => {
    wantPlay.current = play;
    resumeAt.current = 0;
    setGeneration((value) => value + 1);
    setState((value) => ({ ...value, index }));
  }, []);

  const next = useCallback((automatic = false) => {
    const value = stateRef.current;
    if (!value.queue.length) return;
    if (value.index < value.queue.length - 1) go(value.index + 1);
    else if (value.repeat === "all") go(0);
    else if (automatic) {
      wantPlay.current = false;
      setPlaying(false);
      audio.currentTime = 0;
    } else go(0, false);
  }, [audio, go]);

  const previous = useCallback(() => {
    const value = stateRef.current;
    if (audio.currentTime > 3 || value.index <= 0 && value.repeat !== "all") { audio.currentTime = 0; return; }
    go(value.index > 0 ? value.index - 1 : value.queue.length - 1, wantPlay.current || !audio.paused);
  }, [audio, go]);

  useEffect(() => {
    const onTime = () => {
      const position = audio.currentTime;
      time.set({ position });
      const heard = listened.current;
      const delta = position - heard.last;
      if (delta > 0 && delta < 2) heard.seconds += delta;
      heard.last = position;
      if (!heard.counted && heard.seconds >= countAfterSeconds && loaded.current) {
        heard.counted = true;
        callbacks.current.onPlayed(loaded.current.id);
      }
    };
    const onDuration = () => { if (Number.isFinite(audio.duration)) time.set({ duration: audio.duration }); };
    const onMetadata = () => {
      onDuration();
      // A track that had played to its end starts again from the beginning.
      if (resumeAt.current > 0 && resumeAt.current < audio.duration - 2) audio.currentTime = resumeAt.current;
      resumeAt.current = 0;
    };
    const onProgress = () => { if (audio.buffered.length) time.set({ buffered: audio.buffered.end(audio.buffered.length - 1) }); };
    const onPlay = () => { setPlaying(true); errors.current = 0; };
    const onPause = () => { setPlaying(false); save(); };
    const onWaiting = () => setLoading(true);
    const onPlaying = () => { setLoading(false); setPlaying(true); };
    const onEnded = () => {
      const heard = listened.current;
      if (!heard.counted && loaded.current) { heard.counted = true; callbacks.current.onPlayed(loaded.current.id); }
      if (stateRef.current.repeat === "one") { audio.currentTime = 0; start(); return; }
      next(true);
    };
    const onError = () => {
      setLoading(false);
      const id = loaded.current?.id;
      const track = id ? tracksRef.current.get(id) : undefined;
      if (!track || !audio.getAttribute("src")) return;
      callbacks.current.onError(track);
      errors.current += 1;
      // Skip an unplayable file, but stop rather than spin through a queue
      // that cannot play at all.
      if (wantPlay.current && errors.current < 3 && stateRef.current.queue.length > 1) window.setTimeout(() => next(true), 1200);
      else { wantPlay.current = false; setPlaying(false); }
    };
    const events: Array<[string, () => void]> = [
      ["timeupdate", onTime], ["durationchange", onDuration], ["loadedmetadata", onMetadata], ["progress", onProgress],
      ["play", onPlay], ["pause", onPause], ["waiting", onWaiting], ["playing", onPlaying], ["canplay", () => setLoading(false)],
      ["ended", onEnded], ["error", onError],
    ];
    for (const [name, handler] of events) audio.addEventListener(name, handler);
    return () => { for (const [name, handler] of events) audio.removeEventListener(name, handler); };
  }, [audio, next, save, start, time]);

  // Closing the window stops the music.
  useEffect(() => () => {
    save();
    // A remount, as StrictMode does in development, loads the track again
    // where it was.
    resumeAt.current = audio.currentTime || resumeAt.current;
    loaded.current = undefined;
    audio.pause();
    audio.removeAttribute("src");
    audio.load();
  }, [audio, save]);

  const toggle = useCallback(() => {
    if (!stateRef.current.queue.length) return;
    if (audio.paused) {
      if (!audio.getAttribute("src")) { go(Math.max(0, stateRef.current.index)); return; }
      start();
    } else { wantPlay.current = false; audio.pause(); }
  }, [audio, go, start]);

  const seek = useCallback((seconds: number) => {
    if (!Number.isFinite(seconds)) return;
    audio.currentTime = Math.max(0, seconds);
    listened.current.last = audio.currentTime;
    time.set({ position: audio.currentTime });
  }, [audio, time]);

  const play = useCallback((ids: string[], startAt = 0) => {
    if (!ids.length) return;
    const first = Math.min(Math.max(0, startAt), ids.length - 1);
    setState((value) => value.shuffle
      ? { ...value, queue: [ids[first], ...shuffled(ids.filter((_, position) => position !== first))], original: ids, index: 0 }
      : { ...value, queue: ids, original: undefined, index: first });
    go(stateRef.current.shuffle ? 0 : first);
  }, [go]);

  const playShuffled = useCallback((ids: string[]) => {
    if (!ids.length) return;
    setState((value) => ({ ...value, shuffle: true, queue: shuffled(ids), original: ids, index: 0 }));
    go(0);
  }, [go]);

  const insert = useCallback((ids: string[], after: boolean) => {
    if (!ids.length) return;
    const wasEmpty = !stateRef.current.queue.length;
    setState((value) => {
      if (!value.queue.length) return { ...value, queue: ids, index: 0, original: value.shuffle ? ids : undefined };
      const at = after ? value.index + 1 : value.queue.length;
      const queue = [...value.queue.slice(0, at), ...ids, ...value.queue.slice(at)];
      const original = value.original && [...value.original, ...ids];
      return { ...value, queue, original };
    });
    if (wasEmpty) go(0);
  }, [go]);

  const jump = useCallback((index: number) => go(index), [go]);

  const remove = useCallback((index: number) => {
    const value = stateRef.current;
    if (index < 0 || index >= value.queue.length) return;
    const queue = value.queue.filter((_, position) => position !== index);
    const removedId = value.queue[index];
    const originalIndex = value.original?.indexOf(removedId) ?? -1;
    const original = value.original && originalIndex >= 0 ? value.original.filter((_, position) => position !== originalIndex) : value.original;
    if (index === value.index) {
      if (!queue.length) {
        wantPlay.current = false;
        audio.pause();
        audio.removeAttribute("src");
        loaded.current = undefined;
        setState({ ...value, queue, original, index: -1 });
        return;
      }
      setState({ ...value, queue, original, index: Math.min(index, queue.length - 1) });
      go(Math.min(index, queue.length - 1), !audio.paused);
      return;
    }
    setState({ ...value, queue, original, index: index < value.index ? value.index - 1 : value.index });
  }, [audio, go]);

  const move = useCallback((from: number, to: number) => setState((value) => {
    if (from === to || from < 0 || from >= value.queue.length) return value;
    const queue = [...value.queue];
    const [id] = queue.splice(from, 1);
    const target = Math.max(0, Math.min(queue.length, to > from ? to - 1 : to));
    queue.splice(target, 0, id);
    let index = value.index;
    if (from === value.index) index = target;
    else if (from < value.index && target >= value.index) index -= 1;
    else if (from > value.index && target <= value.index) index += 1;
    return { ...value, queue, index };
  }), []);

  const clearUpcoming = useCallback(() => setState((value) => ({
    ...value, queue: value.queue.slice(0, value.index + 1), original: value.original?.filter((id) => value.queue.slice(0, value.index + 1).includes(id)),
  })), []);

  const toggleShuffle = useCallback(() => setState((value) => {
    const currentTrack = value.queue[value.index];
    if (!value.shuffle) {
      if (!value.queue.length) return { ...value, shuffle: true };
      const rest = value.queue.filter((_, position) => position !== value.index);
      return { ...value, shuffle: true, original: value.queue, queue: [currentTrack, ...shuffled(rest)], index: 0 };
    }
    const queue = value.original ?? value.queue;
    return { ...value, shuffle: false, original: undefined, queue, index: Math.max(0, queue.indexOf(currentTrack)) };
  }), []);

  const cycleRepeat = useCallback(() => setState((value) => ({ ...value, repeat: value.repeat === "off" ? "all" : value.repeat === "all" ? "one" : "off" })), []);
  const setVolume = useCallback((volume: number) => setState((value) => ({ ...value, volume: Math.min(1, Math.max(0, volume)), muted: volume <= 0 })), []);
  const toggleMute = useCallback(() => setState((value) => ({ ...value, muted: !value.muted, volume: value.muted && value.volume === 0 ? 0.5 : value.volume })), []);

  // Media keys, headset buttons and the system media panel.
  useEffect(() => {
    const session = navigator.mediaSession;
    if (!session) return;
    const handlers: Array<[MediaSessionAction, MediaSessionActionHandler]> = [
      ["play", () => toggle()], ["pause", () => toggle()], ["nexttrack", () => next()], ["previoustrack", () => previous()],
      ["seekto", (details) => { if (details.seekTime !== undefined) seek(details.seekTime); }],
    ];
    for (const [action, handler] of handlers) {
      try { session.setActionHandler(action, handler); } catch { /* unsupported action */ }
    }
    return () => { for (const [action] of handlers) { try { session.setActionHandler(action, null); } catch { /* unsupported action */ } } };
  }, [next, previous, seek, toggle]);
  useEffect(() => {
    const session = navigator.mediaSession;
    if (!session || typeof MediaMetadata === "undefined") return;
    session.metadata = current ? new MediaMetadata({
      title: current.title, artist: artistLine(current), album: current.album || unknownAlbum,
      artwork: current.coverId ? [{ src: coverURL(current.coverId, "large"), sizes: "720x720", type: "image/jpeg" }] : [],
    }) : null;
  }, [current]);
  useEffect(() => {
    if (navigator.mediaSession) navigator.mediaSession.playbackState = playing ? "playing" : current ? "paused" : "none";
  }, [playing, current]);

  return {
    queue: state.queue, index: state.index, current, playing, loading, shuffle: state.shuffle, repeat: state.repeat,
    volume: state.volume, muted: state.muted, time,
    play, playShuffled, playNext: (ids) => insert(ids, true), enqueue: (ids) => insert(ids, false), toggle,
    next: () => next(false), previous, seek, jump, remove, move, clearUpcoming, setVolume, toggleMute, toggleShuffle, cycleRepeat,
  };
}
