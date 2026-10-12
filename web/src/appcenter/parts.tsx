import { Check, Loader } from "lucide-react";
import { CSSProperties, KeyboardEvent, ReactNode, Suspense, lazy, useEffect, useLayoutEffect, useRef, useState } from "react";

import { AppJob, CatalogApp, iconURL } from "../appsApi";
import { appURL, openApp } from "./model";

const loadMarkdown = () => import("./AppMarkdown");
const AppMarkdown = lazy(loadMarkdown);
// Loading the renderer with the App Center keeps raw Markdown from flashing on an app's page.
export const preloadMarkdown = () => void loadMarkdown();

export function AppIcon({ app, size = "md" }: { app: CatalogApp; size?: "sm" | "md" | "lg" | "xl" }) {
  const [failed, setFailed] = useState(false);
  return (
    <span className={`ac-icon ${size}`} aria-hidden="true">
      {failed ? <b>{app.title.slice(0, 1).toUpperCase()}</b> : <img src={iconURL(app.id)} alt="" draggable={false} onError={() => setFailed(true)} />}
    </span>
  );
}

// The compact action on a card or row: install, open, or what is happening.
export function AppAction({ app, onInstall }: { app: CatalogApp; onInstall: (app: CatalogApp) => void }) {
  if (app.state === "installing" || app.state === "uninstalling") {
    return <span className="ac-get busy"><Loader className="ac-spin" />{app.state === "installing" ? "安装中" : "卸载中"}</span>;
  }
  if (app.state === "installed") {
    return appURL(app)
      ? <button className="ac-get open" aria-label={`打开 ${app.title}`} onClick={() => openApp(app)}>打开</button>
      : <span className="ac-get done"><Check />已安装</span>;
  }
  return <button className="ac-get" aria-label={`安装 ${app.title}`} onClick={() => onInstall(app)}>安装</button>;
}

// Manifest Markdown; plain text shows while the renderer loads.
export function RichText({ text }: { text: string }) {
  return (
    <div className="ac-prose">
      <Suspense fallback={<p className="ac-prose-plain">{text}</p>}>
        <AppMarkdown>{text}</AppMarkdown>
      </Suspense>
    </div>
  );
}

// Long text folds to a few lines with a control to show the rest.
export function Folded({ children, lines = 6 }: { children: ReactNode; lines?: number }) {
  const body = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [overflows, setOverflows] = useState(false);
  // Watch the content rather than the clipped box: Markdown arrives after the first paint.
  useLayoutEffect(() => {
    if (open || !body.current || !content.current) return;
    const measure = () => setOverflows(content.current!.offsetHeight > body.current!.clientHeight + 4);
    measure();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(measure);
    observer?.observe(content.current);
    return () => observer?.disconnect();
  }, [open]);
  return (
    <div className={`ac-folded ${open ? "open" : ""} ${overflows ? "overflows" : ""}`} style={{ "--ac-fold-lines": lines } as CSSProperties}>
      <div ref={body} className="ac-folded-body"><div ref={content}>{children}</div></div>
      {(overflows || open) && <button className="ac-link-button" onClick={() => setOpen(!open)}>{open ? "收起" : "展开全部"}</button>}
    </div>
  );
}

export function JobOutput({ job, summary = "显示详细输出", open }: { job: AppJob; summary?: string; open?: boolean }) {
  if (job.output.length === 0) return null;
  return (
    <details className="ac-output" open={open}>
      <summary>{summary}</summary>
      <pre>{job.output.join("\n")}</pre>
    </details>
  );
}

export function Progress() {
  return <span className="ac-progress" aria-hidden="true"><i /></span>;
}

// A window-modal layer over the App Center that takes focus; Escape or a
// click beside it dismisses it. The panel makes the rest inert meanwhile.
export function Layer({ label, onDismiss, children, size = "md" }: { label: string; onDismiss?: () => void; children: ReactNode; size?: "sm" | "md" }) {
  const sheet = useRef<HTMLDivElement>(null);
  useEffect(() => sheet.current?.focus(), []);
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "Escape" || !onDismiss) return;
    event.stopPropagation();
    onDismiss();
  };
  return (
    <div className="ac-layer" onKeyDown={onKeyDown} onPointerDown={(event) => { if (event.target === event.currentTarget) onDismiss?.(); }}>
      <div ref={sheet} className={`ac-sheet ${size}`} role="dialog" aria-modal="true" aria-label={label} tabIndex={-1}>
        {children}
      </div>
    </div>
  );
}
