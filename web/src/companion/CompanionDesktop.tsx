import { Suspense, useEffect, useRef, useState } from "react";

import { lazyCode } from "../ui/lazyCode";
import { ConnectionStatus } from "./ConnectionStatus";
import styles from "./companion.module.css";
import { Stage } from "./Stage";
import type { TaskSignal } from "./TaskInbox";
import type { TaskPhase } from "./taskState";
import { useCompanionView } from "./useCompanionView";

// Authenticated NAS mode and offline playground share the renderer.
const TaskExperience = lazyCode(() => import("./TaskExperience").then((m) => m.TaskExperience));

export function CompanionDesktop({ userId, connected = false }: { userId: string; connected?: boolean }) {
  const view = useCompanionView(userId, connected, "desktop");
  const [phase, setPhase] = useState<TaskPhase>("idle");
  const [speech, setSpeech] = useState<TaskSignal>();
  const requested = useRef(false);
  useEffect(() => {
    if (connected && view.remote.connection === "online" && !requested.current) {
      requested.current = true;
      void view.remote.sync?.claim();
    }
  }, [connected, view.remote.connection, view.remote.sync]);
  useEffect(() => {
    document.documentElement.classList.add("companion-native");
    window.companionDesktop?.compact();
    const hover = (event: MouseEvent) => {
      const element = document.elementFromPoint(event.clientX, event.clientY);
      window.companionDesktop?.interactive(Boolean(element?.closest("[data-pet-interactive]")));
    };
    const leave = () => window.companionDesktop?.interactive(false);
    document.addEventListener("mousemove", hover);
    document.addEventListener("mouseleave", leave);
    return () => {
      document.documentElement.classList.remove("companion-native");
      document.removeEventListener("mousemove", hover);
      document.removeEventListener("mouseleave", leave);
    };
  }, []);
  return (
    <main className={styles.native}>
      <ConnectionStatus view={view} />
      {connected && (
        <Suspense fallback={null}>
          <TaskExperience
            userId={userId}
            view={view}
            compact
            onPhase={setPhase}
            onSpeech={setSpeech}
            onOpenConversation={(id) => window.companionDesktop?.conversation(id)}
          />
        </Suspense>
      )}
      <Stage
        taskPhase={phase}
        taskMessage={speech?.message}
        taskEvent={speech?.id}
        key={view.stageKey}
        character={view.character}
        quiet={view.quiet || !view.canPlay}
        interactive={view.canPlay}
        native
        floating
      />
      <div className={styles.miniActions} data-pet-interactive="true">
        <button type="button" onClick={() => window.companionDesktop?.home()}>
          {connected ? "回小窝" : "小窝"}
        </button>
        <button type="button" aria-pressed={view.quiet} disabled={!view.canPlay} onClick={view.toggleQuiet}>
          {view.quiet ? "醒来" : "安静"}
        </button>
        <button
          type="button"
          onClick={() => {
            view.rest();
            window.companionDesktop?.hide();
          }}
        >
          隐藏
        </button>
      </div>
    </main>
  );
}
