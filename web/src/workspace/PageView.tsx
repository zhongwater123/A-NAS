import { useQuery } from "@tanstack/react-query";
import { Check, ChevronDown, Crosshair, Download, Globe, TriangleAlert, X } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { useCallback, useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { relativeTime } from "../lib/time";
import {
  clearSelection,
  fileURL,
  getPage,
  reportViewing,
  setSelection,
  stopViewing,
  viewURL,
  type WorkspacePage,
  type WorkspaceVersion,
} from "./api";
import { type Blocked, type Picked, readFromFrame, type ToFrame } from "./frame/protocol";
import { Presence } from "./Presence";
import { Failed } from "./parts";
import { arrivalText, savedText } from "./words";
import styles from "./workspace.module.css";

export const pageKey = (pageId: string) => ["workspace", "page", pageId] as const;

// Load is what the frame shows: a version, the elements its change touched
// for the helper to point at, and where the reader was.
interface Load {
  version: number;
  highlight?: number[];
  scroll?: number;
}

type Selected = { picked: Picked; version: number; state: "saving" | "saved" | "failed"; error?: string };

// PageView shows one shared page in its sandboxed frame (ADR 0040), filling
// its own browser tab, with its versions, and follows new ones: the reader's
// own (an upload, or their agent's change) replace the page at once and point
// at what changed; another member's are announced above it. It reads the
// page every 3 seconds while the tab can be seen. onClose is the device's own
// screen's way back to the desktop.
export function PageView({
  pageId,
  userId,
  onGone,
  onClose,
}: {
  pageId: string;
  userId: string;
  onGone: () => void;
  onClose?: () => void;
}) {
  const query = useQuery({
    queryKey: pageKey(pageId),
    queryFn: ({ signal }) => getPage(pageId, signal),
    refetchInterval: 3_000,
    refetchIntervalInBackground: false,
  });
  const page = query.data;
  const [load, setLoad] = useState<Load>();
  const [incoming, setIncoming] = useState<WorkspaceVersion>();
  const [seen, setSeen] = useState(0);
  const [scrollY, setScrollY] = useState(0);
  // A newer version than the page had: the reader's own is shown at once,
  // another member's is announced. An older version the reader chose stays.
  if (page && page.latest.version > seen) {
    const latest = page.latest;
    setSeen(latest.version);
    if (seen === 0) {
      setLoad({ version: latest.version });
    } else if (latest.savedBy.id === userId) {
      setIncoming(undefined);
      setLoad({ version: latest.version, highlight: latest.changed, scroll: scrollY });
    } else {
      setIncoming(latest);
    }
  }
  const missing = query.error instanceof APIError && query.error.status === 404;
  useEffect(() => {
    if (missing) onGone();
  }, [missing, onGone]);
  // The tab the member used last is "this page" to their agent, and every
  // member sees who has a page open. The tab says so when it is shown or gets
  // the focus and every minute while it can be seen, and takes it back when
  // it is hidden or closes; the service forgets a tab after three quiet
  // minutes.
  const version = load?.version;
  useEffect(() => {
    if (!version) return;
    const report = () => {
      if (!document.hidden) void reportViewing(pageId, version, document.hasFocus()).catch(() => undefined);
    };
    const shownOrHidden = () => {
      if (document.hidden) void stopViewing(true).catch(() => undefined);
      else report();
    };
    report();
    const again = setInterval(report, 60_000);
    document.addEventListener("visibilitychange", shownOrHidden);
    window.addEventListener("focus", report);
    return () => {
      clearInterval(again);
      document.removeEventListener("visibilitychange", shownOrHidden);
      window.removeEventListener("focus", report);
    };
  }, [pageId, version]);
  useEffect(() => {
    // keepalive: a closing tab does not wait for the answer.
    const leave = () => void stopViewing(true).catch(() => undefined);
    window.addEventListener("pagehide", leave);
    return () => {
      window.removeEventListener("pagehide", leave);
      leave();
    };
  }, [pageId]);

  if (!page || !load) {
    if (query.isError && !missing) return <Failed error={query.error} onRetry={() => void query.refetch()} />;
    return (
      <div className={styles.wsCentered}>
        <span className="loader" role="status" aria-label="正在打开页面" />
      </div>
    );
  }

  const show = (next: Load) => {
    if (next.version === page.latest.version) setIncoming(undefined);
    setLoad(next);
  };
  return (
    <Viewer
      key={pageId}
      page={page}
      userId={userId}
      load={load}
      incoming={incoming}
      onShow={show}
      onDismiss={() => setIncoming(undefined)}
      onScrolled={setScrollY}
      onClose={onClose}
    />
  );
}

function Viewer({
  page,
  userId,
  load,
  incoming,
  onShow,
  onDismiss,
  onScrolled,
  onClose,
}: {
  page: WorkspacePage;
  userId: string;
  load: Load;
  incoming?: WorkspaceVersion;
  onShow: (load: Load) => void;
  onDismiss: () => void;
  onScrolled: (y: number) => void;
  onClose?: () => void;
}) {
  const frame = useRef<HTMLIFrameElement>(null);
  const src = viewURL(page.id, load.version, { highlight: load.highlight, scroll: load.scroll });
  const [ready, setReady] = useState(false);
  const [blocked, setBlocked] = useState<Blocked[]>([]);
  const [errors, setErrors] = useState<string[]>([]);
  const [picking, setPicking] = useState(false);
  // armed is whether the frame was asked to pick: the page's own scripts can
  // post "picked" too, and each one accepted costs the frame a picture.
  const armed = useRef(false);
  const [selected, setSelected] = useState<Selected>();
  const capture = useRef<{ id: number; selection: Selected }>(undefined);
  const captures = useRef(0);

  const post = useCallback((message: ToFrame) => {
    // The frame's origin is opaque: only "*" reaches it.
    frame.current?.contentWindow?.postMessage(message, "*");
  }, []);
  const pick = useCallback(
    (on: boolean) => {
      armed.current = on;
      setPicking(on);
      post({ type: "anas:pick", on });
    },
    [post],
  );

  // A new frame starts afresh.
  const [shownSrc, setShownSrc] = useState(src);
  if (shownSrc !== src) {
    setShownSrc(src);
    setReady(false);
    setBlocked([]);
    setErrors([]);
    setPicking(false);
  }

  const remember = useCallback(
    async (selection: Selected, image?: string) => {
      try {
        await setSelection({ pageId: page.id, version: selection.version, ...selection.picked, image });
        setSelected((current) => (current === selection ? { ...selection, state: "saved" } : current));
      } catch (error) {
        setSelected((current) =>
          current === selection ? { ...selection, state: "failed", error: errorText(error, "workspace") } : current,
        );
      }
    },
    [page.id],
  );

  useEffect(() => {
    const receive = (event: MessageEvent) => {
      // Only this page's frame, whose origin is opaque, speaks here.
      if (!frame.current || event.source !== frame.current.contentWindow || event.origin !== "null") return;
      const message = readFromFrame(event.data);
      if (!message) return;
      switch (message.type) {
        case "anas:ready":
          // A new frame, not picking.
          armed.current = false;
          setReady(true);
          setBlocked((current) => merge(current, message.blocked));
          break;
        case "anas:blocked":
          setBlocked((current) => merge(current, [message]));
          break;
        case "anas:error":
          setErrors((current) =>
            current.includes(message.message) ? current : [...current, message.message].slice(-5),
          );
          break;
        case "anas:picked": {
          // One element each time the member asks to pick.
          if (!armed.current) break;
          pick(false);
          const { type: _, ...picked } = message;
          const selection: Selected = { picked, version: load.version, state: "saving" };
          setSelected(selection);
          // A picture of the element and what is around it.
          const id = ++captures.current;
          capture.current = { id, selection };
          const { x, y, w, h } = picked.rect;
          post({ type: "anas:capture", id, rect: { x: x - 48, y: y - 48, w: w + 96, h: h + 96 } });
          break;
        }
        case "anas:captured":
          if (capture.current?.id === message.id) {
            const { selection } = capture.current;
            capture.current = undefined;
            void remember(selection, message.image);
          }
          break;
        case "anas:scrolled":
          onScrolled(message.y);
          break;
      }
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [load.version, onScrolled, pick, post, remember]);

  const unselect = () => {
    setSelected(undefined);
    capture.current = undefined;
    post({ type: "anas:clear" });
    void clearSelection().catch(() => undefined);
  };

  const current = page.versions.find((item) => item.version === load.version);
  return (
    <section className={styles.wsViewer} aria-label={page.title}>
      <header className={styles.wsBar}>
        <h1 className={styles.wsTitle} title={page.title}>
          {page.title}
        </h1>
        <VersionMenu page={page} version={load.version} onPick={(version) => onShow({ version })} />
        <Presence presence={page.presence} userId={userId} latest={page.latest.version} />
        <button
          type="button"
          className={`${styles.wsButton} ${picking ? styles.on : ""}`}
          aria-pressed={picking}
          disabled={!ready}
          onClick={() => pick(!picking)}
        >
          <Crosshair size={15} aria-hidden="true" />
          选取
        </button>
        <a className={styles.wsButton} href={fileURL(page.id, load.version)} download>
          <Download size={15} aria-hidden="true" />
          导出
        </a>
        {onClose && (
          <button type="button" className={styles.wsButton} onClick={onClose}>
            <X size={15} aria-hidden="true" />
            关闭
          </button>
        )}
      </header>
      {incoming && (
        <div className={`${styles.wsNotice} ${styles.news}`} role="status">
          <span>{arrivalText(incoming)}</span>
          <button type="button" className={styles.wsLink} onClick={() => onShow({ version: incoming.version })}>
            查看
          </button>
          <button type="button" className={styles.wsNoticeClose} aria-label="关闭提示" onClick={onDismiss}>
            <X size={14} />
          </button>
        </div>
      )}
      {current && load.version !== page.latest.version && !incoming && (
        <div className={styles.wsNotice}>
          <span>
            正在看 v{current.version}（{savedText(current)}，{relativeTime(current.savedAt)}），最新是 v
            {page.latest.version}
          </span>
          <button type="button" className={styles.wsLink} onClick={() => onShow({ version: page.latest.version })}>
            看最新
          </button>
        </div>
      )}
      {blocked.length > 0 && (
        <details className={`${styles.wsNotice} ${styles.warn}`}>
          <summary>
            <Globe size={14} aria-hidden="true" />
            {blocked.length} 项外网资源未加载
          </summary>
          <ul>
            {blocked.map((item) => (
              <li key={`${item.directive} ${item.uri}`}>{item.uri}</li>
            ))}
          </ul>
        </details>
      )}
      {errors.length > 0 && (
        <div className={`${styles.wsNotice} ${styles.warn}`} role="status">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>页面脚本出错：{errors[errors.length - 1]}</span>
        </div>
      )}
      <div className={styles.wsStage}>
        {!ready && <div className={styles.wsProgress} role="progressbar" aria-label="正在载入页面" />}
        <iframe
          key={src}
          ref={frame}
          className={styles.wsFrame}
          src={src}
          title={`页面：${page.title}`}
          // An origin of its own: no cookies, no desktop, no network (ADR 0040).
          sandbox="allow-scripts"
        />
        {picking && <div className={styles.wsPickHint}>点页面里的元素选中它</div>}
        {selected && (
          <div className={styles.wsSelection} role="status">
            <Crosshair size={15} aria-hidden="true" />
            <span className={styles.wsSelectionWhat}>
              已选中 <code>{selected.picked.tag}</code>
              {selected.picked.text && <> “{selected.picked.text.slice(0, 40)}”</>}
              {selected.version !== load.version && <>（v{selected.version}）</>}
            </span>
            <span className={styles.wsSelectionState}>
              {selected.state === "saving" && "正在记下…"}
              {selected.state === "saved" && (
                <>
                  <Check size={14} aria-hidden="true" />
                  你的 agent 能读到它
                </>
              )}
              {selected.state === "failed" && `没能记下：${selected.error}`}
            </span>
            <button type="button" className={styles.wsNoticeClose} aria-label="清除选中" onClick={unselect}>
              <X size={14} />
            </button>
          </div>
        )}
      </div>
    </section>
  );
}

// VersionMenu lists the page's versions, newest first, and opens one.
function VersionMenu({
  page,
  version,
  onPick,
}: {
  page: WorkspacePage;
  version: number;
  onPick: (version: number) => void;
}) {
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger className={styles.wsButton} aria-label={`版本：v${version}`}>
        v{version}
        {version === page.latest.version && <span className={styles.wsFaint}>最新</span>}
        <ChevronDown size={14} aria-hidden="true" />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content className={styles.wsVersions} align="end" sideOffset={6}>
          {page.versions.map((item) => (
            <DropdownMenu.Item
              key={item.version}
              className={styles.wsVersion}
              data-current={item.version === version || undefined}
              onSelect={() => onPick(item.version)}
            >
              <b>v{item.version}</b>
              <span className={styles.wsVersionWho}>
                {savedText(item)} · {relativeTime(item.savedAt)}
              </span>
              {item.note && <span className={styles.wsVersionNote}>{item.note}</span>}
            </DropdownMenu.Item>
          ))}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function merge(current: Blocked[], more: Blocked[]): Blocked[] {
  const next = [...current];
  for (const item of more) {
    if (!next.some((seen) => seen.uri === item.uri && seen.directive === item.directive)) next.push(item);
  }
  return next.slice(0, 50);
}
