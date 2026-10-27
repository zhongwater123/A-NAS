import { errorText } from "../errorMessages";
import { CircleAlert, CircleCheck, RefreshCw, TriangleAlert, X, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import type { Person } from "../people";
import { PersonAvatar } from "../PersonAvatar";

// Building blocks shared by the settings sections: grouped cards in the style
// of a system settings app.

export type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

export function PageHeader({ title, subtitle, action }: { title: string; subtitle: string; action?: ReactNode }) {
  return (
    <header className="set-header">
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
    <div className="set-group">
      <div className="set-group-head">
        <h3>{title}</h3>
        {aside && <span>{aside}</span>}
      </div>
      <div className={`set-card ${overflowVisible ? "overflow-visible" : ""}`}>{children}</div>
      {footnote && <p className="set-footnote">{footnote}</p>}
    </div>
  );
}

export function Pill({ tone = "neutral", outline, children }: { tone?: Tone; outline?: boolean; children: ReactNode }) {
  return <span className={`set-pill ${tone} ${outline ? "outline" : ""}`}>{children}</span>;
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
    <div className={`set-banner ${tone}`} role={tone === "error" ? "alert" : "status"}>
      <Icon />
      <span>{children}</span>
      {onClose && (
        <button className="set-icon-btn" aria-label="关闭提示" onClick={onClose}>
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
    <div className={`wiz-callout ${tone}`}>
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
    <span className={`set-tile ${tone} ${size ?? ""}`}>
      <Icon />
    </span>
  );
}

export function Avatar({ who, size }: { who: Person; size?: "lg" | "xl" }) {
  return <PersonAvatar who={who} className={`set-avatar ${size ?? ""}`} />;
}

export function RefreshButton({ label, busy, onClick }: { label: string; busy: boolean; onClick: () => void }) {
  return (
    <button className="set-icon-btn bordered" aria-label={label} disabled={busy} onClick={onClick}>
      <RefreshCw className={busy ? "spinning" : ""} />
    </button>
  );
}

export function messageOf(error: unknown): string {
  return errorText(error);
}
