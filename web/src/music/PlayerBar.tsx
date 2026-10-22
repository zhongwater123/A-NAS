import {
  ChevronUp,
  Heart,
  ListMusic,
  Loader2,
  MicVocal,
  Pause,
  Play,
  Repeat,
  Repeat1,
  Shuffle,
  SkipBack,
  SkipForward,
  Volume,
  Volume1,
  Volume2,
  VolumeX,
} from "lucide-react";
import { KeyboardEvent, PointerEvent, useRef, useState } from "react";

import { Cover, useMusic } from "./context";
import { artistKey, artistsOf, formatDuration } from "./library";
import { usePlaybackTime } from "./player";

interface SliderProps {
  label: string;
  value: number;
  max: number;
  buffered?: number;
  step: number;
  format?: (value: number) => string;
  onChange: (value: number) => void;
  // Seeking applies on release, so dragging does not reload audio each step.
  commitOnRelease?: boolean;
  className?: string;
}

// Slider is a pointer- and keyboard-operable range with a buffered track.
export function Slider({
  label,
  value,
  max,
  buffered,
  step,
  format,
  onChange,
  commitOnRelease,
  className = "",
}: SliderProps) {
  const track = useRef<HTMLDivElement>(null);
  const [dragging, setDragging] = useState<number>();
  const shown = dragging ?? value;
  const fraction = max > 0 ? Math.min(1, Math.max(0, shown / max)) : 0;
  const at = (event: PointerEvent) => {
    const box = track.current!.getBoundingClientRect();
    return Math.min(1, Math.max(0, (event.clientX - box.left) / box.width)) * max;
  };
  const down = (event: PointerEvent<HTMLDivElement>) => {
    if (max <= 0 || event.button !== 0) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    const next = at(event);
    if (commitOnRelease) setDragging(next);
    else onChange(next);
  };
  const move = (event: PointerEvent<HTMLDivElement>) => {
    if (!event.currentTarget.hasPointerCapture(event.pointerId)) return;
    const next = at(event);
    if (commitOnRelease) setDragging(next);
    else onChange(next);
  };
  const up = (event: PointerEvent<HTMLDivElement>) => {
    if (!event.currentTarget.hasPointerCapture(event.pointerId)) return;
    event.currentTarget.releasePointerCapture(event.pointerId);
    if (commitOnRelease) {
      onChange(at(event));
      setDragging(undefined);
    }
  };
  const key = (event: KeyboardEvent) => {
    const delta = {
      ArrowRight: step,
      ArrowUp: step,
      ArrowLeft: -step,
      ArrowDown: -step,
      PageUp: step * 4,
      PageDown: -step * 4,
    }[event.key];
    if (delta === undefined) return;
    event.preventDefault();
    event.stopPropagation();
    onChange(Math.min(max, Math.max(0, value + delta)));
  };
  return (
    <div
      ref={track}
      className={`mu-slider${dragging !== undefined ? " dragging" : ""} ${className}`}
      role="slider"
      tabIndex={0}
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={Math.round(max)}
      aria-valuenow={Math.round(shown)}
      aria-valuetext={format?.(shown)}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={() => setDragging(undefined)}
      onKeyDown={key}
    >
      <span className="mu-slider-rail">
        {buffered !== undefined && max > 0 && (
          <span className="mu-slider-buffered" style={{ width: `${Math.min(100, (buffered / max) * 100)}%` }} />
        )}
        <span className="mu-slider-fill" style={{ width: `${fraction * 100}%` }} />
      </span>
      <span className="mu-slider-thumb" style={{ left: `${fraction * 100}%` }} />
    </div>
  );
}

function Progress() {
  const { player } = useMusic();
  const time = usePlaybackTime(player);
  const duration = time.duration || (player.current?.durationMs ?? 0) / 1000;
  return (
    <div className="mu-progress">
      <span>{formatDuration(time.position * 1000)}</span>
      <Slider
        label="播放进度"
        value={time.position}
        max={duration}
        buffered={time.buffered}
        step={5}
        commitOnRelease
        format={(value) => formatDuration(value * 1000)}
        onChange={(value) => player.seek(value)}
      />
      <span>{formatDuration(duration ? duration * 1000 : undefined)}</span>
    </div>
  );
}

const repeatLabels = { off: "列表播放完停止", all: "列表循环", one: "单曲循环" };

interface Props {
  view: "lyrics" | "queue" | undefined;
  onView: (view: "lyrics" | "queue" | undefined) => void;
}

