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
  // A fullscreen page (a video) moves the pet out of the way only while it lasts:
  // nothing is saved, and the pet is hidden rather than unmounted, so its chat, NAS
  // connection and position are all still there when the page leaves fullscreen.
  const [fullscreen, setFullscreen] = useState(() => Boolean(document.fullscreenElement));
  useEffect(() => {
    const change = () => setFullscreen(Boolean(document.fullscreenElement));
    document.addEventListener("fullscreenchange", change);
    return () => document.removeEventListener("fullscreenchange", change);
  }, []);
  if (value.place !== "desk") return null;
  return (
    <aside className={styles.floating} aria-label="网页桌宠" hidden={fullscreen}>
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
