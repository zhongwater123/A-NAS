import { CircleAlert, Maximize2, Minus, PanelLeftClose, X } from "lucide-react";
import { Component, ErrorInfo, PointerEvent as ReactPointerEvent, ReactNode } from "react";

import { WINDOW_ID } from "./windowSnapshot";
import { apps } from "./apps";
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
  const beginDrag = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (model.maximized || (event.target as HTMLElement).closest("button")) return;
    event.preventDefault();
    dispatch({ type: "focus", id: model.id });
    const origin = { pointerX: event.clientX, pointerY: event.clientY, x: model.x, y: model.y };
    const move = (moveEvent: PointerEvent) =>
      dispatch({
        type: "move",
        id: model.id,
        x: Math.max(88, origin.x + moveEvent.clientX - origin.pointerX),
        y: Math.max(12, origin.y + moveEvent.clientY - origin.pointerY),
      });
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  };

  const beginResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (model.maximized) return;
    event.preventDefault();
    const origin = { pointerX: event.clientX, pointerY: event.clientY, width: model.width, height: model.height };
    const move = (moveEvent: PointerEvent) =>
      dispatch({
        type: "resize",
        id: model.id,
        width: Math.max(520, origin.width + moveEvent.clientX - origin.pointerX),
        height: Math.max(330, origin.height + moveEvent.clientY - origin.pointerY),
      });
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  };

  const { title, icon: Icon, lightTitle } = apps[model.id];
  // The dock floats over the bottom of the desktop whenever a window is open,
  // so a maximized window ends above it and keeps its bottom controls reachable.
  const style = model.maximized
    ? { left: 88, top: 12, right: 12, bottom: 90, zIndex: model.z }
    : { left: model.x, top: model.y, width: model.width, height: model.height, zIndex: model.z };

  return (
    <section
      className={`app-window app-window-${model.id} ${model.maximized ? "maximized" : ""}`}
      hidden={model.minimized}
      style={style}
      {...{ [WINDOW_ID]: model.id }}
      role="dialog"
      aria-label={title}
      onPointerDown={() => dispatch({ type: "focus", id: model.id })}
    >
      <div className="window-titlebar" onPointerDown={beginDrag}>
        <div className="window-title">
          <span className={`window-app-icon${lightTitle ? " light" : ""}`}>
            <Icon size={17} />
          </span>
          {title}
        </div>
        <div className="window-actions">
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
            className="close"
            aria-label={`关闭${title}`}
            onClick={() => dispatch({ type: "close", id: model.id })}
          >
            <X />
          </button>
        </div>
      </div>
      <div className={`window-content window-content-${model.id}`}>
        <PanelErrorBoundary>{children}</PanelErrorBoundary>
      </div>
      {!model.maximized && <div className="resize-handle" aria-hidden="true" onPointerDown={beginResize} />}
    </section>
  );
}

class PanelErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("A-NAS panel render failed", error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="center-state">
        <CircleAlert />
        <h2>这个页面暂时无法显示</h2>
        <p>桌面和后台服务仍在运行，可以重新载入界面后继续。</p>
        <button className="primary" onClick={() => window.location.reload()}>
          重新载入界面
        </button>
      </div>
    );
  }
}
