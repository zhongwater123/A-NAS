import { COMPANION_CREDIT } from "../companion/credit";
import { type CompanionMode, setCompanionMode, useCompanionMode } from "../companion/quiet";
import styles from "./settings.module.css";

const modes: { mode: CompanionMode; label: string }[] = [
  { mode: "normal", label: "正常" },
  { mode: "quiet", label: "安静" },
  { mode: "hidden", label: "隐藏" },
];

// CompanionCredit names the desktop companion's makers and licence, which the licence asks for. It is the
// last line of 概览, which every member can open; the companion itself shows only its name. Above it, how
// the companion is on this device: as usual, quiet (asleep until the member clicks it, which VNAS cannot
// wake), or hidden.
export function CompanionCredit() {
  const current = useCompanionMode();
  return (
    <>
      <div className={styles.setRow}>
        <div className={styles.setRowText}>
          <span>这台设备上的伙伴</span>
          <small>安静时它睡着，点它才醒；隐藏后不再出现。只在这台设备上生效。</small>
        </div>
        <div className={styles.pzSegmented} role="radiogroup" aria-label="这台设备上的伙伴">
          {modes.map(({ mode, label }) => (
            <button
              key={mode}
              type="button"
              role="radio"
              aria-checked={current === mode}
              onClick={() => setCompanionMode(mode)}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      <p className={styles.setFootnote}>
        <strong>桌面伙伴</strong> {COMPANION_CREDIT}
      </p>
    </>
  );
}
