import {
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  CircleCheck,
  Ellipsis,
  type LucideIcon,
  RefreshCw,
  TriangleAlert,
  X,
} from "lucide-react";
import { Dialog, DropdownMenu, RadioGroup } from "radix-ui";
import { createContext, type ReactNode, type RefObject, use } from "react";

import { errorText } from "../api/errorMessages";
import { PersonAvatar } from "../people/PersonAvatar";
import type { Person } from "../people/people";
import type { Toast } from "../ui/feedback";
import styles from "./settings.module.css";

// Building blocks shared by the settings sections: a page title that stays in
// view, grouped lists whose rows are the controls, and dialogs, menus and
// toasts that stay inside the settings window.

export type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

// SettingsKit is what the window lends its sections: where dialogs and menus
// mount, a toast for short outcomes, and the way back to the section list in
// a narrow window.
export interface SettingsKit {
  container: RefObject<HTMLElement | null>;
  toast: (toast: Toast) => void;
  showSections: () => void;
}

// A section rendered on its own, as in its tests, mounts dialogs in the
// document and says nothing.
const standalone: SettingsKit = { container: { current: null }, toast: () => undefined, showSections: () => undefined };

export const SettingsKitContext = createContext<SettingsKit>(standalone);

export const useSettingsKit = () => use(SettingsKitContext);

// PageHeader is the section's title. It stays at the top while the page
// scrolls; in a narrow window it carries the way back to the section list.
export function PageHeader({ title, action }: { title: string; action?: ReactNode }) {
  const { showSections } = useSettingsKit();
  return (
    <header className={styles.setHeader}>
      <button className={styles.setBack} aria-label="返回设置列表" onClick={showSections}>
        <ChevronLeft />
      </button>
      <h2>{title}</h2>
      {action && <div className={styles.setHeaderActions}>{action}</div>}
    </header>
  );
}

export function Group({
  title,
  aside,
  anchor,
  children,
}: {
  title?: string;
  aside?: ReactNode;
  // anchor names the group for the settings search, which scrolls to it.
  anchor?: string;
  children: ReactNode;
}) {
  return (
    <section className={styles.setGroup} aria-label={title} data-setting={anchor}>
      {(title || aside) && (
        <div className={styles.setGroupHead}>
          {title && <h3>{title}</h3>}
          {aside && <span>{aside}</span>}
        </div>
      )}
      <div className={styles.setCard}>{children}</div>
    </section>
  );
}

