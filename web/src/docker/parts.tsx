import { Box, ExternalLink } from "lucide-react";
import { KeyboardEvent, ReactNode, useEffect, useRef, useState } from "react";

import { iconURL } from "../appcenter/api";
import { ContainerSummary } from "./api";
import { AppRef, Condition, WebPort } from "./model";

// An App Center app's own icon, or a tile in the container's state colour.
export function ContainerGlyph({
  app,
  condition,
  size = "md",
}: {
  app?: AppRef;
  condition?: Condition;
  size?: "sm" | "md" | "lg";
}) {
  const [failed, setFailed] = useState(false);
  if (app && !failed) {
    return (
      <span className={`dk-glyph app ${size}`} aria-hidden="true">
        <img src={iconURL(app.id)} alt="" draggable={false} onError={() => setFailed(true)} />
      </span>
    );
  }
  return (
    <span className={`dk-glyph ${condition?.tone ?? "ok"} ${size}`} aria-hidden="true">
      <Box />
    </span>
  );
}

export function StatePill({ condition }: { condition: Condition }) {
  return (
    <span className={`dk-pill ${condition.tone}`}>
      <i />
      {condition.label}
    </span>
  );
}

export function PortLinks({ ports, item }: { ports: WebPort[]; item: ContainerSummary }) {
  if (ports.length === 0) return null;
  return (
    <span className="dk-ports">
      {ports.map((port) => (
        <a
          key={port.hostPort}
          className="dk-port"
          href={port.url}
          target="_blank"
          rel="noopener noreferrer"
          title={`在新窗口打开 ${port.url}`}
          aria-label={`打开 ${item.name} 的端口 ${port.hostPort}`}
          onClick={(event) => event.stopPropagation()}
        >
          :{port.hostPort}
          <ExternalLink />
        </a>
      ))}
    </span>
  );
}

export function IconAction({
  label,
  icon,
  busy,
  disabled,
  tone,
  onClick,
}: {
  label: string;
  icon: ReactNode;
  busy?: boolean;
  disabled?: boolean;
  tone?: "danger";
  onClick: () => void;
}) {
  return (
    <button
      className={`dk-icon-btn ${tone ?? ""}`}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
    >
      {busy ? <span className="loader small" /> : icon}
    </button>
  );
}

export function Empty({
  icon,
  title,
  tone,
  children,
}: {
  icon: ReactNode;
  title: string;
  tone?: "error" | "ok";
  children?: ReactNode;
}) {
  return (
    <div className="dk-empty">
      <span className={`dk-empty-art ${tone ?? ""}`}>{icon}</span>
      <h3>{title}</h3>
      {children}
    </div>
  );
}

// A window-modal layer that takes focus; Escape or a click beside it dismisses it.
export function Layer({ label, onDismiss, children }: { label: string; onDismiss: () => void; children: ReactNode }) {
  const sheet = useRef<HTMLDivElement>(null);
  useEffect(() => sheet.current?.focus(), []);
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "Escape") return;
    event.stopPropagation();
    onDismiss();
  };
  return (
    <div
      className="dk-layer"
      onKeyDown={onKeyDown}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onDismiss();
      }}
    >
      <div ref={sheet} className="dk-sheet" role="dialog" aria-modal="true" aria-label={label} tabIndex={-1}>
        {children}
      </div>
    </div>
  );
}
