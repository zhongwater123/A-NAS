import { AudioLines, ChevronsUpDown, LibraryBig, Search, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { messageOf } from "./Browse";
import { Artwork } from "./Cards";
import { ChoiceList, type Choice } from "./Dropdown";
import { episodeCode, progressShare, runtime } from "./format";
import {
  getShow,
  listLibraries,
  listTitles,
  type Category,
  type MediaLibrary,
  type ShowDetail,
  type Title,
  type VideoDetail,
} from "./mediaApi";

export type PanelTab = "episodes" | "library";

const categories: { value: Category; label: string }[] = [
  { value: "movie", label: "电影" },
  { value: "show", label: "电视剧" },
  { value: "other", label: "其他" },
];

function NowPlaying() {
  return (
    <span className="mc-pick-now">
      <AudioLines />
      正在播放
    </span>
  );
}

// PlayerPanel lets the viewer pick another episode of the show, or another
// title of the library, without leaving the player.
export function PlayerPanel({
  detail,
  tab,
  onTab,
  onPick,
  onClose,
}: {
  detail: VideoDetail;
  tab: PanelTab;
  onTab: (tab: PanelTab) => void;
  onPick: (videoId: string) => void;
  onClose: () => void;
}) {
  const episodic = detail.type === "episode" && Boolean(detail.showId);
  return (
    <aside
      className="mc-player-panel"
      aria-label={tab === "episodes" ? "选集" : "片库"}
      onPointerDown={(event) => event.stopPropagation()}
    >
      <header>
        <div className="mc-pick-tabs" role="tablist" aria-label="选择播放内容">
          {episodic && (
            <button
              type="button"
              role="tab"
              aria-selected={tab === "episodes"}
              className={tab === "episodes" ? "active" : ""}
              onClick={() => onTab("episodes")}
            >
              选集
            </button>
          )}
          <button
            type="button"
            role="tab"
            aria-selected={tab === "library"}
            className={tab === "library" ? "active" : ""}
            onClick={() => onTab("library")}
          >
            片库
          </button>
        </div>
        <button type="button" className="mc-player-button" aria-label="收起" onClick={onClose}>
          <X />
        </button>
      </header>
      {tab === "episodes" && episodic ? (
        <Episodes detail={detail} onPick={onPick} />
      ) : (
        <Library detail={detail} onPick={onPick} />
      )}
    </aside>
  );
}

function Episodes({ detail, onPick }: { detail: VideoDetail; onPick: (videoId: string) => void }) {
  const [show, setShow] = useState<ShowDetail>();
  const [season, setSeason] = useState(detail.season ?? 1);
  const [error, setError] = useState("");
  const current = useRef<HTMLLIElement>(null);
  useEffect(() => {
    let live = true;
    getShow(detail.showId!).then(
      (value) => {
        if (live) setShow(value);
      },
      (caught) => {
        if (live) setError(messageOf(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [detail.showId]);
  const episodes = show?.seasonList.find((item) => item.number === season)?.episodes ?? [];
  useEffect(() => {
    current.current?.scrollIntoView?.({ block: "center" });
  }, [show, season]);
  if (error) return <p className="mc-pick-empty">{error}</p>;
  if (!show) return <span className="mc-spinner small mc-pick-loading" />;
  return (
    <>
      {show.seasonList.length > 1 && (
        <div className="mc-pick-chips" role="group" aria-label="季">
          {show.seasonList.map((item) => (
            <button
              key={item.number}
              type="button"
              className={item.number === season ? "active" : ""}
              aria-pressed={item.number === season}
              onClick={() => setSeason(item.number)}
            >
              {item.number ? `第 ${item.number} 季` : "特别篇"}
            </button>
          ))}
        </div>
      )}
      <ol className="mc-pick-list episodes" aria-label="剧集">
        {episodes.map((episode) => {
          const playing = episode.id === detail.id;
          const share = progressShare(episode.progress);
          const title: Title = {
            id: episode.id,
            type: "episode",
            libraryId: show.libraryId,
            title: episode.title,
            addedAt: episode.addedAt,
            artwork: episode.artwork.thumb ? episode.artwork : show.artwork,
            favorite: false,
            showTitle: show.title,
          };
          return (
            <li key={episode.id} ref={playing ? current : undefined}>
              <button
                type="button"
                className={`mc-pick ${playing ? "playing" : ""}`}
                aria-current={playing ? "true" : undefined}
                onClick={() => {
                  if (!playing) onPick(episode.id);
                }}
              >
                <span className="mc-pick-thumb">
                  <Artwork title={title} kind="thumb" fallback="backdrop" label={false} />
                  {share > 0 && !episode.progress?.watched && (
                    <span className="mc-progress">
                      <span style={{ width: `${share * 100}%` }} />
                    </span>
                  )}
                </span>
                <span className="mc-pick-text">
                  <strong>{episodeCode(episode.season, episode.episode) || episode.title}</strong>
                  <small>
                    {episode.title && !episode.title.startsWith("第 ") ? episode.title : runtime(episode.duration)}
                  </small>
                  {playing ? <NowPlaying /> : episode.progress?.watched ? <em>已看完</em> : null}
                </span>
              </button>
            </li>
          );
        })}
      </ol>
    </>
  );
}

// LibrarySwitch shows which library the list comes from. Pressing it moves
// on to the next library; its arrows open them all to pick from.
function LibrarySwitch({
  value,
  choices,
  onChange,
}: {
  value: string;
  choices: Choice[];
  onChange: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);
  const index = Math.max(
    0,
    choices.findIndex((choice) => choice.value === value),
  );
  const current = choices[index];
  const following = choices[(index + 1) % choices.length];
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) toggle.current?.focus({ preventScroll: true });
  };
  return (
    <div ref={box} className={`mc-scope ${open ? "open" : ""}`}>
      <button
        type="button"
        className="mc-scope-name"
        title={`切换到${following.label}`}
        aria-label={`媒体库：${current.label}，切换到${following.label}`}
        onClick={() => onChange(following.value)}
      >
        <LibraryBig />
        <span key={current.value}>{current.label}</span>
      </button>
      <button
        ref={toggle}
        type="button"
        className="mc-scope-toggle"
        aria-label="选择媒体库"
        title="选择媒体库"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <ChevronsUpDown />
      </button>
      <ChoiceList
        anchor={box}
        open={open}
        choices={choices}
        value={value}
        label="媒体库"
        align="end"
        onPick={(next) => {
          close(true);
          if (next !== value) onChange(next);
        }}
        onClose={close}
      />
    </div>
  );
}

function Library({ detail, onPick }: { detail: VideoDetail; onPick: (videoId: string) => void }) {
  const [category, setCategory] = useState<Category>(
    detail.type === "episode" ? "show" : detail.type === "other" ? "other" : "movie",
  );
  // scope is the library the list comes from; empty means every library.
  const [scope, setScope] = useState(detail.libraryId);
  const [libraries, setLibraries] = useState<MediaLibrary[]>([]);
  useEffect(() => {
    let live = true;
    listLibraries().then(
      (result) => {
        if (live) setLibraries(result.items);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, []);
  const scopes = useMemo<Choice[]>(
    () => [
      ...libraries.map((library) => ({ value: library.id, label: library.name })),
      { value: "", label: "全部媒体库" },
    ],
    [libraries],
  );
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<Title[]>();
  const [error, setError] = useState("");
  const [opening, setOpening] = useState("");
  const search = query.trim();
  useEffect(() => {
    let live = true;
    setItems(undefined);
    setError("");
    // Typing waits for a pause before it asks again.
    const timer = window.setTimeout(
      () => {
        listTitles({
          category,
          library: scope || undefined,
          q: search || undefined,
          sort: search ? "name" : "added",
          limit: 80,
        }).then(
          (page) => {
            if (live) setItems(page.items);
          },
          (caught) => {
            if (live) {
              setItems([]);
              setError(messageOf(caught));
            }
          },
        );
      },
      search ? 250 : 0,
    );
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [category, scope, search]);
  const playingID = detail.type === "episode" ? detail.showId : detail.id;
  const pick = async (title: Title) => {
    if (title.id === playingID) return;
    if (title.type !== "show") {
      onPick(title.id);
      return;
    }
    // A show plays its next episode, as its card does.
    setOpening(title.id);
    try {
      const show = await getShow(title.id);
      const next = show.next?.id ?? show.seasonList[0]?.episodes[0]?.id;
      if (next) onPick(next);
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setOpening("");
    }
  };
  const still = category === "other";
  const empty = useMemo(
    () => (search ? "没有找到匹配的影片" : scope ? "这个媒体库里还没有这类视频" : "还没有这类视频"),
    [search, scope],
  );
  return (
    <>
      <div className="mc-pick-filters">
        <div className="mc-pick-row">
          <label className="mc-pick-search">
            <Search />
            <input
              value={query}
              placeholder="搜索片名"
              aria-label="搜索片库"
              onChange={(event) => setQuery(event.target.value)}
            />
          </label>
          {libraries.length > 1 && <LibrarySwitch value={scope} choices={scopes} onChange={setScope} />}
        </div>
        <div className="mc-pick-chips" role="group" aria-label="分类">
          {categories.map((item) => (
            <button
              key={item.value}
              type="button"
              className={item.value === category ? "active" : ""}
              aria-pressed={item.value === category}
              onClick={() => setCategory(item.value)}
            >
              {item.label}
            </button>
          ))}
        </div>
      </div>
      {error && <p className="mc-pick-empty">{error}</p>}
      {!items ? (
        <span className="mc-spinner small mc-pick-loading" />
      ) : !items.length && !error ? (
        <p className="mc-pick-empty">{empty}</p>
      ) : (
        <ul className={`mc-pick-list ${still ? "stills" : "posters"}`} aria-label="影片">
          {items.map((title) => {
            const playing = title.id === playingID;
            const share = progressShare(title.progress);
            return (
              <li key={title.id}>
                <button
                  type="button"
                  className={`mc-pick ${playing ? "playing" : ""}`}
                  aria-current={playing ? "true" : undefined}
                  disabled={opening === title.id}
                  onClick={() => void pick(title)}
                >
                  <span className={`mc-pick-thumb ${still ? "" : "poster"}`}>
                    <Artwork
                      title={title}
                      kind={still ? "thumb" : "poster"}
                      fallback={still ? "backdrop" : "thumb"}
                      label={false}
                    />
                    {share > 0 && !title.progress?.watched && (
                      <span className="mc-progress">
                        <span style={{ width: `${share * 100}%` }} />
                      </span>
                    )}
                  </span>
                  <span className="mc-pick-text">
                    <strong>{title.title}</strong>
                    <small>
                      {[
                        title.year ? String(title.year) : "",
                        title.type === "show" && title.episodes ? `${title.episodes} 集` : runtime(title.duration),
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </small>
                    {playing ? <NowPlaying /> : opening === title.id ? <em>正在打开…</em> : null}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}
