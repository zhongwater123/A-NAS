import { LoaderCircle, MessageSquareText } from "lucide-react";
import {
  PointerEvent as ReactPointerEvent,
  RefObject,
  Suspense,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { ErrorBoundary } from "react-error-boundary";

import { CodeMissing, lazyCode } from "../ui/lazyCode";
import { describeFeedbackError, FeedbackItem, listFeedback } from "./api";
import { FEEDBACK_UI } from "./capture";
import styles from "./feedbackButton.module.css";

const loadPanel = () => import("./FeedbackPanel");
const FeedbackPanel = lazyCode(() => loadPanel().then((module) => module.FeedbackPanel));
// Pointing at the button or focusing it starts the download, so the panel
// is usually there by the time it is clicked.
const preloadPanel = () => void loadPanel().catch(() => undefined);

const ui = { [FEEDBACK_UI]: "" };
// The list refreshes often while someone looks at it, and slowly for the badge.
const OPEN_REFRESH = 15_000;
const CLOSED_REFRESH = 60_000;

// FeedbackChannel is 问题反馈: a button on the right edge of the desktop that
// opens one shared checklist every account can add to, edit, tick off and
// discuss in comments. The desktop shows the button from the start; the
// panel's code and styles load the first time it opens (FeedbackPanel.tsx).
export function FeedbackChannel() {
  const [open, setOpen] = useState(false);
  // Once opened, the panel stays mounted and keeps its draft while closed.
  const [started, setStarted] = useState(false);
  const [items, setItems] = useState<FeedbackItem[]>();
  const [loadError, setLoadError] = useState("");
  const panelID = useId();
  const button = useRef<HTMLButtonElement>(null);
  const wasOpen = useRef(false);

  const refresh = useCallback(
    () =>
      listFeedback().then(
        (list) => {
          setItems(list);
          setLoadError("");
          return list;
        },
        (error: unknown) => {
          setLoadError(describeFeedbackError(error));
          return undefined;
        },
      ),
    [],
  );
  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), open ? OPEN_REFRESH : CLOSED_REFRESH);
    return () => window.clearInterval(timer);
  }, [open, refresh]);
  useEffect(() => {
    if (wasOpen.current && !open) button.current?.focus({ preventScroll: true });
    wasOpen.current = open;
  }, [open]);

  const openCount = (items ?? []).filter((item) => !item.resolved).length;
  const show = () => {
    setOpen(true);
    setStarted(true);
  };
  const close = () => setOpen(false);

  return (
    <>
      {!open && <FeedbackButton ref={button} openCount={openCount} panelID={panelID} onOpen={show} />}
      {started && (
        <ErrorBoundary
          // Code that did not download says so where the panel would be; any
          // other failure goes on to the desktop's boundary, as before.
          fallbackRender={({ error }) => {
            if (!(error instanceof CodeMissing)) throw error;
            return open && <PanelMissing onClose={close} />;
          }}
          onError={(error) => {
            if (error instanceof CodeMissing) console.error("A-NAS feedback panel did not load", error);
          }}
        >
          {/* While the code downloads, the button stays and turns busy. */}
          <Suspense
            fallback={
              open && <FeedbackButton ref={button} openCount={openCount} panelID={panelID} busy onOpen={show} />
            }
          >
            <FeedbackPanel
              id={panelID}
              open={open}
              items={items}
              loadError={loadError}
              onItems={setItems}
              onRefresh={refresh}
              onClose={close}
            />
          </Suspense>
        </ErrorBoundary>
      )}
    </>
  );
}

// PanelMissing stands in for the panel when its code did not download. React
// keeps that failure for as long as the page lives (ui/lazyCode.ts).
function PanelMissing({ onClose }: { onClose: () => void }) {
  // The button that opened it is gone; focus moves here without scrolling
  // the desktop, as the panel's composer does.
  const closeButton = useRef<HTMLButtonElement>(null);
  useEffect(() => closeButton.current?.focus({ preventScroll: true }), []);
  return (
    <section className={styles.fbFailed} {...ui} role="dialog" aria-label="问题反馈">
      <h2>问题反馈没有加载出来</h2>
      <p>A-NAS 可能刚刚更新过，或网络中断了。重新载入页面后再打开，窗口里没保存的内容会丢失。</p>
      <div>
        <button ref={closeButton} type="button" onClick={onClose}>
          关闭
        </button>
        <button type="button" className="primary" onClick={() => window.location.reload()}>
          重新载入页面
        </button>
      </div>
    </section>
  );
}

const POSITION_KEY = "a-nas.feedback.button-top";
const DEFAULT_POSITION = 0.58;

function readPosition() {
  try {
    const value = Number(localStorage.getItem(POSITION_KEY));
    return value > 0 && value < 1 ? value : DEFAULT_POSITION;
  } catch {
    return DEFAULT_POSITION;
  }
}

// FeedbackButton sits on the right edge; dragging it up or down moves it out
// of the way of a window, and the place is remembered.
function FeedbackButton({
  ref,
  openCount,
  panelID,
  busy = false,
  onOpen,
}: {
  ref: RefObject<HTMLButtonElement | null>;
  openCount: number;
  panelID: string;
  // busy while the panel's code downloads.
  busy?: boolean;
  onOpen: () => void;
}) {
  const [top, setTop] = useState(readPosition);
  const [dragging, setDragging] = useState(false);
  const dragged = useRef(false);

  const pointerDown = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0) return;
    const shell = event.currentTarget.offsetParent?.getBoundingClientRect();
    if (!shell || shell.height === 0) return;
    const startY = event.clientY;
    dragged.current = false;
    let latest = top;
    const move = (moveEvent: PointerEvent) => {
      if (!dragged.current && Math.abs(moveEvent.clientY - startY) < 5) return;
      dragged.current = true;
      setDragging(true);
      latest = Math.min(1, Math.max(0, (moveEvent.clientY - shell.top) / shell.height));
      setTop(latest);
    };
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
      setDragging(false);
      if (dragged.current) {
        try {
          localStorage.setItem(POSITION_KEY, String(latest));
        } catch {
          /* the position is a convenience */
        }
      }
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop);
  };

  return (
    <button
      ref={ref}
      className={`${styles.fbFab} ${dragging ? styles.dragging : ""}`}
      {...ui}
      // Below the status bar and clear of the bottom edge on any screen.
      style={{ top: `clamp(160px, ${top * 100}%, calc(100% - 64px))` }}
      aria-label={openCount ? `问题反馈，${openCount} 条待处理` : "问题反馈"}
      aria-haspopup="dialog"
      aria-expanded={false}
      aria-controls={panelID}
      aria-busy={busy || undefined}
      title="问题反馈（可上下拖动）"
      onPointerEnter={preloadPanel}
      onFocus={preloadPanel}
      onPointerDown={pointerDown}
      onClick={() => {
        if (!dragged.current) onOpen();
        dragged.current = false;
      }}
    >
      <span className={styles.fbFabIcon}>{busy ? <LoaderCircle className="spinning" /> : <MessageSquareText />}</span>
      <span className={styles.fbFabLabel}>反馈</span>
      {openCount > 0 && (
        <span className={styles.fbFabCount} aria-hidden="true">
          {openCount > 99 ? "99+" : openCount}
        </span>
      )}
    </button>
  );
}
