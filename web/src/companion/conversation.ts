import { useEffect, useMemo, useSyncExternalStore } from "react";

import type { PersonalDeskState } from "./deskStore";
import { localCommand } from "./localCommands";
import type { TaskPhase } from "./taskState";

export type ChatLine = { id: number; role: "user" | "assistant"; text: string; kind?: "focus" | "todo" | "agent" };
export type AgentSnapshot = { phase: TaskPhase; answer: string; running: boolean; message: string };
export type AgentBridge = { send(text: string): { accepted: boolean; message?: string }; open(): void };
type ChatState = {
  view: "chat" | "focus" | "todos";
  lines: ChatLine[];
  awaiting?: "todo" | "duration" | "break-duration";
  pending?: { id: number; started: boolean };
};

export function createConversation() {
  let serial = 0;
  let state: ChatState = { view: "chat", lines: [] };
  const listeners = new Set<() => void>();
  const alerts = new Set<number>();
  const change = (next: ChatState) => {
    state = next;
    for (const listener of listeners) listener();
  };
  const say = (text: string, role: ChatLine["role"] = "assistant", kind?: ChatLine["kind"]) => {
    const id = ++serial;
    change({ ...state, lines: [...state.lines, { id, role, text, kind }].slice(-30) });
    return id;
  };
  const finish = (text: string, complete: boolean) => {
    if (!state.pending) return;
    const id = state.pending.id;
    change({
      ...state,
      lines: state.lines.map((line) => (line.id === id ? { ...line, text } : line)),
      pending: complete ? undefined : state.pending,
    });
  };
  return {
    get: () => state,
    selectView: (view: ChatState["view"]) => change({ ...state, view }),
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    notifyFocus: (at: number, message: string) => {
      if (alerts.has(at)) return;
      alerts.add(at);
      if (alerts.size > 30) alerts.delete(alerts.values().next().value!);
      say(message, "assistant", "focus");
    },
    observe: (snapshot: AgentSnapshot) => {
      if (!state.pending) return;
      if (snapshot.running && !state.pending.started)
        change({ ...state, pending: { ...state.pending, started: true } });
      if (snapshot.phase === "error") {
        finish(snapshot.message || "这次没能完成，请打开 NAS 对话查看。", true);
        return;
      }
      if (!state.pending?.started) return;
      if (snapshot.phase === "waiting") {
        finish("这一步需要你确认。打开 NAS 对话查看确认卡，我不会替你决定。", true);
        return;
      }
      if (snapshot.phase === "stopped") {
        finish("这次回答已停下，已做的操作可以在 NAS 对话中查看。", true);
        return;
      }
      if (snapshot.answer) finish(snapshot.answer, snapshot.phase === "success");
      else if (snapshot.phase === "success") finish("NAS 已回复，打开完整对话查看。", true);
    },
    send: (input: string, desk: PersonalDeskState, bridge?: AgentBridge) => {
      const text = input.trim();
      if (!text || text.length > 1000) return;
      say(text, "user");
      desk.store.tick();
      let command = localCommand(text);
      if (!command && state.awaiting === "todo") command = { kind: "todo", text };
      if (!command && (state.awaiting === "duration" || state.awaiting === "break-duration"))
        command = localCommand(`${state.awaiting === "break-duration" ? "休息" : "专注"}${text}`);
      if (command) {
        change({ ...state, awaiting: undefined });
        const focus = desk.store.get().focus;
        if (command.kind === "cancel") {
          say("好，先不记，也不会改变正在进行的计时。");
          return;
        }
        if (command.kind === "todo") {
          if (!command.text) {
            change({ ...state, awaiting: "todo" });
            say("要记下什么事？直接告诉我内容就好。");
            return;
          }
          if (command.text.length > 160) {
            say("待办请控制在 160 字以内，可以拆成几件小事。");
            return;
          }
          if (desk.store.get().todos.length >= 100) {
            say("已经有 100 条了，先从待办里删掉一些再记吧。");
            return;
          }
          desk.store.add(command.text);
          say(`记下了：${command.text}。想开始时，可以去待办里点“专注 25 分钟”。`, "assistant", "todo");
          return;
        }
        if (command.kind === "focus" || command.kind === "break") {
          if (focus.status === "running" || focus.status === "paused") {
            say("已经有一轮计时了。可以说“暂停计时”“继续计时”或“结束计时”，我不会覆盖它。");
            return;
          }
          if (command.invalid) {
            say("计时请用 1–90 的整数分钟，例如“专注25分钟”。");
            return;
          }
          if (!command.minutes) {
            change({ ...state, awaiting: command.kind === "break" ? "break-duration" : "duration" });
            say(command.kind === "break" ? "想休息几分钟？例如5分钟。" : "想专注几分钟？例如25分钟。");
            return;
          }
          desk.store.start(command.minutes, command.kind === "break" ? "break" : "focus");
          say(
            command.kind === "break"
              ? `开始休息 ${command.minutes} 分钟，到点我叫你。`
              : `开始专注 ${command.minutes} 分钟。我帮你看时间，到点提醒你休息。`,
            "assistant",
            "focus",
          );
          return;
        }
        if (command.kind === "pause") {
          if (focus.status === "running") {
            desk.store.pause();
            say("已暂停，剩余时间会保留。准备好了说“继续计时”。", "assistant", "focus");
          } else say("现在没有正在走的计时。");
          return;
        }
        if (command.kind === "resume") {
          if (focus.status === "paused") {
            desk.store.resume();
            say("继续计时，我在旁边陪你。", "assistant", "focus");
          } else say("现在没有暂停中的计时。");
          return;
        }
        if (command.kind === "stop") {
          desk.store.reset();
          say(
            focus.status === "done"
              ? "计时已收起，已完成的轮次会保留。"
              : focus.status === "idle"
                ? "现在没有计时，想开始时告诉我。"
                : "这一轮结束了，不计入完成轮次。想重新开始时告诉我。",
            "assistant",
            "focus",
          );
          return;
        }
        if (command.kind === "list") {
          const todos = desk.store.get().todos.filter((t) => !t.done);
          say(
            todos.length
              ? `还有 ${todos.length} 件待办：\n${todos
                  .slice(0, 8)
                  .map((t) => `· ${t.text}`)
                  .join("\n")}`
              : "现在没有未完成的待办。想起什么，可以随时告诉我。",
            "assistant",
            "todo",
          );
          return;
        }
        if (command.kind === "complete") {
          const matches = desk.store.get().todos.filter((t) => !t.done && t.text === command.text);
          if (matches.length === 1) {
            desk.store.toggle(matches[0].id);
            say(`完成了：${command.text}。又做好一件小事。`, "assistant", "todo");
          } else
            say(
              matches.length
                ? "有几条同名待办，请在清单中勾选要完成的那一条。"
                : "请给我完整的待办名称，也可以直接在清单里勾选。",
            );
          return;
        }
      }
      if (state.awaiting === "duration" || state.awaiting === "break-duration") {
        say("请告诉我 1–90 的整数分钟，例如“25分钟”；不想安排了可以说“取消”。");
        return;
      }
      if (state.pending) {
        say("上一件 NAS 工作还在处理；本机计时和待办仍然可以用。");
        return;
      }
      if (!bridge) {
        say("我现在能直接帮你计时和记待办；其他工作需要先连接 NAS 的 AI。试试“专注25分钟”或“帮我记一下：明天交材料”。");
        return;
      }
      const id = say("正在交给 NAS…", "assistant", "agent");
      change({ ...state, pending: { id, started: false } });
      try {
        const result = bridge.send(text);
        if (!result.accepted) finish(result.message || "NAS 还没准备好，请稍后再试。", true);
      } catch {
        finish("暂时没能提交给 NAS，没有自动重试。请检查连接后再试。", true);
      }
    },
  };
}
const conversations = new Map<string, ReturnType<typeof createConversation>>();
export function useConversation(owner: string, desk: PersonalDeskState) {
  const chat = useMemo(() => {
    let found = conversations.get(owner);
    if (!found) {
      found = createConversation();
      conversations.set(owner, found);
    }
    return found;
  }, [owner]);
  const state = useSyncExternalStore(chat.subscribe, chat.get);
  const completedAt = desk.value.focus.completedAt;
  useEffect(() => {
    if (completedAt && desk.value.focus.status === "done" && completedAt > desk.value.focusAck)
      chat.notifyFocus(
        completedAt,
        desk.value.focus.mode === "break"
          ? "休息到点了，要开始下一件小事吗？"
          : "这一轮专注完成啦！起来活动一下，或者休息5分钟吧。",
      );
  }, [chat, completedAt, desk.value.focus.status, desk.value.focus.mode, desk.value.focusAck]);
  return { chat, state };
}
