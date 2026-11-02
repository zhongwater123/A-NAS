import { Suspense, useEffect, useRef, useState } from "react";

import { lazyCode } from "../ui/lazyCode";
import { Character } from "./Character";
import { ConnectionStatus } from "./ConnectionStatus";
import styles from "./companion.module.css";
import { Stage } from "./Stage";
import type { TaskPhase } from "./taskState";
import { useCompanionView } from "./useCompanionView";

const TaskExperience = lazyCode(() => import("./TaskExperience").then((m) => m.TaskExperience));

export function CompanionPanel({
  userId,
  connected = false,
  onOpenConversation,
  previewPhase,
}: {
  userId: string;
  connected?: boolean;
  onOpenConversation?(id: string): void;
  previewPhase?: TaskPhase;
}) {
  const view = useCompanionView(userId, connected);
  const { value, update, character, quiet, canPlay, remote } = view;
  const [phase, setPhase] = useState<TaskPhase>("idle");
  const summoned = useRef(false);
  useEffect(() => {
    if (
      connected &&
      remote.connection === "online" &&
      !summoned.current &&
      new URLSearchParams(window.location.search).get("summon") === "1"
    ) {
      summoned.current = true;
      void remote.sync?.claim();
    }
  }, [connected, remote.connection, remote.sync]);
  return (
    <section className={styles.panel} aria-label="伙伴小窝">
      <header className={styles.header}>
        <div>
          <span className={styles.eyebrow}>一点自己的小日常</span>
          <h1>伙伴的小窝</h1>
        </div>
        <span className={styles.badge}>{connected ? "NAS 接续" : "玩法试验"}</span>
      </header>
      <p className={styles.intro}>忙你的，也可以过来逗我一下。</p>
      <ConnectionStatus view={view} />
      <div className={styles.scene}>
        {value.place === "home" ? (
          <Stage
            taskPhase={connected ? phase : previewPhase}
            key={view.stageKey}
            character={character}
            quiet={quiet || !canPlay}
            interactive={canPlay}
            playCount={view.playCount}
            onPlay={view.play}
          />
        ) : (
          <div className={styles.empty}>
            <Character kind={character} asleep />
            <p>{value.place === "desk" ? "伙伴在这个网页的桌面角落" : "伙伴在休息，随时可以叫回来"}</p>
            <button type="button" onClick={() => update({ place: "home" })}>
              叫回小窝
            </button>
          </div>
        )}
      </div>
      <div className={styles.actions}>
        <button
          type="button"
          className={styles.primary}
          disabled={!canPlay}
          onClick={() => update({ place: value.place === "desk" ? "home" : "desk" })}
        >
          {value.place === "desk" ? "回小窝" : "到网页桌面陪我"}
        </button>
        <button type="button" aria-pressed={quiet} disabled={!canPlay} onClick={view.toggleQuiet}>
          {quiet ? "醒来玩一会儿" : "安静陪我"}
        </button>
        <button
          type="button"
          onClick={() => {
            view.rest();
            update({ place: "hidden" });
          }}
        >
          先休息
        </button>
      </div>
      {connected && (
        <Suspense fallback={<p>正在准备任务…</p>}>
          <TaskExperience userId={userId} view={view} onPhase={setPhase} onOpenConversation={onOpenConversation} />
        </Suspense>
      )}
      <p className={styles.note}>
        {connected
          ? "形象、安静偏好和一起玩的次数会跟着你的账号，屏幕位置各自记住。"
          : "奶蛙素材用于当前原型。当前为本机离线试玩。"}
        触屏交互待硬件支持后再做。
      </p>
    </section>
  );
}