// PlayerBar is the console along the bottom of the window.
export function PlayerBar({ view, onView }: Props) {
  const music = useMusic();
  const { player } = music;
  const track = player.current;
  const VolumeIcon =
    player.muted || player.volume === 0
      ? VolumeX
      : player.volume < 0.34
        ? Volume
        : player.volume < 0.67
          ? Volume1
          : Volume2;
  const favorite = track ? music.favorites.has(track.id) : false;
  const empty = !player.queue.length;
  return (
    <footer className="mu-player" aria-label="播放控制">
      <div className="mu-now">
        {track ? (
          <>
            <button
              type="button"
              className="mu-now-art"
              aria-label="打开正在播放"
              onClick={() => onView(view === "lyrics" ? undefined : "lyrics")}
            >
              <Cover id={track.coverId} name={track.album || track.title} />
              <span className="mu-now-expand">
                <ChevronUp />
              </span>
            </button>
            <div className="mu-now-text">
              <strong title={track.title}>{track.title}</strong>
              <span className="mu-links">
                {artistsOf(track).map((name, at) => (
                  <span key={name}>
                    {at > 0 && " / "}
                    <button type="button" onClick={() => music.open({ kind: "artist", key: artistKey(name) })}>
                      {name}
                    </button>
                  </span>
                ))}
              </span>
            </div>
            <button
              type="button"
              className={`mu-icon-button like${favorite ? " on" : ""}`}
              aria-label={favorite ? "取消收藏" : "收藏"}
              aria-pressed={favorite}
              onClick={() => music.toggleFavorite(track.id)}
            >
              <Heart />
            </button>
          </>
        ) : (
          <p className="mu-now-idle">选一首歌开始播放</p>
        )}
      </div>

      <div className="mu-controls">
        <div className="mu-buttons">
          <button
            type="button"
            className={`mu-icon-button mode${player.shuffle ? " on" : ""}`}
            aria-label="随机播放"
            aria-pressed={player.shuffle}
            title={player.shuffle ? "随机播放：开" : "随机播放：关"}
            onClick={player.toggleShuffle}
          >
            <Shuffle />
          </button>
          <button
            type="button"
            className="mu-icon-button"
            aria-label="上一首"
            disabled={empty}
            onClick={player.previous}
          >
            <SkipBack />
          </button>
          <button
            type="button"
            className="mu-play"
            aria-label={player.playing ? "暂停" : "播放"}
            disabled={empty}
            onClick={player.toggle}
          >
            {player.loading && player.playing ? <Loader2 className="spin" /> : player.playing ? <Pause /> : <Play />}
          </button>
          <button type="button" className="mu-icon-button" aria-label="下一首" disabled={empty} onClick={player.next}>
            <SkipForward />
          </button>
          <button
            type="button"
            className={`mu-icon-button mode${player.repeat !== "off" ? " on" : ""}`}
            aria-label={repeatLabels[player.repeat]}
            title={repeatLabels[player.repeat]}
            onClick={player.cycleRepeat}
          >
            {player.repeat === "one" ? <Repeat1 /> : <Repeat />}
          </button>
        </div>
        <Progress />
      </div>

      <div className="mu-extras">
        <button
          type="button"
          className={`mu-icon-button${view === "lyrics" ? " on" : ""}`}
          aria-label="歌词"
          aria-pressed={view === "lyrics"}
          title="歌词"
          disabled={!track}
          onClick={() => onView(view === "lyrics" ? undefined : "lyrics")}
        >
          <MicVocal />
        </button>
        <button
          type="button"
          className={`mu-icon-button${view === "queue" ? " on" : ""}`}
          aria-label="播放队列"
          aria-pressed={view === "queue"}
          title="播放队列"
          onClick={() => onView(view === "queue" ? undefined : "queue")}
        >
          <ListMusic />
          {player.queue.length > 0 && <small className="mu-count">{player.queue.length}</small>}
        </button>
        <div className="mu-volume">
          <button
            type="button"
            className="mu-icon-button"
            aria-label={player.muted ? "取消静音" : "静音"}
            onClick={player.toggleMute}
          >
            <VolumeIcon />
          </button>
          <Slider
            label="音量"
            value={player.muted ? 0 : player.volume * 100}
            max={100}
            step={5}
            format={(value) => `${Math.round(value)}%`}
            onChange={(value) => player.setVolume(value / 100)}
            className="volume"
          />
        </div>
      </div>
    </footer>
  );
}
