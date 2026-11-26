import { RotateCw, WifiOff } from "lucide-react";

import { errorText } from "../../api/errorMessages";
import styles from "../screens/videocut.module.css";

// Failed is what a page shows when it could not load: why, and a way to ask again.
export function Failed({ error, onRetry, inline }: { error: Error; onRetry: () => void; inline?: boolean }) {
  return (
    <div className={inline ? styles.mtFailedInline : styles.mtCentered} role="alert">
      {!inline && (
        <span className={styles.mtFailedIcon} aria-hidden="true">
          <WifiOff />
        </span>
      )}
      <span>{errorText(error, "videocut")}</span>
      <button type="button" className={`${styles.mtBtn} ${styles.small}`} onClick={onRetry}>
        <RotateCw aria-hidden="true" />
        重试
      </button>
    </div>
  );
}
