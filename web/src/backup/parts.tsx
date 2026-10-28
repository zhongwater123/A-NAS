import { HardDrive, Minus, Plus, UsersRound } from "lucide-react";

import type { Space } from "../api.generated";
import workspaceStyles from "../ui/workspace.module.css";
import type { SnapshotOrigin } from "./api";
import styles from "./backup.module.css";
import { spaceLabel } from "./format";

export function SpaceIcon({ space }: { space: Pick<Space, "kind"> }) {
  return (
    <span className={`${styles.bkSpaceIcon} ${styles[space.kind] ?? ""}`} aria-hidden="true">
      {space.kind === "shared" ? <UsersRound /> : <HardDrive />}
    </span>
  );
}

export function OriginBadge({ origin }: { origin: SnapshotOrigin }) {
  return (
    <span className={`${styles.bkBadge} ${workspaceStyles.bkBadge} ${styles[origin] ?? ""}`}>
      {origin === "scheduled" ? "定时" : "手动"}
    </span>
  );
}

// SpaceSwitcher picks the space a page works on.
export function SpaceSwitcher({
  spaces,
  value,
  onChange,
}: {
  spaces: Space[];
  value: string;
  onChange: (id: string) => void;
}) {
  if (spaces.length < 2) return null;
  return (
    <div className={`${styles.bkSegmented} ${workspaceStyles.bkSegmented}`} role="radiogroup" aria-label="空间">
      {spaces.map((space) => (
        <button
          key={space.id}
          type="button"
          role="radio"
          aria-checked={space.id === value}
          className={space.id === value ? `${styles.active} ${workspaceStyles.active}` : ""}
          onClick={() => onChange(space.id)}
        >
          {space.kind === "shared" ? <UsersRound /> : <HardDrive />}
          {spaceLabel(space)}
        </button>
      ))}
    </div>
  );
}

export function Switch({
  checked,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  disabled?: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      className={`${styles.bkSwitch}${checked ? ` ${styles.on}` : ""}`}
      onClick={() => onChange(!checked)}
    >
      <span />
    </button>
  );
}

// Stepper edits a small whole number with buttons or by typing.
export function Stepper({
  value,
  min,
  max,
  label,
  disabled,
  onChange,
}: {
  value: number;
  min: number;
  max: number;
  label: string;
  disabled?: boolean;
  onChange: (value: number) => void;
}) {
  return (
    <div className={styles.bkStepper}>
      <button
        type="button"
        aria-label={`${label}减一`}
        disabled={disabled || value <= min}
        onClick={() => onChange(value - 1)}
      >
        <Minus />
      </button>
      <input
        type="number"
        min={min}
        max={max}
        value={value}
        disabled={disabled}
        aria-label={label}
        onChange={(event) => {
          const next = Math.round(Number(event.target.value));
          if (Number.isFinite(next)) onChange(Math.min(max, Math.max(min, next)));
        }}
      />
      <button
        type="button"
        aria-label={`${label}加一`}
        disabled={disabled || value >= max}
        onClick={() => onChange(value + 1)}
      >
        <Plus />
      </button>
    </div>
  );
}
