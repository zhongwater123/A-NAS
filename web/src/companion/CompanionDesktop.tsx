import { useEffect, useRef, useState } from "react";

import styles from "./companion.module.css";
import { useConversation } from "./conversation";
import { usePersonalDesk } from "./deskStore";
import type { Clip } from "./native";
import { companionWords, PetDock } from "./PetDock";
import { Stage } from "./Stage";
import type { TaskSignal } from "./TaskInbox";
import type { TaskPhase } from "./taskState";
import { useCompanionView } from "./useCompanionView";

// Authenticated NAS mode and offline playground share the renderer.

export function CompanionDesktop({ userId, connected = false }: { userId: string; connected?: boolean }) {
  const view = useCompanionView(userId, connected, "desktop");
  const [phase, setPhase] = useState<TaskPhase>("idle");
  const desk = usePersonalDesk(userId);
  const conversation = useConversation(userId, desk);
  const [chatOpen, setChatOpen] = useState(false);
  const [speech, setSpeech] = useState<TaskSignal>();
  const [menuOpen, setMenuOpen] = useState(false);
  const [clip, setClip] = useState<Clip>();
  const main = useRef<HTMLElement>(null);
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
  // The shell keeps the character and its buttons on screen and lets the transparent rest of
  // the window run off it. Chats and menus open in portals and bring the whole window back.
  useEffect(() => {
    const desktop = window.companionDesktop;
    const root = main.current;
    if (!desktop?.body || !root) return;
    let frame = 0;
    let last = "";
    const resize = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(() => report());
    const report = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const elements = [...root.querySelectorAll("[data-pet-interactive]")].filter(
          (element) => !element.closest("[data-radix-popper-content-wrapper]"),
        );
        // Cards and status lines come and go; watch the ones on screen now.
        resize?.disconnect();
        for (const element of [...root.children, ...elements]) resize?.observe(element);
        const parts = elements
          .map((element) => element.getBoundingClientRect())
          .filter((rect) => rect.width > 0 && rect.height > 0);
        if (!parts.length) return;
        const left = Math.max(0, Math.min(...parts.map((rect) => rect.left)));
        const top = Math.max(0, Math.min(...parts.map((rect) => rect.top)));
        const right = Math.min(window.innerWidth, Math.max(...parts.map((rect) => rect.right)));
        const bottom = Math.min(window.innerHeight, Math.max(...parts.map((rect) => rect.bottom)));
        const rect = { x: left, y: top, width: right - left, height: bottom - top };
        const key = JSON.stringify(rect);
        if (key === last) return;
        last = key;
        desktop.body?.(rect);
      });
    };
    const changes = new MutationObserver(report);
    changes.observe(root, { childList: true, subtree: true });
    window.addEventListener("resize", report);
    report();
    const stopClip = desktop.onClip?.(setClip);
    return () => {
      cancelAnimationFrame(frame);
      resize?.disconnect();
      changes.disconnect();
      window.removeEventListener("resize", report);
      stopClip?.();
    };
  }, []);
  useEffect(() => {
    window.companionDesktop?.reveal?.(chatOpen || menuOpen);
  }, [chatOpen, menuOpen]);
  return (
    <main ref={main} className={styles.native}>
      <Stage
        taskPhase={phase}
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
        quiet={view.quiet || !view.canPlay}
        interactive={view.canPlay}
        native
        floating
        clip={clip}
      />
      <PetDock
        owner={userId}
        view={view}
        desk={desk}
        conversation={conversation}
        open={chatOpen}
        onOpenChange={setChatOpen}
        onMenuOpenChange={setMenuOpen}
        onHome={() => window.companionDesktop?.home()}
        onHide={() => {
          view.rest();
          window.companionDesktop?.hide();
        }}
        onPhase={setPhase}
        onSpeech={setSpeech}
        onOpenConversation={(id) => window.companionDesktop?.conversation(id)}
      />
    </main>
  );
}
