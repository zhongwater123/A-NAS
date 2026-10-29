import { Box, ExternalLink } from "lucide-react";
import { KeyboardEvent, ReactNode, useEffect, useRef, useState } from "react";

import { iconURL } from "../appcenter/api";
import { ContainerSummary } from "./api";
import styles from "./docker.module.css";
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
      <span className={`${styles.dkGlyph} ${styles.app} ${styles[size] ?? ""}`} aria-hidden="true">
        <img src={iconURL(app.id)} alt="" draggable={false} onError={() => setFailed(true)} />
      </span>
    );
  }
  return (
    <span
      className={`${styles.dkGlyph} ${styles[condition?.tone ?? "ok"] ?? ""} ${styles[size] ?? ""}`}
      aria-hidden="true"
    >
      <Box />
    </span>
  );
}

export function StatePill({ condition }: { condition: Condition }) {
  return (
    <span className={`${styles.dkPill} ${styles[condition.tone] ?? ""}`}>
      <i />
      {condition.label}
    </span>
  );
}

export function PortLinks({ ports, item }: { ports: WebPort[]; item: ContainerSummary }) {
  if (ports.length === 0) return null;
  return (
    <span className={styles.dkPorts}>
      {ports.map((port) => (
        <a
          key={port.hostPort}
          className={styles.dkPort}
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
      className={`${styles.dkIconBtn} ${styles[tone ?? ""] ?? ""}`}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
    >
      {busy ? <span className={`loader ${styles.small}`} /> : icon}
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
    <div className={styles.dkEmpty}>
      <span className={`${styles.dkEmptyArt} ${styles[tone ?? ""] ?? ""}`}>{icon}</span>
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
      className={styles.dkLayer}
      onKeyDown={onKeyDown}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onDismiss();
      }}
    >
      <div ref={sheet} className={styles.dkSheet} role="dialog" aria-modal="true" aria-label={label} tabIndex={-1}>
        {children}
      </div>
    </div>
  );
}
