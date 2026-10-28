import { CircleAlert, CloudOff, Maximize2, Minus, PanelLeftClose, X } from "lucide-react";
import { ErrorInfo, ReactNode, PointerEvent as ReactPointerEvent, useState } from "react";
import { ErrorBoundary, type FallbackProps } from "react-error-boundary";

import { CodeMissing } from "../ui/lazyCode";
import { apps } from "./apps";
import styles from "./desktop.module.css";
import { WINDOW_ID } from "./windowSnapshot";
import { WindowAction, WindowModel } from "./windows";

export function AppWindow({
  model,
  dispatch,
  children,
}: {
  model: WindowModel;
  dispatch: (action: WindowAction) => void;
  children: ReactNode;
}) {
  // While the pointer moves, the window keeps its place and size here and
  // tells the desktop once released, so the desktop and the other windows do
  // not re-render on every move. The pressed title bar or handle captures the
  // pointer: its moves arrive wherever the pointer goes, a press the browser
  // cancels puts the window back, and the listeners go with the element.
  const [moving, setMoving] = useState<{ x: number; y: number }>();
  const [sizing, setSizing] = useState<{ width: number; height: number }>();
  const follow = <Value,>(
    press: ReactPointerEvent<HTMLElement>,
    update: (moveEvent: PointerEvent) => Value,
    show: (value?: Value) => void,
    done: (value: Value) => void,
  ) => {
    const target = press.currentTarget;
    target.setPointerCapture(press.pointerId);
    let latest: Value | undefined;
    const move = (moveEvent: PointerEvent) => {
      latest = update(moveEvent);
      show(latest);
    };
    const release = () => {
      if (latest !== undefined) done(latest);
    };
    target.addEventListener("pointermove", move);
    target.addEventListener("pointerup", release, { once: true });
    // Follows pointerup, or pointercancel without one.
    target.addEventListener(
      "lostpointercapture",
      () => {
        target.removeEventListener("pointermove", move);
        target.removeEventListener("pointerup", release);
        show(undefined);
      },
      { once: true },
    );
  };

  const beginDrag = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (model.maximized || (event.target as HTMLElement).closest("button")) return;
    event.preventDefault();
    dispatch({ type: "focus", id: model.id });
    const origin = { pointerX: event.clientX, pointerY: event.clientY, x: model.x, y: model.y };
    follow(
      event,
      (moveEvent) => ({
        x: Math.max(88, origin.x + moveEvent.clientX - origin.pointerX),
        y: Math.max(12, origin.y + moveEvent.clientY - origin.pointerY),
      }),
      setMoving,
      ({ x, y }) => dispatch({ type: "move", id: model.id, x, y }),
    );
  };

  const beginResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (model.maximized) return;
    event.preventDefault();
    const origin = { pointerX: event.clientX, pointerY: event.clientY, width: model.width, height: model.height };
    follow(
      event,
      (moveEvent) => ({
        width: Math.max(520, origin.width + moveEvent.clientX - origin.pointerX),
        height: Math.max(330, origin.height + moveEvent.clientY - origin.pointerY),
      }),
      setSizing,
      ({ width, height }) => dispatch({ type: "resize", id: model.id, width, height }),
    );
  };

  const { title, icon: Icon, lightTitle } = apps[model.id];
  // The dock floats over the bottom of the desktop whenever a window is open,
  // so a maximized window ends above it and keeps its bottom controls reachable.
  const style = model.maximized
    ? { left: 88, top: 12, right: 12, bottom: 90, zIndex: model.z }
    : {
        left: model.x,
        top: model.y,
        width: model.width,
        height: model.height,
        ...(moving && { left: moving.x, top: moving.y }),
        ...sizing,
        zIndex: model.z,
      };

  return (
    <section
      className={`${styles.appWindow} ${styles[`app-window-${model.id}`] ?? ""} ${model.maximized ? styles.maximized : ""}`}
      hidden={model.minimized}
      style={style}
      {...{ [WINDOW_ID]: model.id }}
      role="dialog"
      aria-label={title}
      onPointerDown={() => dispatch({ type: "focus", id: model.id })}
    >
      <div className={styles.windowTitlebar} onPointerDown={beginDrag}>
        <div className={styles.windowTitle}>
          <span className={`${styles.windowAppIcon}${lightTitle ? ` ${styles.light}` : ""}`}>
            <Icon size={17} />
          </span>
          {title}
        </div>
        <div className={styles.windowActions}>
          <button aria-label={`最小化${title}`} onClick={() => dispatch({ type: "minimize", id: model.id })}>
            <Minus />
          </button>
          <button
            aria-label={`${model.maximized ? "还原" : "最大化"}${title}`}
            onClick={() => dispatch({ type: "maximize", id: model.id })}
          >
            {model.maximized ? <PanelLeftClose /> : <Maximize2 />}
          </button>
          <button
            className={styles.close}
            aria-label={`关闭${title}`}
            onClick={() => dispatch({ type: "close", id: model.id })}
          >
            <X />
          </button>
        </div>
      </div>
      <div className={`${styles.windowContent} ${styles[`window-content-${model.id}`] ?? ""}`}>
        <ErrorBoundary FallbackComponent={WindowFailed} onError={logWindowError(model.id)}>
          {children}
        </ErrorBoundary>
      </div>
      {!model.maximized && <div className={styles.resizeHandle} aria-hidden="true" onPointerDown={beginResize} />}
    </section>
  );
}

const logWindowError = (id: string) => (error: unknown, info: ErrorInfo) =>
  console.error(`A-NAS ${id} window failed`, error, info.componentStack);

// WindowFailed replaces a window whose content threw. Reopening mounts the
// content afresh; the desktop and the other windows keep running. A window
// whose code did not download comes back only with the page.
function WindowFailed({ error, resetErrorBoundary }: FallbackProps) {
  if (error instanceof CodeMissing)
    return (
      <div className={styles.centerState}>
        <CloudOff />
        <h2>这个窗口没有加载出来</h2>
        <p>A-NAS 可能刚刚更新过，或网络中断了。重新载入页面后再打开，其他窗口里没保存的内容会丢失。</p>
        <button className="primary" onClick={() => window.location.reload()}>
          重新载入页面
        </button>
      </div>
    );
  return (
    <div className={styles.centerState}>
      <CircleAlert />
      <h2>这个窗口暂时无法显示</h2>
      <p>桌面和其他窗口不受影响，可以重新打开这个窗口。</p>
      <button className="primary" onClick={resetErrorBoundary}>
        重新打开
      </button>
    </div>
  );
}
