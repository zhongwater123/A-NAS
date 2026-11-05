import { Tabs } from "radix-ui";
import { Suspense, useEffect, useRef } from "react";

import { lazyCode } from "../ui/lazyCode";
import { Character } from "./Character";
import { CompanionChat } from "./CompanionChat";
import styles from "./companion.module.css";
import { type AgentBridge, useConversation } from "./conversation";
import chatStyles from "./conversation.module.css";
import { FocusDesk, TodoDesk } from "./DeskTools";
import { focusDue, usePersonalDesk } from "./deskStore";
import deskStyles from "./personalDesk.module.css";
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
  const { value, update, character, quiet, remote } = view;
  const desk = usePersonalDesk(userId);
  const conversation = useConversation(userId, desk);
  const bridge = useRef<AgentBridge | undefined>(undefined);
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
  const due = focusDue(desk.value);
  return (
    <section className={styles.panel} aria-label="伙伴小窝">
      <div className={`${deskStyles.workspace} ${chatStyles.home}`}>
        <header className={chatStyles.homeHeader}>
          <button
            type="button"
            className={chatStyles.avatar}
            aria-label="和大肥鱼聊聊"
            onClick={() => conversation.chat.selectView("chat")}
          >
            <Character
              kind={character}
              asleep={quiet}
              phase={previewPhase}
              action={due ? "happy" : conversation.state.pending ? "thinking" : undefined}
            />
          </button>
          <div className={chatStyles.title}>
            <h1>伙伴的小窝</h1>
            <p>小事跟我说，我帮你记着。</p>
          </div>
          {value.place !== "home" ? (
            <button type="button" onClick={() => update({ place: "home" })}>
              叫回小窝
            </button>
          ) : (
            <button type="button" onClick={() => update({ place: "desk" })}>
              到网页桌面陪我
            </button>
          )}
        </header>
        <div className={chatStyles.headerStatus}>
          <i />
          {view.local ? "本机陪伴 · 计时与待办可直接使用" : remote.here ? "正在这里陪你 · NAS 已连接" : remote.message}
          {!view.local && !remote.here && remote.connection === "online" && (
            <button type="button" disabled={remote.waiting} onClick={view.claim}>
              {remote.waiting ? "过来中…" : "叫到这里"}
            </button>
          )}
          {view.local && connected && remote.connection === "online" && (
            <button type="button" disabled={remote.waiting} onClick={view.claim}>
              连接 NAS
            </button>
          )}
          {!view.local && remote.connection !== "online" && (
            <button type="button" onClick={view.playLocal}>
              先在本机陪伴
            </button>
          )}
          <button type="button" disabled={!view.canPlay} onClick={view.toggleQuiet}>
            {quiet ? "醒来" : "安静陪我"}
          </button>
        </div>
        <Tabs.Root
          value={conversation.state.view}
          onValueChange={(tab) => conversation.chat.selectView(tab as "chat" | "focus" | "todos")}
        >
          <Tabs.List className={deskStyles.tabs} aria-label="小窝工具">
            <Tabs.Trigger value="chat">聊天{due ? " · 到点啦" : ""}</Tabs.Trigger>
            <Tabs.Trigger value="focus">
              专注计时
              {desk.value.focus.status === "running" || desk.value.focus.status === "paused" ? ` · ${desk.clock}` : ""}
            </Tabs.Trigger>
            <Tabs.Trigger value="todos">
              简单待办
              {desk.value.todos.some((t) => !t.done) ? ` · ${desk.value.todos.filter((t) => !t.done).length}` : ""}
            </Tabs.Trigger>
          </Tabs.List>
          {due && conversation.state.view !== "chat" && (
            <div className={chatStyles.attention} role="status">
              <span>{desk.value.focus.mode === "break" ? "休息到点了" : "这一轮专注完成了"}</span>
              <button type="button" onClick={() => conversation.chat.selectView("chat")}>
                看看提醒
              </button>
              <button type="button" onClick={desk.store.acknowledgeFocus}>
                知道了
              </button>
            </div>
          )}
          <Tabs.Content value="chat" className={deskStyles.content}>
            <CompanionChat
              owner={userId}
              desk={desk}
              conversation={conversation}
              bridge={bridge}
              connected={connected}
              onTool={conversation.chat.selectView}
              onOpenConversation={onOpenConversation}
            />
          </Tabs.Content>
          <Tabs.Content value="focus" className={deskStyles.content}>
            <FocusDesk desk={desk} />
          </Tabs.Content>
          <Tabs.Content value="todos" className={deskStyles.content}>
            <TodoDesk desk={desk} onFocus={() => conversation.chat.selectView("focus")} />
          </Tabs.Content>
        </Tabs.Root>
        {connected && (
          <details className={chatStyles.details}>
            <summary>NAS 任务与记录</summary>
            <Suspense fallback={<p>正在连接 VNAS…</p>}>
              <TaskExperience
                userId={userId}
                view={view}
                bridge={bridge}
                onChatState={conversation.chat.observe}
                chatActive={value.place === "home"}
                onPhase={() => {}}
                onOpenConversation={onOpenConversation}
              />
            </Suspense>
          </details>
        )}
        {!desk.value.persistent && (
          <p role="status" className={deskStyles.hint}>
            浏览器暂时无法保存，这次的计时和待办仅在当前页面保留。
          </p>
        )}
        <p className={deskStyles.hint}>
          形象：社区二创「大肥鱼」，动画来自{" "}
          <a href="https://github.com/PC2005-cloud/dsh-pet" target="_blank" rel="noreferrer">
            github.com/PC2005-cloud/dsh-pet
          </a>
          ，按 CC BY-NC-SA 4.0 仅作非商业试验。
        </p>
      </div>
    </section>
  );
}
