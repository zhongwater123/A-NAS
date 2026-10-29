import { Ellipsis, Play, X } from "lucide-react";
import { Dialog, DropdownMenu } from "radix-ui";
import { type ReactNode, useState } from "react";

import { PersonAvatar } from "../people/PersonAvatar";
import { person, usePeople } from "../people/people";
import { thumbnailURL } from "../photos/api";
import type { FamilyRecord } from "./api";
import { memberOf, useFamily } from "./context";
import styles from "./family.module.css";

// Modal is a dialog inside the family window. It stays in the window, as the
// desktop's other in-window dialogs do, so the rest of the desktop keeps
// working; Escape and the close button end it unless it is busy.
export function Modal({
  title,
  icon,
  busy,
  wide,
  onClose,
  children,
  footer,
}: {
  title: string;
  icon?: ReactNode;
  busy?: boolean;
  wide?: boolean;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const { container } = useFamily();
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
          className={styles.fcBackdrop}
          aria-hidden="true"
          onPointerDown={() => {
            if (!busy) onClose();
          }}
        />
        <Dialog.Content
          className={`${styles.fcModal} ${wide ? styles.wide : ""}`}
          aria-describedby={undefined}
          onInteractOutside={(event) => event.preventDefault()}
        >
          <header className={styles.fcModalHead}>
            {icon && (
              <span className={styles.fcModalIcon} aria-hidden="true">
                {icon}
              </span>
            )}
            <Dialog.Title>{title}</Dialog.Title>
            <Dialog.Close className={styles.fcIconButton} aria-label="关闭" disabled={busy}>
              <X size={17} />
            </Dialog.Close>
          </header>
          <div className={styles.fcModalBody}>{children}</div>
          {footer && <footer className={styles.fcModalFoot}>{footer}</footer>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

// PhotoGrid shows a record's photos the way a chat shows them: one large,
// a few side by side, many as a grid ending in +N. A photo opens in 相册.
export function PhotoGrid({ photos, limit = 9 }: { photos: FamilyRecord["photos"]; limit?: number }) {
  const { onShowPhoto } = useFamily();
  if (photos.length === 0) return null;
  const shown = photos.slice(0, limit);
  const more = photos.length - shown.length;
  const layout =
    photos.length === 1 ? styles.one : photos.length === 2 || photos.length === 4 ? styles.two : styles.three;
  return (
    <div className={`${styles.fcPhotoGrid} ${layout}`}>
      {shown.map((photo, index) => (
        <button
          key={photo.assetId}
          type="button"
          className={styles.fcPhoto}
          onClick={() => onShowPhoto(photo.assetId)}
          aria-label={`在相册中打开第 ${index + 1} 张${photo.video ? "视频" : "照片"}`}
        >
          <Thumb assetId={photo.assetId} />
          {photo.video && (
            <span className={styles.fcVideoMark} aria-hidden="true">
              <Play size={14} fill="currentColor" />
            </span>
          )}
          {more > 0 && index === shown.length - 1 && <span className={styles.fcMore}>+{more}</span>}
        </button>
      ))}
    </div>
  );
}

// Thumb shows a photo's thumbnail, or a soft placeholder while the photo
// service is still making it.
export function Thumb({ assetId }: { assetId: string }) {
  const [failed, setFailed] = useState(false);
  if (failed) return <span className={styles.fcThumbPending} aria-hidden="true" />;
  return (
    <img
      src={thumbnailURL(assetId)}
      alt=""
      loading="lazy"
      decoding="async"
      draggable={false}
      onError={() => setFailed(true)}
    />
  );
}

// MemberFace shows a family member's avatar from the account directory.
export function MemberFace({
  accountId,
  username,
  className,
}: {
  accountId: string;
  username: string;
  className: string;
}) {
  const people = usePeople();
  return <PersonAvatar who={person(people, { id: accountId, username })} className={className} />;
}

// MemberName is the member's nickname and family role, as the family calls them.
export function useMemberName() {
  const family = useFamily();
  const people = usePeople();
  return (accountId: string, username: string) => {
    const profile = people.byId(accountId) ?? people.byUsername(username);
    const name = profile?.nickname || profile?.username || username;
    const role = memberOf(family, accountId)?.role;
    return { name, role };
  };
}

export interface MenuItem {
  label: string;
  danger?: boolean;
  run: () => void;
}

// ItemMenu is the … menu of a record, a comment or a member.
export function ItemMenu({ label, items }: { label: string; items: MenuItem[] }) {
  if (items.length === 0) return null;
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger className={styles.fcIconButton} aria-label={label}>
        <Ellipsis size={17} />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content className={styles.fcMenu} align="end" sideOffset={4}>
          {items.map((item) => (
            <DropdownMenu.Item
              key={item.label}
              className={`${styles.fcMenuItem} ${item.danger ? styles.danger : ""}`}
              onSelect={item.run}
            >
              {item.label}
            </DropdownMenu.Item>
          ))}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

// Field lays out a labelled form control.
export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className={styles.fcField}>
      <span className={styles.fcFieldLabel}>{label}</span>
      {children}
      {hint && <span className={styles.fcFieldHint}>{hint}</span>}
    </label>
  );
}
