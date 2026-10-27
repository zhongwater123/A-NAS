import { formatBytes } from "../bytes";
import { relativeTime as formatRelativeTime } from "../format/time";
// AI 助手 window, named VNAS on the desktop (ADR 0024 decision 9). The thread is assistant-ui — the chat
// library Hermes Desktop is built on — fed with Hermes Desktop's own transcript
// model over its own JSON-RPC client; the conversation list, confirmation cards
// and styling are A-NAS's.
import {
  AssistantRuntimeProvider,
  ChainOfThoughtPrimitive,
  ComposerPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  useAuiState,
  useExternalStoreRuntime,
  type AppendMessage,
  type ExternalStoreAdapter,
  type ThreadMessageLike,
  type ToolCallMessagePartComponent,
} from "@assistant-ui/react";
import {
  type CodeHeaderProps,
  type StreamdownTextComponents,
  StreamdownTextPrimitive,
} from "@assistant-ui/react-streamdown";
import { diffLines } from "diff";
import {
  ArrowUp,
  Brain,
  CalendarClock,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  CircleHelp,
  Code,
  Copy,
  FilePen,
  FileSearch,
  FileText,
  FolderPlus,
  Hand,
  History,
  Eye,
  Image as ImageIcon,
  Info,
  ListChecks,
  LoaderCircle,
  LockKeyhole,
  MessageSquarePlus,
  MoveRight,
  Pencil,
  Search,
  Send,
  ShieldCheck,
  Square,
  SquareTerminal,
  Trash2,
  Users,
  Video,
  Wrench,
  X,
  Keyboard,
  MonitorSmartphone,
  MousePointerClick,
} from "lucide-react";
import {
  type ComponentPropsWithoutRef,
  type ErrorInfo,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  Component,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useEffectEvent,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { AssistantGlyph } from "../AssistantGlyph";
import { copyText } from "../clipboard";
import {
  type Approval,
  type AssistantQuestion,
  type AssistantSession,
  type ChangeOperation,
  answerQuestions,
  decideApproval,
  deleteSession,
  failureText,
  listApprovals,
  listSessions,
  renameSession,
} from "./assistantApi";
import { chatMessageText } from "./hermes/lib/chat-messages/parts";
import { withUniqueToolCallIdsWithinMessage } from "./hermes/lib/chat-messages/tool-parts";
import type { ChatMessage } from "./hermes/lib/chat-messages/types";
import type { SessionActiveListResult } from "./hermes/shared/gateway-contract.generated";
import { MemoryView } from "./MemoryView";
import { readTaskNotices, taskOfRun, useTaskNotices } from "./taskNotices";
import { type Danger, ControlError, WebosController } from "./webosControl";
import { TasksView } from "./TasksView";
import { type NamedPhoto, linkPhotos, photoOf, photosIn } from "./photoLinks";
import { useAssistantGateway } from "./useAssistantGateway";
import { useAssistantThread } from "./useAssistantThread";
import "./assistant.css";

const SUGGESTIONS = [
  "总结一下共享空间里有哪些文件",
  "帮我在个人空间的 计划.md 里加一项：交电费",
  "我之前删掉过一个文件，帮我在回收站找找",
];

// How long a step VNAS takes on the desktop waits for the member to allow it:
// Hermes gives the window 30 s to answer a browser command.
const CONTROL_CONFIRM_MS = 25_000;
// The window serves these of Hermes's browser commands (internal/assistantapi rpc.go);
// a screenshot is what Hermes's browser_vision looks at (hermes-patches/0003).
const CONTROL_CAPABILITIES = [
  "browser_snapshot",
  "browser_click",
  "browser_type",
  "browser_press",
  "browser_scroll",
  "browser_screenshot",
];

// Opens a photo the assistant named in the photo app, from the desktop.
const PhotoOpener = createContext<((assetId: string) => void) | undefined>(undefined);

// Opening a run reads its task's notices.
function readRun(id: string) {
  const task = taskOfRun(id);
  if (task) readTaskNotices(task);
}

/** What the desktop asks the window to show: a run of a scheduled task, or the tasks. */
export interface AssistantFocus {
  sessionId?: string;
  title?: string;
  at: number;
}

export function AssistantPanel({
  username,
  onShowPhoto,
  focus,
  onFocusHandled,
}: {
  username: string;
  onShowPhoto?(assetId: string): void;
  focus?: AssistantFocus;
  onFocusHandled?(): void;
}) {
  const gateway = useAssistantGateway();
  const [sessions, setSessions] = useState<AssistantSession[]>([]);
  const [sessionsLoaded, setSessionsLoaded] = useState(false);
  const [activeId, setActiveId] = useState<string>();
  // The window shows a conversation, what the assistant remembers, or its scheduled tasks.
  const [view, setView] = useState<"chat" | "memory" | "tasks">("chat");
  // A run of a scheduled task opened from its task: not in the conversation list.
  const [run, setRun] = useState<{ id: string; title: string }>();
  const [approvals, setApprovals] = useState<Map<string, Approval>>(new Map());
  // Stored conversations with a turn running in the instance, wherever it was started.
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const [notice, setNotice] = useState("");
  // What Hermes's background review of a conversation's last turn changed, in
  // its own words, by live conversation; a new turn clears it.
  const [reviews, setReviews] = useState<Map<string, string>>(new Map());

  // A list that failed while the instance was still starting says so until a
  // later load succeeds.
  const listFailure = useRef("");
  const refreshSessions = useCallback(
    () =>
      listSessions()
        .then((list) => {
          setSessions(list);
          setSessionsLoaded(true);
          const failure = listFailure.current;
          listFailure.current = "";
          if (failure) setNotice((current) => (current === failure ? "" : current));
        })
        .catch((reason) => {
          listFailure.current = failureText(reason);
          setNotice(listFailure.current);
        }),
    [],
  );
  const refreshBusy = useCallback(() => {
    if (!gateway.ready) return;
    gateway.client
      .request<SessionActiveListResult>("session.active_list")
      .then((result) =>
        setBusy(new Set(result.sessions.filter((item) => item.status !== "idle").map((item) => item.session_key))),
      )
      .catch(() => undefined);
  }, [gateway.client, gateway.ready]);
  const keep = useCallback(
    (approval: Approval) => setApprovals((all) => new Map(all).set(approval.requestId, approval)),
    [],
  );
  const reloadApprovals = useCallback(() => {
    listApprovals()
      .then((list) => setApprovals(new Map(list.map((approval) => [approval.requestId, approval]))))
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    void refreshSessions();
  }, [refreshSessions]);
  // Conversations, cards and running turns, again on every (re)connection.
  useEffect(() => {
    if (!gateway.ready) return;
    void refreshSessions();
    reloadApprovals();
    refreshBusy();
  }, [gateway.ready, gateway.generation, refreshSessions, reloadApprovals, refreshBusy]);
  // The product service tells every window of the member how each card stands;
  // a card from a conversation this window has not listed yet, or Hermes saying
  // the conversations changed, brings the list up to date.
  const known = useEffectEvent((id: string) => sessions.some((session) => session.id === id));
  useEffect(
    () =>
      gateway.client.onEvent((event) => {
        const type = event.type as string;
        if (type === "anas.approval" && event.payload) {
          const approval = event.payload as unknown as Approval;
          keep(approval);
          if (approval.storedSessionId && !known(approval.storedSessionId)) void refreshSessions();
        }
        const session = typeof event.session_id === "string" ? event.session_id : "";
        if (type === "review.summary" && session) {
          // Hermes leads with 💾; the notice has its own icon.
          const text = String((event.payload as { text?: unknown } | undefined)?.text ?? "")
            .replace(/^\p{Extended_Pictographic}️?\s*/u, "")
            .trim();
          if (text) setReviews((all) => new Map(all).set(session, text));
        }
        if (type === "message.start" && session) {
          setReviews((all) => {
            if (!all.has(session)) return all;
            const next = new Map(all);
            next.delete(session);
            return next;
          });
        }
        if (type === "sessions.changed" || type === "session.title") void refreshSessions();
        if (type === "sessions.changed" || type === "message.complete") refreshBusy();
      }),
    [gateway.client, keep, refreshBusy, refreshSessions],
  );

  const changed = useCallback(() => {
    void refreshSessions();
    refreshBusy();
    // Hermes names a new conversation in the background after its first turn.
    window.setTimeout(() => void refreshSessions(), 4000);
  }, [refreshSessions, refreshBusy]);
  // VNAS operates the WebOS page it is on as Hermes's browser for its live
  // conversation (Hermes's browser control). A step whose writes delete, move,
  // share or change settings, and any step in the terminal, Docker or 应用中心,
  // waits here for the member.
  const [controlAsk, setControlAsk] = useState<{ danger: Danger; until: number; answer(allowed: boolean): void }>();
  const asking = useRef<(allowed: boolean) => void>(undefined);
  // One card at a time: a write that arrives while another card is open waits its turn.
  const cards = useRef(Promise.resolve());
  const controller = useRef<WebosController>(undefined);
  useEffect(() => {
    const installed = new WebosController({
      exclude: (element) => Boolean(element.closest(".app-window-assistant, .vt-toasts, .vnas-control-ring")),
      confirm: (danger) => {
        const shown = cards.current.then(
          () =>
            new Promise<boolean>((resolve) => {
              let timer = 0;
              const answer = (allowed: boolean) => {
                window.clearTimeout(timer);
                asking.current = undefined;
                setControlAsk(undefined);
                resolve(allowed);
              };
              asking.current = answer;
              timer = window.setTimeout(() => answer(false), CONTROL_CONFIRM_MS);
              setControlAsk({ danger, until: Date.now() + CONTROL_CONFIRM_MS, answer });
            }),
        );
        cards.current = shown.then(() => undefined);
        return shown;
      },
    });
    controller.current = installed;
    return installed.install();
  }, []);
  // The window that sends a prompt is the conversation's browser for its turn:
  // it registers again before every prompt, so the steps and their cards stay
  // on the device the member is using; a window that only shows the
  // conversation never registers (#160). These are the live sessions this
  // connection registered for.
  const controlled = useRef(new Set<string>());
  useLayoutEffect(() => {
    controlled.current.clear();
  }, [gateway.generation]);
  const beforePrompt = useCallback(
    (liveId: string) =>
      gateway.client
        .request("browser.controller.register", {
          session_id: liveId,
          protocol_version: 1,
          controller_id: "vnas-window",
          browser_profile_id: "webos",
          capabilities: CONTROL_CAPABILITIES,
        })
        .then(
          () => {
            controlled.current.add(liveId);
          },
          () => {
            controlled.current.delete(liveId);
          },
        ),
    [gateway.client],
  );
  useEffect(
    () =>
      gateway.client.onEvent((event) => {
        const type = event.type as string;
        const session = typeof event.session_id === "string" ? event.session_id : "";
        if (type === "browser.controller.cancel") {
          // Hermes stopped waiting: the step is not taken.
          asking.current?.(false);
          return;
        }
        if (type !== "browser.controller.command" || !controlled.current.has(session)) return;
        const command = (event.payload ?? {}) as {
          command_id?: string;
          action?: string;
          arguments?: Record<string, unknown>;
        };
        if (!command.command_id || !command.action || !controller.current) return;
        void controller.current
          .run(command.action, command.arguments ?? {})
          .then(
            (result) => ({ ok: true, result }),
            (reason) => ({
              ok: false,
              error: { message: reason instanceof ControlError ? reason.message : "这一步没有完成" },
            }),
          )
          .then((outcome) =>
            gateway.client.request("browser.controller.result", {
              session_id: session,
              command_id: command.command_id,
              ...outcome,
            }),
          )
          .catch(() => undefined);
      }),
    [gateway.client],
  );

  const thread = useAssistantThread(gateway, activeId, { onCreated: setActiveId, onChanged: changed, beforePrompt });
  const { send, cancel } = thread;

  // Results of scheduled tasks the member has not read; opening a run reads its task's.
  const unread = useTaskNotices().length;
  const showRun = useCallback((id: string, title: string) => {
    setRun({ id, title });
    setActiveId(id);
    setView("chat");
  }, []);
  const openRun = useCallback(
    (id: string, title: string) => {
      readRun(id);
      showRun(id, title);
    },
    [showRun],
  );
  // A task asked for from the tasks page goes to a new conversation, once the
  // thread has left the one that was open.
  const [plan, setPlan] = useState<string>();
  const [planned, setPlanned] = useState<{ text: string }>();
  if (plan !== undefined && !activeId) {
    setPlan(undefined);
    setPlanned({ text: plan });
  }
  const sendPlan = useEffectEvent((text: string) => void send(text));
  const sentPlan = useRef<{ text: string }>(undefined);
  useEffect(() => {
    if (!planned || sentPlan.current === planned) return;
    sentPlan.current = planned;
    sendPlan(planned.text);
  }, [planned]);
  // A toast on the desktop opens a run, or the tasks.
  const [focusBefore, setFocusBefore] = useState<typeof focus>();
  if (focusBefore !== focus) {
    setFocusBefore(focus);
    if (focus?.sessionId) showRun(focus.sessionId, focus.title ?? "定时任务");
    else if (focus) setView("tasks");
  }
  const focusHandled = useEffectEvent(() => onFocusHandled?.());
  useEffect(() => {
    if (!focus) return;
    if (focus.sessionId) readRun(focus.sessionId);
    focusHandled();
  }, [focus]);

  // assistant-ui re-reads its store whenever the adapter changes, so it only
  // changes with the messages or the running state.
  const visible = useMemo(() => thread.messages.filter((message) => !message.hidden), [thread.messages]);
  const adapter = useMemo(
    () => ({
      messages: visible,
      isRunning: thread.running,
      convertMessage: toThreadMessage,
      onNew: async (message: AppendMessage) =>
        send(message.content.map((part) => (part.type === "text" ? part.text : "")).join("")),
      onCancel: async () => cancel(),
    }),
    [visible, thread.running, send, cancel],
  );

  const remove = async (session: AssistantSession) => {
    if (!window.confirm(`删除对话“${session.title || "新对话"}”？删除后无法恢复。`)) return;
    // As Hermes Desktop does: stop and close the live session before deleting it.
    if (session.id === activeId && thread.liveId) {
      await gateway.client.request("session.interrupt", { session_id: thread.liveId }).catch(() => undefined);
      await gateway.client.request("session.close", { session_id: thread.liveId }).catch(() => undefined);
    }
    await deleteSession(session.id).catch((reason) => setNotice(failureText(reason)));
    if (session.id === activeId) setActiveId(undefined);
    void refreshSessions();
  };

  const rename = async (session: AssistantSession, title: string) => {
    const updated = await renameSession(session.id, title).catch((reason) => {
      setNotice(failureText(reason));
      return undefined;
    });
    if (updated)
      setSessions((list) => list.map((item) => (item.id === updated.id ? { ...item, title: updated.title } : item)));
  };

  const decide = async (approval: Approval, confirm: boolean, forSession = false) => {
    try {
      keep(await decideApproval(approval.requestId, confirm, forSession));
    } catch (reason) {
      setNotice(failureText(reason));
      reloadApprovals();
    }
  };

  const answer = async (approval: Approval, answers: Record<string, string | null>) => {
    try {
      keep(await answerQuestions(approval.requestId, answers));
    } catch (reason) {
      setNotice(failureText(reason));
      reloadApprovals();
    }
  };

  // A card belongs to the conversation it was asked in and shows only there,
  // above the composer, while it waits for the member: once answered (here or
  // in another window), withdrawn or timed out it leaves, and what came of it
  // is in the steps and the reply. The list marks the conversations waiting
  // for an answer.
  const active = sessions.find((session) => session.id === activeId);
  const waiting = new Map<string, number>();
  for (const approval of approvals.values()) {
    if (approval.status === "awaiting" && approval.storedSessionId)
      waiting.set(approval.storedSessionId, (waiting.get(approval.storedSessionId) ?? 0) + 1);
  }
  const here = [...approvals.values()]
    .filter(
      (approval) =>
        (activeId && approval.storedSessionId === activeId) || (thread.liveId && approval.sessionId === thread.liveId),
    )
    .filter((approval) => approval.status === "awaiting" || approval.status === "deciding")
    .sort((a, b) => a.createdAt - b.createdAt);
  const unreachable = !gateway.ready && gateway.problem;
  const runOpen = Boolean(run && run.id === activeId && !active);
  const dock = (
    <>
      {controlAsk && <ControlCard ask={controlAsk} />}
      {here.length > 0 && (
        <section className="as-changes" aria-label="本对话中等你确认或回答的事">
          {here.map((approval) =>
            approval.kind === "command" ? (
              <CommandCard
                key={approval.requestId}
                approval={approval}
                onDecide={(confirm, forSession) => decide(approval, confirm, forSession)}
              />
            ) : approval.kind === "question" ? (
              <QuestionCard
                key={approval.requestId}
                approval={approval}
                onAnswer={(answers) => answer(approval, answers)}
                onSkip={() => decide(approval, false)}
              />
            ) : (
              <ApprovalCard
                key={approval.requestId}
                approval={approval}
                onDecide={(confirm) => decide(approval, confirm)}
              />
            ),
          )}
        </section>
      )}
    </>
  );

  return (
    <PhotoOpener.Provider value={onShowPhoto}>
      <div className="as-app">
        <aside className="as-side">
          <button
            type="button"
            className="as-new"
            onClick={() => {
              setActiveId(undefined);
              setView("chat");
            }}
          >
            <MessageSquarePlus />
            新对话
          </button>
          <button
            type="button"
            className={`as-nav${view === "memory" ? " active" : ""}`}
            aria-pressed={view === "memory"}
            onClick={() => setView("memory")}
          >
            <Brain />
            记忆<small>VNAS 记住的内容</small>
          </button>
          <button
            type="button"
            className={`as-nav${view === "tasks" ? " active" : ""}`}
            aria-pressed={view === "tasks"}
            onClick={() => setView("tasks")}
          >
            <CalendarClock />
            定时任务
            {unread > 0 ? (
              <span className="as-nav-count" title={`${unread} 个任务有新结果`}>
                {unread}
              </span>
            ) : (
              <small>按时替你做事</small>
            )}
          </button>
          <div className="as-side-label">
            <History />
            对话
          </div>
          <ul className="as-sessions">
            {sessions.map((session) => (
              <SessionRow
                key={session.id}
                session={session}
                active={session.id === activeId}
                waiting={waiting.get(session.id) ?? 0}
                running={busy.has(session.id)}
                onOpen={() => {
                  setActiveId(session.id);
                  setView("chat");
                }}
                onRename={(title) => void rename(session, title)}
                onDelete={() => void remove(session)}
              />
            ))}
            {!sessions.length && (
              <li className="as-sessions-empty">
                {sessionsLoaded ? (
                  "还没有对话"
                ) : (
                  <>
                    <LoaderCircle />
                    正在启动 VNAS…
                  </>
                )}
              </li>
            )}
          </ul>
          <TrustNote />
        </aside>

        <main className="as-main">
          <header className="as-head">
            {view === "chat" && runOpen && (
              <button type="button" className="as-head-back" onClick={() => setView("tasks")} aria-label="回到定时任务">
                <ChevronLeft />
                <CalendarClock />
              </button>
            )}
            <h2>
              {view === "memory"
                ? "记忆"
                : view === "tasks"
                  ? "定时任务"
                  : active?.title || (runOpen ? run?.title : activeId ? "新对话" : "开始一个对话")}
            </h2>
            {thread.running && (
              <span className="as-working">
                <LoaderCircle />
                正在处理
              </span>
            )}
            {!gateway.ready && !unreachable && (
              <span className="as-working">
                <LoaderCircle />
                正在连接 VNAS
              </span>
            )}
          </header>

          {view === "memory" && (
            <MemoryView
              onConversationsDeleted={() => {
                setActiveId(undefined);
                void refreshSessions();
              }}
            />
          )}
          {view === "tasks" && (
            <TasksView
              onOpenRun={openRun}
              onPlan={(text) => {
                setActiveId(undefined);
                setView("chat");
                if (text) setPlan(text);
              }}
            />
          )}
          <ConversationBoundary
            key={activeId ?? ""}
            messages={thread.messages}
            fallback={
              <div className="as-thread" hidden={view !== "chat"}>
                <div className="as-thread-failed" role="alert">
                  <CircleAlert />
                  <strong>这个对话暂时无法显示</strong>
                  <p>VNAS 仍在后台处理，等你确认的事照常显示在下方。可以打开其他对话，或开始新对话。</p>
                  <button type="button" onClick={() => setActiveId(undefined)}>
                    <MessageSquarePlus />
                    开始新对话
                  </button>
                </div>
                {dock}
              </div>
            }
          >
            <ConversationRuntime adapter={adapter}>
              <ThreadPrimitive.Root className="as-thread" hidden={view !== "chat"}>
                <ThreadPrimitive.Viewport className="as-viewport" autoScroll={Boolean(activeId)}>
                  {!activeId && !thread.messages.length ? (
                    <Welcome username={username} onPick={(text) => void send(text)} />
                  ) : thread.loading && !thread.messages.length ? (
                    <div className="as-loading">
                      <LoaderCircle />
                      正在打开对话…
                    </div>
                  ) : (
                    <ThreadPrimitive.Messages components={THREAD_COMPONENTS} />
                  )}
                  {(thread.error || notice || unreachable) && (
                    <div className="as-error" role="alert">
                      <CircleAlert />
                      <span>{thread.error || notice || unreachable}</span>
                      {notice && (
                        <button type="button" aria-label="关闭" onClick={() => setNotice("")}>
                          <X />
                        </button>
                      )}
                    </div>
                  )}
                  {thread.liveId && reviews.get(thread.liveId) && (
                    <div className="as-review" role="status">
                      <div className="as-review-note">
                        <Brain />
                        <span>{reviews.get(thread.liveId)}</span>
                        <button type="button" onClick={() => setView("memory")}>
                          查看记忆
                        </button>
                      </div>
                    </div>
                  )}
                  <div className="as-viewport-spacer" />
                </ThreadPrimitive.Viewport>

                {dock}

                <ComposerPrimitive.Root className="as-composer">
                  <ComposerPrimitive.Input
                    className="as-input"
                    placeholder="让 VNAS 帮你找文件、整理内容，或修改你的文件…"
                    rows={1}
                    autoFocus
                    submitOnEnter
                  />
                  {thread.running ? (
                    <ComposerPrimitive.Cancel className="as-send stop" aria-label="停止">
                      <Square />
                    </ComposerPrimitive.Cancel>
                  ) : (
                    <ComposerPrimitive.Send className="as-send" aria-label="发送">
                      <ArrowUp />
                    </ComposerPrimitive.Send>
                  )}
                </ComposerPrimitive.Root>
              </ThreadPrimitive.Root>
            </ConversationRuntime>
          </ConversationBoundary>
        </main>
      </div>
    </PhotoOpener.Provider>
  );
}

// The conversation's assistant-ui runtime. It lives inside the conversation's
// boundary, so a message assistant-ui refuses while taking it in is caught there
// as well, and each conversation starts with a fresh one.
function ConversationRuntime({
  adapter,
  children,
}: {
  adapter: ExternalStoreAdapter<ChatMessage>;
  children: ReactNode;
}) {
  const runtime = useExternalStoreRuntime<ChatMessage>(adapter);
  return <AssistantRuntimeProvider runtime={runtime}>{children}</AssistantRuntimeProvider>;
}

// A conversation that fails to render takes down only itself: the list, the
// cards waiting for the member and the other conversations stay usable, where
// the desktop's window boundary would replace all of VNAS and ask to reload
// WebOS. The next change of its messages tries again.
class ConversationBoundary extends Component<
  { messages: unknown; fallback: ReactNode; children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("VNAS conversation render failed", error, info.componentStack);
  }

  componentDidUpdate(previous: { messages: unknown }) {
    // oxlint-disable-next-line react/no-did-update-set-state -- an error boundary resets once its input changes
    if (this.state.failed && previous.messages !== this.props.messages) this.setState({ failed: false });
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

// Hermes opens each run of a scheduled task with its own instructions for the
// run (cron/scheduler_prompt.py), then the task's prompt.
const CRON_PREAMBLE = /^\[IMPORTANT: You are running as a scheduled cron job[\s\S]*?\]\s*\n\n/;

/** The task's prompt, when the message is the start of a scheduled run. */
export function scheduledPrompt(message: ChatMessage): string | undefined {
  if (message.role !== "user") return undefined;
  const text = message.parts.map((part) => (part.type === "text" ? part.text : "")).join("");
  const preamble = CRON_PREAMBLE.exec(text);
  return preamble ? text.slice(preamble[0].length).trim() : undefined;
}

// Hermes closes a turn that ended without an answer with a notice of its own
// (agent/turn_failure_copy.py), which its history serves as a system message;
// the member reads it in their own words.
const TURN_NOTICES: Record<string, string> = {
  "Your request was not processed. Send it again if you still want me to carry it out.":
    "这次请求没有处理。如果还需要，请重新发送。",
  "This turn did not complete. Some actions may already have run; verify their effects before resending.":
    "这一轮没有完成，其中一些操作可能已经执行；重新发送前请先确认结果。",
};

// assistant-ui throws, and takes the whole window down with it, on a message it
// cannot take: a status on anything but an answer, a notice that is not exactly
// one text, anything but text from the member, or a tool call id repeated in one
// message. Messages are shaped as Hermes Desktop shapes them for it
// (lib/chat-runtime.ts, and #87857 for the ids).
export function toThreadMessage(original: ChatMessage): ThreadMessageLike {
  const message = withUniqueToolCallIdsWithinMessage(original);
  const createdAt = message.timestamp ? new Date(message.timestamp * 1000) : undefined;
  const prompt = scheduledPrompt(message);
  if (prompt !== undefined) {
    return {
      id: message.id,
      role: "user",
      content: [{ type: "text", text: prompt }],
      createdAt,
      metadata: { custom: { scheduled: true } },
    };
  }
  if (message.role === "user")
    return { id: message.id, role: "user", content: message.parts.filter((part) => part.type === "text"), createdAt };
  if (message.role === "system") {
    const text = chatMessageText(message).trim();
    return { id: message.id, role: "system", content: [{ type: "text", text: TURN_NOTICES[text] ?? text }], createdAt };
  }
  return {
    id: message.id,
    role: "assistant",
    content: message.parts.map((part) => (part.type === "text" ? { ...part, text: linkPhotos(part.text) } : part)),
    createdAt,
    status: message.pending
      ? { type: "running" }
      : message.interrupted
        ? { type: "incomplete", reason: "cancelled" }
        : message.error
          ? { type: "incomplete", reason: "error" }
          : { type: "complete", reason: "stop" },
  } as ThreadMessageLike;
}

// A step VNAS wants to take on the desktop that deletes, replaces or resets
// something: the member allows it, or not, before Hermes stops waiting.
function ControlCard({ ask }: { ask: { danger: Danger; until: number; answer(allowed: boolean): void } }) {
  const [left, setLeft] = useState(() => Math.max(0, Math.ceil((ask.until - Date.now()) / 1000)));
  useEffect(() => {
    const timer = window.setInterval(() => setLeft(Math.max(0, Math.ceil((ask.until - Date.now()) / 1000))), 500);
    return () => window.clearInterval(timer);
  }, [ask.until]);
  return (
    <section className="as-changes" aria-label="VNAS 想在桌面上执行的操作">
      <article className="as-change as-command awaiting" role="alertdialog" aria-label={`VNAS 想${ask.danger.what}`}>
        <header>
          <span className="as-change-mark">
            <MousePointerClick />
          </span>
          <div>
            <strong>VNAS 想{ask.danger.what}</strong>
            <small>
              在“{ask.danger.where}”中 · {ask.danger.why} · {left} 秒内不回答就不执行
            </small>
          </div>
        </header>
        <footer>
          <button type="button" className="as-reject" onClick={() => ask.answer(false)}>
            不允许
          </button>
          <button type="button" className="as-confirm" onClick={() => ask.answer(true)}>
            <Check />
            允许
          </button>
        </footer>
      </article>
    </section>
  );
}

function SessionRow({
  session,
  active,
  waiting,
  running,
  onOpen,
  onRename,
  onDelete,
}: {
  session: AssistantSession;
  active: boolean;
  waiting: number;
  running: boolean;
  onOpen(): void;
  onRename(title: string): void;
  onDelete(): void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(session.title);
  const finish = () => {
    setEditing(false);
    if (draft.trim() && draft.trim() !== session.title) onRename(draft.trim());
  };
  return (
    <li className={`as-session${active ? " active" : ""}`}>
      {editing ? (
        <input
          className="as-session-edit"
          value={draft}
          autoFocus
          maxLength={100}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={finish}
          onKeyDown={(event) => {
            if (event.key === "Enter") finish();
            if (event.key === "Escape") {
              setDraft(session.title);
              setEditing(false);
            }
          }}
        />
      ) : (
        <button type="button" className="as-session-open" onClick={onOpen}>
          <span className="as-session-line">
            <strong>{session.title || session.preview || "新对话"}</strong>
            {waiting > 0 ? (
              <span className="as-session-badge" title={`${waiting} 项更改等你确认`}>
                {waiting}
              </span>
            ) : (
              running && <LoaderCircle className="as-session-running" aria-label="正在回答" />
            )}
          </span>
          <small>
            {relativeTime(session.lastActive ?? session.startedAt)}
            {waiting > 0 ? <em> · 等你确认</em> : running && " · 正在回答"}
          </small>
        </button>
      )}
      {!editing && (
        <span className="as-session-actions">
          <button
            type="button"
            aria-label="改名"
            onClick={() => {
              setDraft(session.title);
              setEditing(true);
            }}
          >
            <Pencil />
          </button>
          <button type="button" aria-label="删除" onClick={onDelete}>
            <Trash2 />
          </button>
        </span>
      )}
    </li>
  );
}

// What makes VNAS the member's own, said for someone who has never heard of
// an agent. Each line holds only what ADR 0024 enforces; none says the model
// runs on this device, because during development it does not.
const TRUST: { icon: ReactNode; title: string; text: string }[] = [
  {
    icon: <ShieldCheck />,
    title: "安全",
    text: "它只看得到你的文件和照片，以及家里共享的内容；看不到家人的私人文件，也碰不到系统设置。",
  },
  {
    icon: <Hand />,
    title: "可控",
    text: "改动你的文件前，它会先把要改的地方给你看，你点“确认更改”才生效；运行有风险的命令也要先问你。被替换或删除的旧文件会放进回收站，可以找回。",
  },
  {
    icon: <LockKeyhole />,
    title: "隐私",
    text: "每位家人的 VNAS 各自独立。你和它的对话、它记住的事，家里其他人都看不到，管理员也不行；在“记忆”里可以随时删掉。",
  },
];

// The sidebar's one line about trust. Pointing at it or focusing it with the
// keyboard shows why; a tap pins it open for touch screens, and a tap
// elsewhere or Esc closes it again.
function TrustNote() {
  const [pinned, setPinned] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (!pinned) return;
    const away = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setPinned(false);
    };
    document.addEventListener("pointerdown", away);
    return () => document.removeEventListener("pointerdown", away);
  }, [pinned]);
  const escape = (event: ReactKeyboardEvent) => {
    if (event.key !== "Escape") return;
    setPinned(false);
    (document.activeElement as HTMLElement | null)?.blur();
  };
  return (
    <div ref={root} className={`as-trust${pinned ? " open" : ""}`} onKeyDown={escape}>
      <button
        type="button"
        className="as-trust-button"
        aria-describedby={id}
        aria-expanded={pinned}
        onClick={() => setPinned((open) => !open)}
      >
        <ShieldCheck />
        <span>你的私人 Agent</span>
        <Info className="as-trust-hint" />
      </button>
      <div id={id} role="tooltip" className="as-trust-pop">
        <strong>为什么可以放心用</strong>
        <ul>
          {TRUST.map((item) => (
            <li key={item.title}>
              <span className="as-trust-icon">{item.icon}</span>
              <div>
                <b>{item.title}</b>
                <p>{item.text}</p>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

function Welcome({ username, onPick }: { username: string; onPick(text: string): void }) {
  return (
    <div className="as-welcome">
      <span className="as-welcome-mark">
        <AssistantGlyph />
      </span>
      <h3>你好，{username}</h3>
      <p>我是你的私人 NAS 助理，需要我做什么？</p>
      <div className="as-suggestions">
        {SUGGESTIONS.map((text) => (
          <button type="button" key={text} onClick={() => onPick(text)}>
            {text}
            <ChevronRight />
          </button>
        ))}
      </div>
    </div>
  );
}

function UserMessage() {
  const scheduled = useAuiState((state) => Boolean(state.message.metadata?.custom?.scheduled));
  if (scheduled) return <ScheduledStart />;
  return (
    <MessagePrimitive.Root className="as-msg user">
      <div className="as-bubble">
        <MessagePrimitive.Parts />
      </div>
    </MessagePrimitive.Root>
  );
}

// A run of a scheduled task starts with no one having typed anything: a marker,
// with what the task asks folded under it.
function ScheduledStart() {
  const [open, setOpen] = useState(false);
  return (
    <MessagePrimitive.Root className="as-msg scheduled">
      <div className="as-scheduled">
        <button type="button" aria-expanded={open} onClick={() => setOpen((shown) => !shown)}>
          <CalendarClock />
          <span>定时任务按计划运行</span>
          <small>{open ? "收起" : "要做的事"}</small>
          <ChevronDown className={open ? "open" : undefined} />
        </button>
        {open && (
          <div className="as-scheduled-prompt">
            <MessagePrimitive.Parts />
          </div>
        )}
      </div>
    </MessagePrimitive.Root>
  );
}

// A notice Hermes wrote into the conversation itself, such as a turn that did
// not complete: a quiet line, not VNAS speaking.
function SystemNotice() {
  return (
    <MessagePrimitive.Root className="as-msg notice">
      <div className="as-notice">
        <Info />
        <div>
          <MessagePrimitive.Parts />
        </div>
      </div>
    </MessagePrimitive.Root>
  );
}

function AssistantMessage() {
  return (
    <MessagePrimitive.Root className="as-msg assistant">
      <span className="as-avatar">
        <AssistantGlyph />
      </span>
      <div className="as-answer">
        <MessagePrimitive.Parts components={ASSISTANT_PARTS} />
        <AnswerPhotos />
        <Outcome />
      </div>
    </MessagePrimitive.Root>
  );
}

// How a reply that did not finish ended: stopped by the member, or failed.
function Outcome() {
  const reason = useAuiState((state) =>
    state.message.status?.type === "incomplete" ? state.message.status.reason : undefined,
  );
  if (reason === "cancelled")
    return (
      <p className="as-outcome stopped">
        <Square />
        已停止
      </p>
    );
  if (reason === "error")
    return (
      <p className="as-outcome failed">
        <CircleAlert />
        这次回答没有完成
      </p>
    );
  return null;
}

// Streamdown's own code, table, diagram and image buttons are styled with
// Tailwind, which A-NAS does not load, so they are off; code blocks get the
// window's own header with a copy button instead.
export const MarkdownText = () => (
  <StreamdownTextPrimitive className="as-markdown" components={MARKDOWN_COMPONENTS} controls={false} />
);

// A code block's header: its language, and a copy that works on the LAN's
// plain HTTP, where the Clipboard API is unavailable. The props are typed as
// unknown because StreamdownTextComponents' index signature, meant for HTML
// elements, applies to CodeHeader too; the adapter passes CodeHeaderProps.
function CodeHeader({ language, code }: { [K in keyof CodeHeaderProps]?: unknown }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1600);
    return () => window.clearTimeout(timer);
  }, [copied]);
  const text = typeof code === "string" ? code.replace(/\n$/, "") : "";
  return (
    <div className="as-code-head">
      <span>{typeof language === "string" && language ? language : "文本"}</span>
      <button
        type="button"
        className={copied ? "copied" : undefined}
        aria-label={copied ? "已复制" : "复制代码"}
        onClick={() => setCopied(copyText(text))}
      >
        {copied ? <Check /> : <Copy />}
        {copied ? "已复制" : "复制"}
      </button>
    </div>
  );
}

// A photo the assistant names is a link to it in the text (the picture itself
// shows under the answer) and opens in the photo app; other links open in a
// new tab.
function MarkdownLink({ href, children, node: _node, ...rest }: ComponentPropsWithoutRef<"a"> & { node?: unknown }) {
  const open = useContext(PhotoOpener);
  const photo = photoOf(href);
  if (!photo)
    return (
      <a href={href} target="_blank" rel="noreferrer" {...rest}>
        {children}
      </a>
    );
  return (
    <button type="button" className="as-photo-ref" title="在相册中打开" disabled={!open} onClick={() => open?.(photo)}>
      <ImageIcon />
      {children}
    </button>
  );
}

// Pictures in an answer load only from A-NAS itself. An outside address
// would have the member's browser fetch it, carrying whatever the model put in
// it, which the sandbox having no network is meant to rule out (ADR 0024); the
// desktop's CSP blocks such images too (issue #161). It shows as a link the
// member may open instead.
function MarkdownImage({ src, alt, node: _node, ...rest }: ComponentPropsWithoutRef<"img"> & { node?: unknown }) {
  if (typeof src === "string" && onDevice(src)) return <img src={src} alt={alt} {...rest} />;
  return (
    <a
      className="as-outside-image"
      href={typeof src === "string" ? src : undefined}
      target="_blank"
      rel="noreferrer"
      title="外部图片不会自动加载"
    >
      <ImageIcon />
      {alt || "外部图片"}
    </a>
  );
}

function onDevice(src: string) {
  if (/^data:image\//i.test(src)) return true;
  try {
    return new URL(src, window.location.href).origin === window.location.origin;
  } catch {
    return false;
  }
}

// More than this many photos show as the first ones and a count.
const ANSWER_PHOTOS = 12;

// The photos an answer names, under it as the pictures themselves, the way a
// chat shows photos sent in it: one large, several as tiles. They load with
// the member's own session; a click opens the photo in the photo app.
function AnswerPhotos() {
  const text = useAuiState((state) =>
    state.message.content.map((part) => (part.type === "text" ? part.text : "")).join("\n"),
  );
  const photos = useMemo(() => photosIn(text), [text]);
  if (!photos.length) return null;
  const shown = photos.slice(0, ANSWER_PHOTOS);
  return (
    <div className={`as-gallery${shown.length === 1 ? " one" : ""}`}>
      {shown.map((photo) => (
        <AnswerPhoto key={photo.id} photo={photo} />
      ))}
      {photos.length > shown.length && (
        <p className="as-gallery-more">另有 {photos.length - shown.length} 张，点上文的名称查看</p>
      )}
    </div>
  );
}

function AnswerPhoto({ photo }: { photo: NamedPhoto }) {
  const open = useContext(PhotoOpener);
  const [state, setState] = useState<"loading" | "ready" | "missing">("loading");
  return (
    <button
      type="button"
      className={`as-gallery-item ${state}`}
      aria-label={`${photo.name}，在相册中打开`}
      title="在相册中打开"
      disabled={!open}
      onClick={() => open?.(photo.id)}
    >
      <span className="as-gallery-frame">
        {state === "missing" ? (
          <ImageIcon />
        ) : (
          <img
            src={`/api/v1/photos/assets/${encodeURIComponent(photo.id)}/thumbnail`}
            alt=""
            loading="lazy"
            onLoad={() => setState("ready")}
            onError={() => setState("missing")}
          />
        )}
      </span>
      <span className="as-gallery-name">{state === "missing" ? `${photo.name}（打不开）` : photo.name}</span>
    </button>
  );
}

// One folded row per run of reasoning and tool calls, like Hermes Desktop's
// run summary: the member sees what the assistant did without the noise.
function Steps() {
  const count = useAuiState((state) => state.chainOfThought.parts.length);
  const collapsed = useAuiState((state) => state.chainOfThought.collapsed);
  const running = useAuiState((state) => state.chainOfThought.status.type === "running");
  const latest = useAuiState((state) => state.chainOfThought.parts.at(-1));
  const tools = useAuiState((state) => state.chainOfThought.parts.filter((part) => part.type === "tool-call").length);
  const current =
    latest?.type === "tool-call"
      ? describeTool(latest.toolName, (latest.args ?? {}) as Record<string, unknown>)
      : undefined;
  return (
    <ChainOfThoughtPrimitive.Root className={`as-steps${collapsed ? "" : " open"}${running ? " running" : ""}`}>
      <ChainOfThoughtPrimitive.AccordionTrigger className="as-steps-trigger">
        {running ? <LoaderCircle className="spin" /> : <ListChecks />}
        <span>{running ? "正在处理" : "处理过程"}</span>
        <small>
          {running && current
            ? `${current.label}${current.detail ? ` ${current.detail}` : ""}`
            : tools
              ? `${tools} 个动作`
              : `${count} 步`}
        </small>
        <ChevronRight className="chevron" />
      </ChainOfThoughtPrimitive.AccordionTrigger>
      {!collapsed && (
        <div className="as-steps-body">
          <ChainOfThoughtPrimitive.Parts components={STEP_PARTS} />
        </div>
      )}
    </ChainOfThoughtPrimitive.Root>
  );
}

// assistant-ui also renders Empty for a reply that ended without text; the
// dots only mean something while the reply is still coming.
function Thinking() {
  const running = useAuiState((state) => state.message.status?.type === "running");
  return running ? (
    <div className="as-thinking">
      <span />
      <span />
      <span />
    </div>
  ) : null;
}

function Reasoning({ text }: { text: string }) {
  if (!text.trim()) return null;
  return (
    <details className="as-reasoning">
      <summary>
        <Brain />
        思考过程
      </summary>
      <p>{text}</p>
    </details>
  );
}

// A friendlier name, with an icon, for each tool the assistant has.
function describeTool(name: string, args: Record<string, unknown>): { icon: ReactNode; label: string; detail: string } {
  const path = typeof args.path === "string" ? friendlyPath(args.path) : "";
  switch (name) {
    case "read_file":
      return { icon: <FileText />, label: "读取", detail: path };
    case "search_files":
      return {
        icon: <Search />,
        label: "搜索",
        detail: [typeof args.pattern === "string" ? `“${args.pattern}”` : "", path && `在 ${path}`]
          .filter(Boolean)
          .join(" "),
      };
    case "write_file":
      return { icon: <FilePen />, label: "写入草稿", detail: path };
    case "patch":
      return { icon: <FilePen />, label: "修改草稿", detail: path };
    case "mcp__anas__submit_changes":
      return {
        icon: <Send />,
        label: "提交更改",
        detail: typeof args.summary === "string" ? args.summary : "等你确认",
      };
    case "terminal":
      return {
        icon: <SquareTerminal />,
        label: "运行命令",
        detail: typeof args.command === "string" ? short(args.command) : "",
      };
    case "process_manage":
      return { icon: <SquareTerminal />, label: "管理后台命令", detail: "" };
    case "execute_code":
      return { icon: <Code />, label: "运行代码", detail: "" };
    case "delegate_task":
      return { icon: <Users />, label: "分派子任务", detail: typeof args.goal === "string" ? short(args.goal) : "" };
    case "clarify":
      return { icon: <CircleHelp />, label: "问你", detail: "" };
    case "mcp__anas__recent_changes":
      return { icon: <History />, label: "查看最近的更改", detail: "" };
    case "mcp__anas__search_photos":
      return { icon: <Search />, label: "搜索照片", detail: typeof args.query === "string" ? `“${args.query}”` : "" };
    case "mcp__anas__browse_photos":
      return { icon: <ImageIcon />, label: "浏览照片", detail: typeof args.month === "string" ? args.month : "" };
    case "mcp__anas__photo_details":
      return { icon: <ImageIcon />, label: "查看照片信息", detail: "" };
    case "mcp__anas__similar_photos":
      return { icon: <ImageIcon />, label: "找相似的照片", detail: "" };
    case "mcp__anas__look_at_photo":
      return { icon: <ImageIcon />, label: "取出照片画面", detail: "" };
    // A photo's view lands in Hermes's image cache; the question says more than that path.
    case "vision_analyze":
      return {
        icon: <ImageIcon />,
        label: "看图",
        detail:
          typeof args.question === "string" && args.question
            ? short(args.question)
            : typeof args.image_url === "string"
              ? friendlyPath(args.image_url)
              : "",
      };
    case "video_analyze":
      return {
        icon: <Video />,
        label: "看视频",
        detail: typeof args.video_url === "string" ? friendlyPath(args.video_url) : "",
      };
    case "memory":
      return { icon: <Brain />, label: "记住", detail: "" };
    case "session_search":
      return { icon: <FileSearch />, label: "查找以前的对话", detail: "" };
    case "skill_view":
    case "skills_list":
    case "skill_manage":
      return { icon: <Wrench />, label: "查看技能", detail: typeof args.name === "string" ? args.name : "" };
    case "browser_snapshot":
      return { icon: <MonitorSmartphone />, label: "查看桌面", detail: "" };
    case "browser_vision":
      return {
        icon: <Eye />,
        label: "看桌面画面",
        detail: typeof args.question === "string" ? short(args.question) : "",
      };
    case "browser_click":
      return { icon: <MousePointerClick />, label: "点击", detail: typeof args.ref === "string" ? args.ref : "" };
    case "browser_type":
      return {
        icon: <Keyboard />,
        label: "输入",
        detail: typeof args.text === "string" ? `“${short(args.text)}”` : "",
      };
    case "browser_press":
      return { icon: <Keyboard />, label: "按键", detail: typeof args.key === "string" ? args.key : "" };
    case "browser_scroll":
      return { icon: <MousePointerClick />, label: "滚动", detail: args.direction === "up" ? "向上" : "向下" };
    default:
      return { icon: <Wrench />, label: name, detail: "" };
  }
}

const ToolActivity: ToolCallMessagePartComponent = ({ toolName, args, result, status, isError }) => {
  const { icon, label, detail } = describeTool(toolName, (args ?? {}) as Record<string, unknown>);
  const running = status.type === "running";
  const failed = isError || status.type === "incomplete";
  const output = typeof result === "string" ? result : result === undefined ? "" : JSON.stringify(result, null, 2);
  return (
    <details className={`as-tool${running ? " running" : ""}${failed ? " failed" : ""}`}>
      <summary>
        <span className="as-tool-icon">{icon}</span>
        <span className="as-tool-label">{label}</span>
        {detail && <span className="as-tool-detail">{detail}</span>}
        <span className="as-tool-state">{running ? <LoaderCircle /> : failed ? <CircleAlert /> : <Check />}</span>
      </summary>
      {output && <pre>{output.length > 4000 ? `${output.slice(0, 4000)}…` : output}</pre>}
    </details>
  );
};

function ApprovalCard({ approval, onDecide }: { approval: Approval; onDecide(confirm: boolean): Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const choose = (confirm: boolean) => {
    setBusy(true);
    void onDecide(confirm).finally(() => setBusy(false));
  };
  const { change, status } = approval;
  if (!change) return null;
  const operations = change.operations ?? [];
  return (
    <article className="as-change">
      <header>
        <span className="as-change-mark">
          <ShieldCheck />
        </span>
        <div>
          <strong>{change.summary || "VNAS 提交的更改"}</strong>
          <small>{operations.length} 项操作 · 确认前文件不会变化</small>
        </div>
      </header>
      <ul className="as-change-ops">
        {operations.map((operation, index) => (
          <OperationRow key={index} operation={operation} />
        ))}
      </ul>
      <footer>
        <button
          type="button"
          className="as-reject"
          disabled={busy || status !== "awaiting"}
          onClick={() => choose(false)}
        >
          拒绝
        </button>
        <button
          type="button"
          className="as-confirm"
          disabled={busy || status !== "awaiting"}
          onClick={() => choose(true)}
        >
          {busy || status === "deciding" ? <LoaderCircle /> : <Check />}确认更改
        </button>
      </footer>
    </article>
  );
}

// Hermes names why it asks in English; the common reasons, in the member's words.
const COMMAND_REASONS: Record<string, string> = {
  "recursive delete": "会删除整个文件夹",
  "delete in root path": "会删除文件",
  "overwrite file": "会覆盖文件",
  "kill process": "会结束进程",
};

export function CommandCard({
  approval,
  onDecide,
}: {
  approval: Approval;
  onDecide(confirm: boolean, forSession: boolean): Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const choose = (confirm: boolean, forSession = false) => {
    setBusy(true);
    void onDecide(confirm, forSession).finally(() => setBusy(false));
  };
  const { command, status } = approval;
  const script = Boolean(command?.script);
  const reason = command?.description ? (COMMAND_REASONS[command.description] ?? command.description) : "";
  const shown = script
    ? (command?.text ?? "").replace(/^execute_code <<'PY'\n/, "").replace(/\nPY$/, "")
    : (command?.text ?? "");
  return (
    <article className="as-change as-command">
      <header>
        <span className="as-change-mark">{script ? <Code /> : <SquareTerminal />}</span>
        <div>
          <strong>{script ? "VNAS 想运行一段代码" : `VNAS 想运行一条命令${reason ? `：${reason}` : ""}`}</strong>
          <small>只影响 VNAS 自己的文件夹和对话记录；你的个人空间与共享空间不会被改动</small>
        </div>
      </header>
      <pre className="as-command-text">{shown}</pre>
      <footer>
        <button
          type="button"
          className="as-reject"
          disabled={busy || status !== "awaiting"}
          onClick={() => choose(false)}
        >
          拒绝
        </button>
        {command?.forSession && (
          <button
            type="button"
            className="as-reject"
            disabled={busy || status !== "awaiting"}
            onClick={() => choose(true, true)}
          >
            本对话中都允许
          </button>
        )}
        <button
          type="button"
          className="as-confirm"
          disabled={busy || status !== "awaiting"}
          onClick={() => choose(true)}
        >
          {busy || status === "deciding" ? <LoaderCircle /> : <Check />}允许
        </button>
      </footer>
    </article>
  );
}

export function QuestionCard({
  approval,
  onAnswer,
  onSkip,
}: {
  approval: Approval;
  onAnswer(answers: Record<string, string | null>): Promise<void>;
  onSkip(): Promise<void>;
}) {
  const questions = approval.questions ?? [];
  const [picked, setPicked] = useState<Record<string, string[]>>({});
  const [written, setWritten] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const { status } = approval;
  // The answer Hermes Desktop sends for a question (clarify/pending.tsx stagedAnswer): one
  // choice or the text, which replace each other; for several choices a JSON list of them
  // with the text as one more.
  const answerOf = (question: AssistantQuestion) => {
    const chosen = (picked[question.id] ?? []).map(plainChoice);
    const draft = (written[question.id] ?? "").trim();
    if (question.multiSelect) {
      const all = [...chosen, ...(draft ? [draft] : [])];
      return all.length ? JSON.stringify(all) : null;
    }
    return chosen[0] ?? (draft || null);
  };
  const anything = questions.some((question) => answerOf(question) !== null);
  const pick = (question: AssistantQuestion, choice: string) => {
    setPicked((all) => {
      const current = all[question.id] ?? [];
      const next = !question.multiSelect
        ? [choice]
        : current.includes(choice)
          ? current.filter((item) => item !== choice)
          : [...current, choice];
      return { ...all, [question.id]: next };
    });
    if (!question.multiSelect) setWritten((all) => ({ ...all, [question.id]: "" }));
  };
  const write = (question: AssistantQuestion, value: string) => {
    setWritten((all) => ({ ...all, [question.id]: value }));
    if (!question.multiSelect) setPicked((all) => ({ ...all, [question.id]: [] }));
  };
  const run = (work: () => Promise<void>) => {
    setBusy(true);
    void work().finally(() => setBusy(false));
  };
  return (
    <article className="as-change as-question">
      <header>
        <span className="as-change-mark">
          <CircleHelp />
        </span>
        <div>
          <strong>VNAS 想问你</strong>
          <small>{questions.length} 个问题 · 回答后 VNAS 接着做</small>
        </div>
      </header>
      <div className="as-questions">
        {questions.map((question) => (
          <div key={question.id} className="as-question-item">
            <p>{question.text}</p>
            {question.choices?.length ? (
              <div className="as-choices" role="group" aria-label={question.text}>
                {question.choices.map((choice) => (
                  <button
                    key={choice}
                    type="button"
                    aria-pressed={(picked[question.id] ?? []).includes(choice)}
                    disabled={busy}
                    onClick={() => pick(question, choice)}
                  >
                    {plainChoice(choice)}
                    {RECOMMENDED.test(choice) && <small>推荐</small>}
                  </button>
                ))}
              </div>
            ) : null}
            <input
              className="as-answer-input"
              aria-label={`回答：${question.text}`}
              placeholder={question.choices?.length ? "或者写下你的回答" : "写下你的回答"}
              value={written[question.id] ?? ""}
              disabled={busy}
              maxLength={4000}
              onChange={(event) => write(question, event.target.value)}
            />
          </div>
        ))}
      </div>
      <footer>
        <button
          type="button"
          className="as-reject"
          disabled={busy || status !== "awaiting"}
          onClick={() => run(onSkip)}
        >
          跳过
        </button>
        <button
          type="button"
          className="as-confirm"
          disabled={busy || status !== "awaiting" || !anything}
          onClick={() =>
            run(() => onAnswer(Object.fromEntries(questions.map((question) => [question.id, answerOf(question)]))))
          }
        >
          {busy || status === "deciding" ? <LoaderCircle /> : <Send />}回答
        </button>
      </footer>
    </article>
  );
}

// Hermes marks the choice it recommends by appending this to its text (clarify_tool.py
// mark_recommended); as in Hermes Desktop (store/clarify.ts bareChoice) the card shows it as
// a badge and answers with the bare choice.
const RECOMMENDED = /\s*\(Recommended\)$/;
const plainChoice = (choice: string) => choice.replace(RECOMMENDED, "");

function OperationRow({ operation }: { operation: ChangeOperation }) {
  const target = friendlyPath(operation.path);
  if (operation.op === "write") {
    const created = !operation.exists;
    return (
      <li>
        <div className="as-op-title">
          <FilePen />
          <span>{created ? "新建" : "修改"}</span>
          <code>{target}</code>
        </div>
        <Diff before={operation.before ?? ""} after={operation.after ?? ""} />
      </li>
    );
  }
  if (operation.op === "copy") {
    return (
      <li>
        <div className="as-op-title">
          <Copy />
          <span>{operation.exists ? "复制并替换" : "复制"}</span>
          <code>{friendlyPath(operation.from ?? "")}</code>
          <MoveRight />
          <code>{target}</code>
          {operation.size !== undefined && (
            <small>
              {operation.folder ? "文件夹，" : ""}
              {formatBytes(operation.size)}
            </small>
          )}
        </div>
        {operation.exists && <p className="as-op-note">原来的文件会移到回收站</p>}
      </li>
    );
  }
  const verb = operation.op === "delete" ? "删除（移入回收站）" : operation.op === "move" ? "移动" : "新建文件夹";
  const icon = operation.op === "delete" ? <Trash2 /> : operation.op === "move" ? <MoveRight /> : <FolderPlus />;
  return (
    <li>
      <div className="as-op-title">
        {icon}
        <span>{verb}</span>
        <code>{target}</code>
        {operation.to && (
          <>
            <MoveRight />
            <code>{friendlyPath(operation.to)}</code>
          </>
        )}
      </div>
    </li>
  );
}

function Diff({ before, after }: { before: string; after: string }) {
  const parts = useMemo(() => diffLines(before, after), [before, after]);
  return (
    <pre className="as-diff">
      {parts.map((part, index) => {
        const lines = part.value.replace(/\n$/, "").split("\n");
        const kind = part.added ? "add" : part.removed ? "del" : "same";
        const shown = kind === "same" && lines.length > 6 ? [...lines.slice(0, 2), "…", ...lines.slice(-2)] : lines;
        return shown.map((line, row) => (
          <span key={`${index}-${row}`} className={kind}>
            <i aria-hidden>{kind === "add" ? "+" : kind === "del" ? "−" : ""}</i>
            {line || " "}
          </span>
        ));
      })}
    </pre>
  );
}

// assistant-ui keeps its message state keyed on these, so they must not be
// recreated per render (Hermes Desktop hoists them the same way).
const MARKDOWN_COMPONENTS: StreamdownTextComponents = { a: MarkdownLink, img: MarkdownImage, CodeHeader };
const ASSISTANT_PARTS = { Text: MarkdownText, ChainOfThought: Steps, Empty: Thinking };
const STEP_PARTS = { Reasoning, tools: { Fallback: ToolActivity } };
const THREAD_COMPONENTS = { UserMessage, AssistantMessage, SystemMessage: SystemNotice };

// Sizes in 1024 units, as everywhere in A-NAS (docs/specs/size-units.md).

function short(text: string) {
  const line = text.split("\n")[0];
  return line.length > 60 ? `${line.slice(0, 60)}…` : line;
}

function friendlyPath(path: string) {
  return path.replace(/^\/nas\//, "").replace(/^助手\/待确认\//, "草稿/");
}

function relativeTime(seconds: number) {
  return formatRelativeTime(seconds * 1000, Date.now(), { dateAfterDays: 1 });
}
