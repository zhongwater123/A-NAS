import { Inbox, Search, Send, Share2, X } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import { usePeople } from "../people/people";
import { listShares, type Share } from "./api";
import { Avatar, nameOf, names, PeopleStack } from "./avatars";
import { errorText, isUnread, phaseLabel, phaseOf, receipt, remaining, shareTitle, topLevel, when } from "./format";
import palette from "./palette.module.css";
import { EntryTile } from "./parts";
import { ShareDetail } from "./ShareDetail";
import { useShares } from "./SharesProvider";
import styles from "./shares.module.css";

type Direction = "received" | "sent";
// wide: list, files and a docked conversation; split: list and files, the
// conversation slides over; single: one pane at a time.
export type Layout = "wide" | "split" | "single";
const layoutFor = (width: number): Layout => (!width || width >= 1060 ? "wide" : width >= 700 ? "split" : "single");

export function SharesPanel() {
  const { userId, focus, revision, unread } = useShares();
  const [direction, setDirection] = useState<Direction>("received");
  const [items, setItems] = useState<Share[]>([]);
  const [selected, setSelected] = useState<string>();
  const [next, setNext] = useState<number>();
  const [pageCount, setPageCount] = useState(1);
  const [error, setError] = useState("");
  // Each change of direction, revision or pages loads the list again.
  const [loading, setLoading] = useState(true);
  const listing = `${direction}/${revision}/${pageCount}`;
  const [listingBefore, setListingBefore] = useState(listing);
  if (listingBefore !== listing) {
    setListingBefore(listing);
    setLoading(true);
  }
  const [query, setQuery] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [host, setHost] = useState<HTMLDivElement | null>(null);
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    setHost(element);
    setWidth(element.clientWidth);
    if (typeof ResizeObserver !== "function") return;
    const observer = new ResizeObserver(([entry]) => setWidth(Math.round(entry.contentRect.width)));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const layout = layoutFor(width);
  // A share another window asks for opens; a request for none lists the received ones.
  const [focusSeen, setFocusSeen] = useState<typeof focus>();
  if (focus !== focusSeen) {
    setFocusSeen(focus);
    if (focus) {
      setSelected(focus.id || undefined);
      if (!focus.id) {
        setDirection("received");
        setPageCount(1);
      }
    }
  }
  useEffect(() => {
    let stopped = false,
      busy = false;
    const load = async () => {
      if (busy) return;
      busy = true;
      try {
        const collected: Share[] = [];
        let offset = 0,
          nextOffset: number | undefined;
        for (let page = 0; page < pageCount && !stopped; page++) {
          const result = await listShares(direction, offset);
          collected.push(...(result.items ?? []).filter((item) => !collected.some((old) => old.id === item.id)));
          nextOffset = result.nextOffset;
          if (nextOffset === undefined || nextOffset <= offset) break;
          offset = nextOffset;
        }
        if (!stopped) {
          setItems(collected);
          setNext(nextOffset);
          setError("");
        }
      } catch (error) {
        if (!stopped) setError(errorText(error));
      } finally {
        busy = false;
        if (!stopped) setLoading(false);
      }
    };
    void load();
    const timer = setInterval(() => void load(), 5000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [direction, revision, pageCount]);
  const switchTo = (value: Direction) => {
    if (value === direction) return;
    setDirection(value);
    setPageCount(1);
    setItems([]);
    setNext(undefined);
  };
  const people = usePeople();
  const needle = query.trim().toLocaleLowerCase();
  const shown = needle ? items.filter((share) => searchText(share, people, userId).includes(needle)) : items;
  return (
    <div
      ref={root}
      className={`${palette.shPalette} ${styles.shApp} ${styles[`layout-${layout}`] ?? ""}${selected ? ` ${styles.hasDetail}` : ""}`}
    >
      <aside className={styles.shSidebar} aria-label="分享">
        <div className={styles.shSidebarHead}>
          <div className={styles.shSegment}>
            <button
              aria-label="收到的分享"
              aria-pressed={direction === "received"}
              onClick={() => switchTo("received")}
            >
              <Inbox />
              收到{unread.received > 0 && <b className={styles.shCount}>{unread.received}</b>}
            </button>
            <button aria-label="我发出的分享" aria-pressed={direction === "sent"} onClick={() => switchTo("sent")}>
              <Send />
              发出{unread.sent > 0 && <b className={styles.shCount}>{unread.sent}</b>}
            </button>
          </div>
          <label className={styles.shSearch}>
            <Search />
            <input
              type="search"
              aria-label="搜索分享"
              placeholder="搜索文件或成员"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            {query && (
              <button aria-label="清除搜索" onClick={() => setQuery("")}>
                <X />
              </button>
            )}
          </label>
        </div>
        {error && (
          <p role="alert" className={`${styles.shAlert} ${styles.shSidebarAlert}`}>
            {error}
          </p>
        )}
        <nav className={styles.shRows} aria-label="分享列表" aria-busy={loading}>
          {shown.map((share) => (
            <ShareRow
              key={share.id}
              share={share}
              sent={direction === "sent"}
              selected={share.id === selected}
              onOpen={() => setSelected(share.id)}
            />
          ))}
          {!shown.length &&
            (loading && !items.length ? (
              <RowSkeleton />
            ) : (
              <div className={styles.shRowsEmpty}>
                {needle ? (
                  <>
                    <Search />
                    <p>没有匹配的分享</p>
                  </>
                ) : direction === "received" ? (
                  <>
                    <Inbox />
                    <p>还没有收到分享</p>
                  </>
                ) : (
                  <>
                    <Send />
                    <p>还没有发出分享</p>
                    <small>在文件管理中选中文件，点“分享”</small>
                  </>
                )}
              </div>
            ))}
          {next !== undefined && (
            <button className={styles.shMore} disabled={loading} onClick={() => setPageCount((value) => value + 1)}>
              加载更多
            </button>
          )}
        </nav>
      </aside>
      <main className={styles.shMain}>
        {selected ? (
          <ShareDetail
            key={selected}
            id={selected}
            layout={layout}
            layerHost={host}
            preferredRecipient={focus?.id === selected ? focus.recipientId : undefined}
            onBack={() => setSelected(undefined)}
            onDirection={(value) => switchTo(value)}
          />
        ) : (
          <div className={styles.shPlaceholder}>
            <span>
              <Share2 />
            </span>
            <p>选择一个分享，查看文件和私聊</p>
          </div>
        )}
      </main>
    </div>
  );
}

function searchText(share: Share, people: ReturnType<typeof usePeople>, userId: string) {
  const who =
    share.sender.id === userId
      ? share.recipients.map((item) => nameOf(people, item.userId)).join(" ")
      : nameOf(people, share.sender.id, share.sender.username);
  return `${topLevel(share)
    .map((entry) => entry.path)
    .join(" ")} ${who}`.toLocaleLowerCase();
}

function ShareRow({
  share,
  sent,
  selected,
  onOpen,
}: {
  share: Share;
  sent: boolean;
  selected: boolean;
  onOpen: () => void;
}) {
  const { userId } = useShares();
  const people = usePeople();
  const phase = phaseOf(share);
  const unread = isUnread(share, userId);
  const top = topLevel(share);
  const ids = share.recipients.map((item) => item.userId);
  let status: { text: string; tone?: string };
  if (phase !== "active")
    status = {
      text: phase === "preparing" ? "正在准备副本…" : phaseLabel[phase],
      tone: phase === "failed" ? "danger" : "ended",
    };
  else if (sent) {
    const viewed = share.recipients.filter((item) => item.viewedAt).length,
      saved = share.recipients.filter((item) => item.saved).length;
    status = {
      text:
        ids.length === 1
          ? receipt(share.recipients[0]).text
          : `${viewed}/${ids.length} 人已查看${saved ? ` · ${saved} 人已保存` : ""}`,
    };
  } else {
    const me = share.recipients.find((item) => item.userId === userId);
    const left = remaining(share);
    status = !me?.viewedAt
      ? { text: "新分享", tone: "new" }
      : { text: left.text, tone: left.urgent ? "urgent" : undefined };
  }
  return (
    <button
      className={`${styles.shRow}${selected ? ` ${styles.selected}` : ""}${unread ? ` ${styles.unread}` : ""}${phase !== "active" ? ` ${styles.ended}` : ""}`}
      aria-current={selected || undefined}
      onClick={onOpen}
    >
      <span className={styles.shRowFace}>
        {sent ? (
          <PeopleStack people={people} ids={ids} />
        ) : (
          <Avatar people={people} id={share.sender.id} username={share.sender.username} size="lg" />
        )}
      </span>
      <span className={styles.shRowBody}>
        <span className={styles.shRowTop}>
          <strong>
            {sent ? `给 ${names(people, ids, 2)}` : nameOf(people, share.sender.id, share.sender.username)}
          </strong>
          <time dateTime={share.createdAt}>{when(share.createdAt)}</time>
        </span>
        <span className={styles.shRowTitle}>
          {top[0] && <EntryTile name={top[0].path} isDir={top[0].isDir} size="sm" />}
          <span>{shareTitle(share)}</span>
        </span>
        <span className={`${styles.shRowStatus}${status.tone ? ` ${styles[status.tone]}` : ""}`}>{status.text}</span>
      </span>
      {unread && (
        <span className={styles.shDot}>
          <span className={styles.shSr}>未读</span>
        </span>
      )}
    </button>
  );
}

function RowSkeleton() {
  return (
    <div className={styles.shSkeleton} aria-label="正在读取分享">
      {[0, 1, 2].map((index) => (
        <span key={index}>
          <i />
          <span>
            <i />
            <i />
          </span>
        </span>
      ))}
    </div>
  );
}
