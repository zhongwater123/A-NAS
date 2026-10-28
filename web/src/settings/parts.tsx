import { CircleAlert, CircleCheck, type LucideIcon, RefreshCw, TriangleAlert, X } from "lucide-react";
import type { ReactNode } from "react";

import { errorText } from "../api/errorMessages";
import { PersonAvatar } from "../people/PersonAvatar";
import type { Person } from "../people/people";
import styles from "./settings.module.css";

// Building blocks shared by the settings sections: grouped cards in the style
// of a system settings app.

export type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

export function PageHeader({ title, subtitle, action }: { title: string; subtitle: string; action?: ReactNode }) {
  return (
    <header className={styles.setHeader}>
      <div>
        <h2>{title}</h2>
        <p>{subtitle}</p>
      </div>
      {action}
    </header>
  );
}

export function Group({
  title,
  aside,
  footnote,
  overflowVisible,
  children,
}: {
  title: string;
  aside?: string;
  footnote?: string;
  overflowVisible?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={styles.setGroup}>
      <div className={styles.setGroupHead}>
        <h3>{title}</h3>
        {aside && <span>{aside}</span>}
      </div>
      <div className={`${styles.setCard} ${overflowVisible ? styles.overflowVisible : ""}`}>{children}</div>
      {footnote && <p className={styles.setFootnote}>{footnote}</p>}
    </div>
  );
}

export function Pill({ tone = "neutral", outline, children }: { tone?: Tone; outline?: boolean; children: ReactNode }) {
  return <span className={`${styles.setPill} ${styles[tone] ?? ""} ${outline ? styles.outline : ""}`}>{children}</span>;
}

export function Banner({
  tone,
  onClose,
  children,
}: {
  tone: "error" | "warning" | "success";
  onClose?: () => void;
  children: ReactNode;
}) {
  const Icon = tone === "success" ? CircleCheck : CircleAlert;
  return (
    <div className={`${styles.setBanner} ${styles[tone] ?? ""}`} role={tone === "error" ? "alert" : "status"}>
      <Icon />
      <span>{children}</span>
      {onClose && (
        <button className={styles.setIconBtn} aria-label="关闭提示" onClick={onClose}>
          <X />
        </button>
      )}
    </div>
  );
}

export function Callout({
  tone,
  title,
  children,
}: {
  tone: "danger" | "success" | "warning";
  title: string;
  children: ReactNode;
}) {
  const Icon = tone === "success" ? CircleCheck : TriangleAlert;
  return (
    <div className={`${styles.wizCallout} ${styles[tone] ?? ""}`}>
      <Icon />
      <div>
        <strong>{title}</strong>
        <p>{children}</p>
      </div>
    </div>
  );
}

export function IconTile({ tone, icon: Icon, size }: { tone: string; icon: LucideIcon; size?: "lg" }) {
  return (
    <span className={`${styles.setTile} ${styles[tone] ?? ""} ${styles[size ?? ""] ?? ""}`}>
      <Icon />
    </span>
  );
}

export function Avatar({ who, size }: { who: Person; size?: "lg" | "xl" }) {
  return <PersonAvatar who={who} className={`${styles.setAvatar} ${styles[size ?? ""] ?? ""}`} />;
}

export function RefreshButton({ label, busy, onClick }: { label: string; busy: boolean; onClick: () => void }) {
  return (
    <button className={`${styles.setIconBtn} ${styles.bordered}`} aria-label={label} disabled={busy} onClick={onClick}>
      <RefreshCw className={busy ? "spinning" : ""} />
    </button>
  );
}

export function messageOf(error: unknown): string {
  return errorText(error);
}
