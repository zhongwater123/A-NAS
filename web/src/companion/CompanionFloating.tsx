import { Suspense, useEffect, useState } from "react";

import { lazyCode } from "../ui/lazyCode";
import { ConnectionStatus } from "./ConnectionStatus";
import styles from "./companion.module.css";
import { Stage } from "./Stage";
import { useCompanion } from "./store";
import type { TaskPhase } from "./taskState";
import { useCompanionView } from "./useCompanionView";

const TaskExperience = lazyCode(() => import("./TaskExperience").then((m) => m.TaskExperience));

export function CompanionFloating({
  userId,
  onOpen,
  connected = false,
  onOpenConversation,
}: {
  userId: string;
  onOpen: () => void;
  connected?: boolean;
  onOpenConversation?(id: string): void;
}) {
  const [phase, setPhase] = useState<TaskPhase>("idle");
  const [local] = useCompanion(userId);
  const view = useCompanionView(userId, connected && local.place === "desk");
  const { value, update, quiet } = view;
  useEffect(() => {
    const hide = () => {
      if (document.fullscreenElement) {
        view.rest();
        update({ place: "hidden" });
      }
    };
    document.addEventListener("fullscreenchange", hide);
    return () => document.removeEventListener("fullscreenchange", hide);
  }, [update, view]);
  if (value.place !== "desk") return null;
  return (
    <aside className={styles.floating} aria-label="网页桌宠">
      <ConnectionStatus view={view} />
      {connected && (
        <Suspense fallback={null}>
          <TaskExperience
            userId={userId}
            view={view}
            compact
            onOpenHome={onOpen}
            onPhase={setPhase}
            onOpenConversation={onOpenConversation}
          />
        </Suspense>
      )}
      <Stage
        taskPhase={phase}
        key={view.stageKey}
        character={view.character}
        quiet={quiet || !view.canPlay}
        interactive={view.canPlay}
        floating
        playCount={view.playCount}
        onPlay={view.play}
      />
      <div className={styles.miniActions}>
        <button
          type="button"
          onClick={() => {
            update({ place: "home" });
            onOpen();
          }}
        >
          回小窝
        </button>
        <button type="button" aria-pressed={quiet} disabled={!view.canPlay} onClick={view.toggleQuiet}>
          {quiet ? "醒来" : "安静"}
        </button>
        <button
          type="button"
          onClick={() => {
            view.rest();
            update({ place: "hidden" });
          }}
        >
          休息
        </button>
      </div>
    </aside>
  );
}