// Row is one line of a group: a label with an optional detail under it, and
// at its end a value, a control, or both. With onClick the whole row is the
// button and ends in a chevron, so the target is as wide as the card.
export function Row({
  icon,
  label,
  detail,
  value,
  anchor,
  danger,
  onClick,
  children,
}: {
  icon?: ReactNode;
  label: ReactNode;
  detail?: ReactNode;
  value?: ReactNode;
  // anchor names the row for the settings search, which scrolls to it.
  anchor?: string;
  danger?: boolean;
  onClick?: () => void;
  children?: ReactNode;
}) {
  const body = (
    <>
      {icon}
      <span className={styles.setRowText}>
        <span>{label}</span>
        {detail && <small>{detail}</small>}
      </span>
      {value !== undefined && <span className={styles.setValue}>{value}</span>}
      {children && <span className={styles.setRowEnd}>{children}</span>}
    </>
  );
  if (onClick)
    return (
      <button
        type="button"
        className={`${styles.setRow} ${styles.setRowAction} ${danger ? styles.danger : ""}`}
        data-setting={anchor}
        onClick={onClick}
      >
        {body}
        <ChevronRight className={styles.setRowGo} aria-hidden="true" />
      </button>
    );
  return (
    <div className={styles.setRow} data-setting={anchor}>
      {body}
    </div>
  );
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className={styles.setField}>
      <label>
        <span>{label}</span>
        {children}
      </label>
      {hint && <small>{hint}</small>}
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

export function Avatar({ who, size }: { who: Person; size?: "sm" | "lg" | "xl" }) {
  return <PersonAvatar who={who} className={`${styles.setAvatar} ${styles[size ?? ""] ?? ""}`} />;
}

export function RefreshButton({ label, busy, onClick }: { label: string; busy: boolean; onClick: () => void }) {
  return (
    <button className={styles.setIconBtn} aria-label={label} title={label} disabled={busy} onClick={onClick}>
      <RefreshCw className={busy ? "spinning" : ""} />
    </button>
  );
}

// Modal is a dialog inside the settings window. Like the desktop's other
// in-window dialogs it leaves the rest of the desktop working; Tab stays in
// it, and Escape or a click outside closes it unless it is busy.
export function Modal({
  title,
  label,
  description,
  icon,
  busy,
  onClose,
  children,
}: {
  title: string;
  // label names the dialog where the title alone would not, such as for a member.
  label?: string;
  description?: ReactNode;
  icon?: ReactNode;
  busy?: boolean;
  onClose: () => void;
  children: ReactNode;
}) {
  const { container } = useSettingsKit();
  return (
    <Dialog.Root
      open
      modal={false}
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <Dialog.Portal container={container.current}>
        <div
          className={styles.setDialogLayer}
          onPointerDown={(event) => {
            if (event.target === event.currentTarget && !busy) onClose();
          }}
        >
          <Dialog.Content
            className={styles.setDialog}
            aria-label={label ?? title}
            aria-describedby={undefined}
            onInteractOutside={(event) => event.preventDefault()}
            onEscapeKeyDown={(event) => {
              if (busy) event.preventDefault();
            }}
          >
            <header className={styles.setDialogHead}>
              {icon}
              <Dialog.Title>{title}</Dialog.Title>
              {description && <Dialog.Description>{description}</Dialog.Description>}
            </header>
            {children}
          </Dialog.Content>
        </div>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export type MenuItem =
  | { label: string; icon: LucideIcon; onSelect: () => void; danger?: boolean; disabled?: boolean }
  | "separator";

// RowMenu is the "more actions" button at the end of a row.
export function RowMenu({ label, items }: { label: string; items: MenuItem[] }) {
  const { container } = useSettingsKit();
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger className={styles.setIconBtn} aria-label={label}>
        <Ellipsis />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal container={container.current}>
        <DropdownMenu.Content className={styles.setMenu} align="end" sideOffset={6} collisionPadding={10}>
          {items.map((item, index) =>
            item === "separator" ? (
              <DropdownMenu.Separator key={index} className={styles.setMenuLine} />
            ) : (
              <DropdownMenu.Item
                key={item.label}
                className={item.danger ? styles.danger : ""}
                disabled={item.disabled}
                onSelect={item.onSelect}
              >
                <item.icon />
                {item.label}
              </DropdownMenu.Item>
            ),
          )}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

// Segmented is a short set of exclusive choices shown side by side.
export function Segmented<Value extends string | number>({
  label,
  value,
  choices,
  busy,
  disabled,
  onChange,
}: {
  label: string;
  value: Value | undefined;
  choices: ReadonlyArray<{ value: Value; label: string }>;
  busy?: boolean;
  disabled?: boolean;
  onChange: (value: Value) => void;
}) {
  return (
    <RadioGroup.Root
      className={styles.setSegmented}
      aria-label={label}
      aria-busy={busy || undefined}
      orientation="horizontal"
      value={value === undefined ? "" : String(value)}
      disabled={disabled}
      onValueChange={(next) => {
        const choice = choices.find((item) => String(item.value) === next);
        if (choice) onChange(choice.value);
      }}
    >
      {choices.map((choice) => (
        <RadioGroup.Item key={choice.value} value={String(choice.value)}>
          {choice.label}
        </RadioGroup.Item>
      ))}
    </RadioGroup.Root>
  );
}

export function messageOf(error: unknown): string {
  return errorText(error);
}
