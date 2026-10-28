import { Check, Copy, Eye, EyeOff, Loader, TriangleAlert } from "lucide-react";
import { CSSProperties, KeyboardEvent, ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";

import { copyText } from "../lib/clipboard";
import { AppCredential, AppJob, CatalogApp, iconURL } from "./api";
import styles from "./appcenter.module.css";
import { appURL, openApp } from "./model";

// The Markdown renderer is its own chunk. Once loaded it renders synchronously
// (React.lazy would still suspend once), so loading it with the App Center
// keeps raw Markdown from flashing on an app's page.
let markdown: typeof import("./AppMarkdown") | undefined;
let loadingMarkdown: Promise<unknown> | undefined;
export function preloadMarkdown(): Promise<unknown> {
  loadingMarkdown ??= import("./AppMarkdown").then((module) => {
    markdown = module;
  });
  return loadingMarkdown;
}

export function AppIcon({ app, size = "md" }: { app: CatalogApp; size?: "sm" | "md" | "lg" | "xl" }) {
  const [failed, setFailed] = useState(false);
  return (
    <span className={`${styles.acIcon} ${styles[size] ?? ""}`} aria-hidden="true">
      {failed ? (
        <b>{app.title.slice(0, 1).toUpperCase()}</b>
      ) : (
        <img src={iconURL(app.id)} alt="" draggable={false} onError={() => setFailed(true)} />
      )}
    </span>
  );
}

// The compact action on a card or row: install, open, or what is happening.
export function AppAction({ app, onInstall }: { app: CatalogApp; onInstall: (app: CatalogApp) => void }) {
  if (app.state === "installing" || app.state === "uninstalling") {
    return (
      <span className={`${styles.acGet} ${styles.busy}`}>
        <Loader className={styles.acSpin} />
        {app.state === "installing" ? "安装中" : "卸载中"}
      </span>
    );
  }
  if (app.state === "installed") {
    return appURL(app) ? (
      <button
        className={`${styles.acGet} ${styles.open}`}
        aria-label={`打开 ${app.title}`}
        onClick={() => openApp(app)}
      >
        打开
      </button>
    ) : (
      <span className={`${styles.acGet} ${styles.done}`}>
        <Check />
        已安装
      </span>
    );
  }
  return (
    <button className={styles.acGet} aria-label={`安装 ${app.title}`} onClick={() => onInstall(app)}>
      安装
    </button>
  );
}

// Manifest Markdown. Until the renderer has loaded, a few placeholder lines
// stand in, so raw Markdown never reaches the screen or a screen reader.
export function RichText({ text }: { text: string }) {
  const [, setLoaded] = useState(Boolean(markdown));
  useEffect(() => {
    if (!markdown) void preloadMarkdown().then(() => setLoaded(true));
  }, []);
  const Markdown = markdown?.default;
  if (!Markdown) {
    return (
      <div className={`${styles.acProse} ${styles.acProseLoading}`} role="status" aria-label="正在载入说明">
        <span />
        <span />
        <span />
      </div>
    );
  }
  return (
    <div className={styles.acProse}>
      <Markdown>{text}</Markdown>
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
    <div
      className={`${styles.acFolded} ${open ? styles.open : ""} ${overflows ? styles.overflows : ""}`}
      style={{ "--ac-fold-lines": lines } as CSSProperties}
    >
      <div ref={body} className={styles.acFoldedBody}>
        <div ref={content}>{children}</div>
      </div>
      {(overflows || open) && (
        <button className={styles.acLinkButton} onClick={() => setOpen(!open)}>
          {open ? "收起" : "展开全部"}
        </button>
      )}
    </div>
  );
}

export function JobOutput({ job, summary = "显示详细输出", open }: { job: AppJob; summary?: string; open?: boolean }) {
  if (job.output.length === 0) return null;
  return (
    <details className={styles.acOutput} open={open}>
      <summary>{summary}</summary>
      <pre>{job.output.join("\n")}</pre>
    </details>
  );
}

// The logins A-NAS generated for an app, hidden until asked for. An app
// installed before they were generated still uses the manifest's published
// ones, which the owner should change.
export function Logins({ credentials }: { credentials: AppCredential[] }) {
  const upstream = credentials.some((credential) => credential.upstream);
  return (
    <div className={`${styles.acLogins} ${upstream ? "upstream" : ""}`}>
      {credentials.map((credential) => (
        <Login key={credential.label} credential={credential} />
      ))}
      {upstream ? (
        <p className={styles.acLoginsWarning}>
          <TriangleAlert />
          这是安装清单公开的默认密码，任何人都查得到。应用安装于 A-NAS 生成独立密码之前，请在应用里改成自己的密码。
        </p>
      ) : (
        <small>
          由 A-NAS 为本机随机生成，只有管理员能看到。在应用里改过密码，或重新安装时沿用了更早的数据，以那时的密码为准。
        </small>
      )}
    </div>
  );
}

function Login({ credential }: { credential: AppCredential }) {
  const [shown, setShown] = useState(false);
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1600);
    return () => window.clearTimeout(timer);
  }, [copied]);
  return (
    <dl className={styles.acLogin}>
      {credential.username && (
        <div>
          <dt>账号</dt>
          <dd>
            <code>{credential.username}</code>
          </dd>
        </div>
      )}
      <div>
        <dt>{credential.label}</dt>
        <dd>
          <code className={shown ? "" : styles.hidden} aria-label={shown ? undefined : `${credential.label}已隐藏`}>
            {shown ? credential.secret : "•".repeat(14)}
          </code>
          <button
            className={styles.acIconBtn}
            aria-label={shown ? `隐藏${credential.label}` : `显示${credential.label}`}
            title={shown ? "隐藏" : "显示"}
            onClick={() => setShown(!shown)}
          >
            {shown ? <EyeOff /> : <Eye />}
          </button>
          <button
            className={`${styles.acIconBtn} ${copied ? styles.done : ""}`}
            aria-label={copied ? "已复制" : `复制${credential.label}`}
            title={copied ? "已复制" : "复制"}
            onClick={() => setCopied(copyText(credential.secret))}
          >
            {copied ? <Check /> : <Copy />}
          </button>
        </dd>
      </div>
    </dl>
  );
}

export function Progress() {
  return (
    <span className={styles.acProgress} aria-hidden="true">
      <i />
    </span>
  );
}

// A window-modal layer over the App Center that takes focus; Escape or a
// click beside it dismisses it. The panel makes the rest inert meanwhile.
export function Layer({
  label,
  onDismiss,
  children,
  size = "md",
}: {
  label: string;
  onDismiss?: () => void;
  children: ReactNode;
  size?: "sm" | "md";
}) {
  const sheet = useRef<HTMLDivElement>(null);
  useEffect(() => sheet.current?.focus(), []);
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "Escape" || !onDismiss) return;
    event.stopPropagation();
    onDismiss();
  };
  return (
    <div
      className={styles.acLayer}
      onKeyDown={onKeyDown}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onDismiss?.();
      }}
    >
      <div
        ref={sheet}
        className={`${styles.acSheet} ${styles[size] ?? ""}`}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
      >
        {children}
      </div>
    </div>
  );
}
