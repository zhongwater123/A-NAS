import { COMPANION_CREDIT } from "../companion/credit";
import styles from "./settings.module.css";

// CompanionCredit names the desktop companion's makers and licence, which the licence asks for. It is the
// last line of 概览, which every member can open; the companion itself shows only its name.
export function CompanionCredit() {
  return (
    <p className={styles.setFootnote}>
      <strong>桌面伙伴</strong> {COMPANION_CREDIT}
    </p>
  );
}
