import { useEffect, useState } from "react";

import styles from "./companion.module.css";
import { useConversation } from "./conversation";
import { usePersonalDesk } from "./deskStore";
import { companionWords, PetDock } from "./PetDock";
import { Stage } from "./Stage";
import { useCompanion } from "./store";
import type { TaskSignal } from "./TaskInbox";
import type { TaskPhase } from "./taskState";
import { useCompanionView } from "./useCompanionView";

export function CompanionFloating({
  userId,
  onOpen,
  connected = false,
  onOpenConversation,
  previewPhase,
}: {
  userId: string;
  onOpen: () => void;
  connected?: boolean;
  onOpenConversation?(id: string): void;
  previewPhase?: TaskPhase;
}) {
  const [phase, setPhase] = useState<TaskPhase>("idle");
  const desk = usePersonalDesk(userId);
  const conversation = useConversation(userId, desk);
  const [chatOpen, setChatOpen] = useState(false);
  const [speech, setSpeech] = useState<TaskSignal>();
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
      <Stage
        taskPhase={connected ? phase : previewPhase}
        taskMessage={speech?.message}
        taskEvent={speech?.id}
        companionMessage={companionWords(desk, conversation)}
        onInteract={() => setChatOpen(true)}
        hideSpeech={chatOpen}
        companionAction={
          desk.value.focus.status === "done"
            ? "happy"
            : desk.value.focus.status === "running" && desk.value.focus.mode === "focus"
              ? "thinking"
              : undefined
        }
        key={view.stageKey}
        character={view.character}
        quiet={quiet || !view.canPlay}
        interactive={view.canPlay}
        floating
      />
      <PetDock
        owner={userId}
        view={view}
        desk={desk}
        conversation={conversation}
        open={chatOpen}
        onOpenChange={setChatOpen}
        onHome={() => {
          update({ place: "home" });
          onOpen();
        }}
        onHide={() => {
          view.rest();
          update({ place: "hidden" });
        }}
        onPhase={setPhase}
        onSpeech={setSpeech}
        onOpenConversation={onOpenConversation}
      />
    </aside>
  );
}
