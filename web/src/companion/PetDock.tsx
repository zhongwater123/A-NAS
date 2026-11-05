import { Ellipsis, MessageCircle, X } from "lucide-react";
import { DropdownMenu, Popover } from "radix-ui";
import { Suspense, useRef } from "react";

import { lazyCode } from "../ui/lazyCode";
import { CompanionChat } from "./CompanionChat";
import type { AgentBridge, useConversation } from "./conversation";
import styles from "./conversation.module.css";
import { focusDue, type PersonalDeskState } from "./deskStore";
import type { TaskSignal } from "./TaskInbox";
import type { TaskPhase } from "./taskState";
import type { CompanionView } from "./useCompanionView";

const TaskExperience = lazyCode(() => import("./TaskExperience").then((m) => m.TaskExperience));

export function PetDock({
  owner,
  view,
  desk,
  conversation,
  open,
  onOpenChange,
  onMenuOpenChange,
  onHome,
  onHide,
  onPhase,
  onSpeech,
  onOpenConversation,
}: {
  owner: string;
  view: CompanionView;
  desk: PersonalDeskState;
  conversation: ReturnType<typeof useConversation>;
  open: boolean;
  onOpenChange(open: boolean): void;
  onMenuOpenChange?(open: boolean): void;
  onHome(): void;
  onHide(): void;
  onPhase(phase: TaskPhase): void;
  onSpeech(signal: TaskSignal | undefined): void;
  onOpenConversation?(id: string): void;
}) {
  const bridge = useRef<AgentBridge | undefined>(undefined);
  const focus = desk.value.focus;
  const due = focusDue(desk.value);
  const goHome = (tab: "chat" | "focus" | "todos" = "chat") => {
    conversation.chat.selectView(tab);
    onOpenChange(false);
    onHome();
  };
  return (
    <>
      <div className={styles.runtime} aria-hidden="true">
        {view.connected && (
          <Suspense fallback={null}>
            <TaskExperience
              userId={owner}
              view={view}
              compact
              bridge={bridge}
              onChatState={conversation.chat.observe}
              onPhase={onPhase}
              onSpeech={onSpeech}
              onOpenHome={onHome}
              onOpenConversation={onOpenConversation}
            />
          </Suspense>
        )}
      </div>
      <div className={styles.petDock} data-pet-interactive="true" aria-label="大肥鱼操作">
        <Popover.Root open={open} onOpenChange={onOpenChange}>
          <Popover.Trigger asChild>
            <button type="button" className={styles.talk} aria-label="和大肥鱼聊聊">
              <MessageCircle size={14} />
              聊聊{due ? " ·" : ""}
            </button>
          </Popover.Trigger>
          <Popover.Portal>
            <Popover.Content
              className={styles.popover}
              side="top"
              align="center"
              sideOffset={150}
              collisionPadding={10}
              data-pet-interactive="true"
              onOpenAutoFocus={(event) => event.preventDefault()}
            >
              <div className={styles.popoverHeader}>
                <strong>大肥鱼</strong>
                <Popover.Close asChild>
                  <button type="button" aria-label="收起聊天">
                    <X size={15} />
                  </button>
                </Popover.Close>
              </div>
              <CompanionChat
                owner={owner}
                desk={desk}
                conversation={conversation}
                bridge={bridge}
                connected={view.connected}
                compact
                onTool={goHome}
                onOpenConversation={onOpenConversation}
                focusInput={open}
              />
            </Popover.Content>
          </Popover.Portal>
        </Popover.Root>
        {(focus.status === "running" || focus.status === "paused" || due) && (
          <button
            type="button"
            className={`${styles.clockPill} ${due ? styles.due : ""}`}
            aria-label={
              due ? "专注到点了，打开提醒" : `查看${focus.mode === "break" ? "休息" : "专注"}计时 ${desk.clock}`
            }
            onClick={() => (due ? onOpenChange(true) : goHome("focus"))}
          >
            {due ? "到点啦" : `${focus.status === "paused" ? "已暂停 " : ""}${desk.clock}`}
          </button>
        )}
        {view.connected && !view.local && !view.remote.here && view.remote.connection === "online" && (
          <button type="button" disabled={view.remote.waiting} onClick={view.claim}>
            {view.remote.waiting ? "过来中" : "叫到这里"}
          </button>
        )}
        <DropdownMenu.Root onOpenChange={onMenuOpenChange}>
          <DropdownMenu.Trigger asChild>
            <button type="button" className={styles.more} aria-label="大肥鱼更多操作">
              <Ellipsis size={16} />
            </button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content
              className={styles.menu}
              side="top"
              align="end"
              sideOffset={8}
              collisionPadding={10}
              data-pet-interactive="true"
            >
              <DropdownMenu.Label className={styles.menuLabel}>
                {view.local ? "本机陪伴" : view.remote.here ? "正在这里陪你" : view.remote.message}
              </DropdownMenu.Label>
              <DropdownMenu.Item className={styles.menuItem} onSelect={() => goHome()}>
                回小窝
              </DropdownMenu.Item>
              <DropdownMenu.Item className={styles.menuItem} onSelect={() => goHome("todos")}>
                查看待办
              </DropdownMenu.Item>
              <DropdownMenu.Item className={styles.menuItem} onSelect={() => goHome("focus")}>
                专注计时
              </DropdownMenu.Item>
              <DropdownMenu.Separator className={styles.menuSeparator} />
              <DropdownMenu.Item className={styles.menuItem} disabled={!view.canPlay} onSelect={view.toggleQuiet}>
                {view.quiet ? "醒来" : "安静陪我"}
              </DropdownMenu.Item>
              {view.local && view.connected && view.remote.connection === "online" && (
                <DropdownMenu.Item className={styles.menuItem} onSelect={view.claim}>
                  连接 NAS
                </DropdownMenu.Item>
              )}
              {!view.local && view.remote.connection !== "online" && (
                <DropdownMenu.Item className={styles.menuItem} onSelect={view.playLocal}>
                  先在本机陪伴
                </DropdownMenu.Item>
              )}
              <DropdownMenu.Item
                className={styles.menuItem}
                onSelect={() => {
                  onOpenChange(false);
                  onHide();
                }}
              >
                收起大肥鱼
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
      </div>
    </>
  );
}

export function companionWords(desk: PersonalDeskState, conversation: ReturnType<typeof useConversation>) {
  if (focusDue(desk.value)) return desk.message;
  if (conversation.state.pending) return "NAS 正在处理。你可以继续和我说计时或待办。";
  const last = conversation.state.lines.findLast((line) => line.role === "assistant");
  if (last) return last.text.length > 85 ? `${last.text.slice(0, 82)}…点我看看。` : last.text;
  return desk.message ?? "想专注一会儿，还是记点什么？点我聊聊。";
}
