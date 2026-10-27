import { GripVertical, ListMusic, X } from "lucide-react";
import { DragEvent, useState } from "react";

import { Cover, carriesTracks, Equalizer, readDrag, useMusic } from "./context";
import { artistLine, formatDuration } from "./library";

const queueDrag = "application/x-anas-music-queue";

// QueuePanel lists what is playing and what plays next. Rows can be dragged
// into a new order, and songs dragged in from a list join the end.
export function QueuePanel({ onClose }: { onClose: () => void }) {
  const music = useMusic();
  const { player, index } = music;
  const [dropAt, setDropAt] = useState<number>();
  const [over, setOver] = useState(false);
  const upcoming = player.queue
    .map((id, position) => ({ id, position, track: index.byId.get(id) }))
    .slice(player.index + 1);
  const current = player.current;

  const onDrop = (event: DragEvent, at?: number) => {
    event.preventDefault();
    setDropAt(undefined);
    setOver(false);
    const from = event.dataTransfer.getData(queueDrag);
    if (from !== "") {
      if (at !== undefined) player.move(Number(from), at);
      return;
    }
    const payload = readDrag(event.dataTransfer);
    if (payload) player.enqueue(payload.ids);
  };
  const accepts = (event: DragEvent) =>
    carriesTracks(event.dataTransfer) || Array.from(event.dataTransfer.types).includes(queueDrag);

  return (
    <aside
      className={`mu-queue${over ? " drop" : ""}`}
      aria-label="播放队列"
      onDragOver={(event) => {
        if (accepts(event)) {
          event.preventDefault();
          setOver(true);
        }
      }}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node)) setOver(false);
      }}
      onDrop={(event) => onDrop(event, upcoming.length ? player.queue.length : undefined)}
    >
      <header>
        <div>
          <h2>播放队列</h2>
          <small>{player.queue.length ? `${player.queue.length} 首` : "空"}</small>
        </div>
        {upcoming.length > 0 && (
          <button type="button" className="mu-text-button" onClick={player.clearUpcoming}>
            清空接下来
          </button>
        )}
        <button type="button" className="mu-icon-button" aria-label="关闭播放队列" onClick={onClose}>
          <X />
        </button>
      </header>
      <div className="mu-queue-list">
        {current && (
          <>
            <h3>正在播放</h3>
            <div className="mu-queue-item current">
              <Cover id={current.coverId} name={current.album || current.title} />
              <span>
                <strong>{current.title}</strong>
                <small>{artistLine(current)}</small>
              </span>
              <Equalizer paused={!player.playing} />
            </div>
          </>
        )}
        {upcoming.length > 0 && <h3>接下来播放</h3>}
        {upcoming.map(({ id, position, track }) => (
          <div
            key={`${id}:${position}`}
            className={`mu-queue-item${dropAt === position ? " drop-before" : ""}`}
            draggable
            onDragStart={(event) => {
              event.dataTransfer.effectAllowed = "move";
              event.dataTransfer.setData(queueDrag, String(position));
            }}
            onDragOver={(event) => {
              if (accepts(event)) {
                event.preventDefault();
                event.stopPropagation();
                setDropAt(position);
              }
            }}
            onDrop={(event) => {
              event.stopPropagation();
              onDrop(event, position);
            }}
            onDoubleClick={() => player.jump(position)}
          >
            <GripVertical className="mu-grip" />
            <button
              type="button"
              className="mu-queue-jump"
              onClick={() => player.jump(position)}
              aria-label={`播放 ${track?.title ?? "歌曲"}`}
            >
              <Cover id={track?.coverId} name={track?.album || track?.title || ""} />
              <span>
                <strong>{track?.title ?? "不可用的歌曲"}</strong>
                <small>{track ? artistLine(track) : ""}</small>
              </span>
            </button>
            <em>{formatDuration(track?.durationMs)}</em>
            <button
              type="button"
              className="mu-icon-button small"
              aria-label={`从队列移除 ${track?.title ?? "歌曲"}`}
              onClick={() => player.remove(position)}
            >
              <X />
            </button>
          </div>
        ))}
        {!player.queue.length && (
          <div className="mu-queue-empty">
            <ListMusic />
            <p>队列是空的</p>
            <small>双击歌曲开始播放，或把歌曲拖到这里。</small>
          </div>
        )}
      </div>
    </aside>
  );
}
