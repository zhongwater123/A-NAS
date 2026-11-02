import styles from "./companion.module.css";
import type { CompanionView } from "./useCompanionView";

export function ConnectionStatus({ view }: { view: CompanionView }) {
  if (!view.connected) return null;
  const { remote } = view;
  return (
    <div className={styles.syncStatus} data-pet-interactive="true">
      <p aria-live="polite">{view.local ? "本地逗玩，不写入 NAS 进度" : remote.message}</p>
      {(!remote.here || view.local) && (
        <button
          type="button"
          disabled={remote.waiting || remote.connection !== "online"}
          onClick={() => {
            view.update({ place: "home" });
            view.claim();
          }}
        >
          {remote.waiting ? "正在过来…" : "叫到这里"}
        </button>
      )}
      {(remote.connection === "offline" || remote.connection === "unavailable") && !view.local && (
        <button type="button" onClick={view.playLocal}>
          先在本地玩
        </button>
      )}
    </div>
  );
}
