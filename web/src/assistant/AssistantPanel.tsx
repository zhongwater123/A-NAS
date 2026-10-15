// AI 助手 window (ADR 0022 decision 9). The thread is assistant-ui — the chat
// library Hermes Desktop is built on — fed with Hermes Desktop's own transcript
// model; the conversation list, pending-change cards and styling are A-NAS's.
import {
  AssistantRuntimeProvider,
  ChainOfThoughtPrimitive,
  ComposerPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  useAuiState,
  useExternalStoreRuntime,
  type AppendMessage,
  type ThreadMessageLike,
  type ToolCallMessagePartComponent,
} from "@assistant-ui/react";
import { StreamdownTextPrimitive } from "@assistant-ui/react-streamdown";
import { diffLines } from "diff";
import {
  ArrowUp, Bot, Brain, Check, ChevronDown, ChevronRight, CircleAlert, FilePen, FileSearch, FileText, FolderPlus, History,
  ListChecks, LoaderCircle, MessageSquare, MessageSquarePlus, MoveRight, Pencil, Search, Send, ShieldCheck, Square, Trash2, Wrench, X,
} from "lucide-react";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  type AssistantSession, type ChangeOperation, type PendingChange,
  assistantStatus, createSession, decideChange, deleteSession, failureText, listChanges, listSessions, renameSession,
} from "./assistantApi";
import type { ChatMessage } from "./hermes/lib/chat-messages/types";
import { useAssistantThread } from "./useAssistantThread";
import "./assistant.css";

const SUGGESTIONS = [
  "总结一下共享空间里有哪些文件",
  "帮我在个人空间的 计划.md 里加一项：交电费",
  "我之前删掉过一个文件，帮我在回收站找找",
];

export function AssistantPanel({ username }: { username: string }) {
  const [sessions, setSessions] = useState<AssistantSession[]>([]);
  const [sessionsLoaded, setSessionsLoaded] = useState(false);
  const [activeId, setActiveId] = useState<string>();
  const [queued, setQueued] = useState<string>();
  const [changes, setChanges] = useState<PendingChange[]>([]);
  const [changesEnabled, setChangesEnabled] = useState(false);
  const [inboxOpen, setInboxOpen] = useState(false);
  const [notice, setNotice] = useState("");

  const refreshSessions = useCallback(() => listSessions()
    .then((list) => { setSessions(list); setSessionsLoaded(true); })
    .catch((reason) => setNotice(failureText(reason))), []);
  const refreshChanges = useCallback(() => {
    if (changesEnabled) listChanges().then(setChanges).catch(() => undefined);
  }, [changesEnabled]);

  useEffect(() => {
    assistantStatus().then((status) => setChangesEnabled(status.changes)).catch(() => undefined);
    void refreshSessions();
  }, [refreshSessions]);
  useEffect(refreshChanges, [refreshChanges]);

  const afterTurn = useCallback(() => {
    refreshChanges();
    void refreshSessions();
    // Hermes names a new conversation in the background after its first turn.
    window.setTimeout(() => void refreshSessions(), 4000);
  }, [refreshChanges, refreshSessions]);

  const thread = useAssistantThread(activeId, afterTurn);
  const { send, cancel } = thread;

  // A message typed before any conversation exists starts one, then is sent
  // once that conversation has loaded.
  useEffect(() => {
    if (queued && activeId && !thread.loading) {
      setQueued(undefined);
      void send(queued);
    }
  }, [queued, activeId, thread.loading, send]);

  const submit = useCallback(async (text: string) => {
    const input = text.trim();
    if (!input) return;
    if (activeId) return send(input);
    try {
      const session = await createSession();
      setSessions((list) => [session, ...list]);
      setActiveId(session.id);
      setQueued(input);
    } catch (reason) {
      setNotice(failureText(reason));
    }
  }, [activeId, send]);

  // assistant-ui re-reads its store whenever the adapter changes, so it only
  // changes with the messages or the running state.
  const visible = useMemo(() => thread.messages.filter((message) => !message.hidden), [thread.messages]);
  const running = thread.running || Boolean(queued);
  const adapter = useMemo(() => ({
    messages: visible,
    isRunning: running,
    convertMessage: toThreadMessage,
    onNew: async (message: AppendMessage) => submit(message.content.map((part) => (part.type === "text" ? part.text : "")).join("")),
    onCancel: async () => cancel(),
  }), [visible, running, submit, cancel]);
  const runtime = useExternalStoreRuntime<ChatMessage>(adapter);

  const remove = async (session: AssistantSession) => {
    if (!window.confirm(`删除对话“${session.title || "新对话"}”？删除后无法恢复。`)) return;
    await deleteSession(session.id).catch((reason) => setNotice(failureText(reason)));
    if (session.id === activeId) setActiveId(undefined);
    void refreshSessions();
  };

  const rename = async (session: AssistantSession, title: string) => {
    const updated = await renameSession(session.id, title).catch((reason) => { setNotice(failureText(reason)); return undefined; });
    if (updated) setSessions((list) => list.map((item) => (item.id === updated.id ? { ...item, title: updated.title } : item)));
  };

  const decide = async (change: PendingChange, confirm: boolean) => {
    try {
      const decided = await decideChange(change.id, confirm);
      setChanges((list) => list.map((item) => (item.id === decided.id ? decided : item)));
    } catch (reason) {
      setNotice(failureText(reason));
      refreshChanges();
    }
  };

  const openSession = (id: string | undefined) => {
    setActiveId(id);
    setInboxOpen(false);
  };
  const closeInbox = useCallback(() => setInboxOpen(false), []);

  // A change belongs to the conversation it was submitted in and shows only
  // there; the header lists every pending change of the member.
  const active = sessions.find((session) => session.id === activeId);
  const pending = changes.filter((change) => change.status === "pending");
  const waiting = new Map<string, number>();
  for (const change of pending) if (change.sessionId) waiting.set(change.sessionId, (waiting.get(change.sessionId) ?? 0) + 1);
  const here = activeId ? changes.filter((change) => change.sessionId === activeId) : [];
  const herePending = here.filter((change) => change.status === "pending");
  const hereRecent = here.filter((change) => change.status !== "pending" && change.decidedAt && Date.now() / 1000 - change.decidedAt < 600);

  return (
    <div className="as-app">
      <aside className="as-side">
        <div className="as-brand">
          <span className="as-brand-mark"><Bot /></span>
          <div><strong>AI 助手</strong><small>{username} 的助手</small></div>
        </div>
        <button type="button" className="as-new" onClick={() => openSession(undefined)}><MessageSquarePlus />新对话</button>
        <div className="as-side-label"><History />对话</div>
        <ul className="as-sessions">
          {sessions.map((session) => (
            <SessionRow key={session.id} session={session} active={session.id === activeId} waiting={waiting.get(session.id) ?? 0}
              onOpen={() => openSession(session.id)} onRename={(title) => void rename(session, title)} onDelete={() => void remove(session)} />
          ))}
          {!sessions.length && <li className="as-sessions-empty">{sessionsLoaded ? "还没有对话" : <><LoaderCircle />正在启动助手…</>}</li>}
        </ul>
        <div className="as-side-foot"><ShieldCheck /><span>助手只能读你的个人空间和共享空间；改动文件都要你确认。</span></div>
      </aside>

      <main className="as-main">
        <header className="as-head">
          <h2>{active?.title || (activeId ? "新对话" : "开始一个对话")}</h2>
          {thread.running && <span className="as-working"><LoaderCircle />正在处理</span>}
          {pending.length > 0 && (
            <div className="as-inbox-anchor">
              <button type="button" className="as-pending-pill" aria-expanded={inboxOpen} aria-haspopup="dialog"
                onClick={() => { setInboxOpen((open) => !open); refreshChanges(); }}>
                {pending.length} 项待确认<ChevronDown />
              </button>
              {inboxOpen && (
                <Inbox changes={pending} sessions={sessions} activeId={activeId} onOpen={openSession} onClose={closeInbox}
                  onDecide={(change, confirm) => decide(change, confirm)} />
              )}
            </div>
          )}
        </header>

        <AssistantRuntimeProvider runtime={runtime}>
          <ThreadPrimitive.Root className="as-thread">
            <ThreadPrimitive.Viewport className="as-viewport" autoScroll={Boolean(activeId)}>
              {!activeId && !queued ? (
                <Welcome username={username} onPick={(text) => void submit(text)} />
              ) : thread.loading && !thread.messages.length ? (
                <div className="as-loading"><LoaderCircle />正在打开对话…</div>
              ) : (
                <ThreadPrimitive.Messages components={THREAD_COMPONENTS} />
              )}
              {(thread.error || notice) && (
                <div className="as-error" role="alert"><CircleAlert /><span>{thread.error || notice}</span>{notice && <button type="button" aria-label="关闭" onClick={() => setNotice("")}><X /></button>}</div>
              )}
              <div className="as-viewport-spacer" />
            </ThreadPrimitive.Viewport>

            {(herePending.length > 0 || hereRecent.length > 0) && (
              <section className="as-changes" aria-label="本对话中待确认的更改">
                {herePending.map((change) => <ChangeCard key={change.id} change={change} onDecide={(confirm) => decide(change, confirm)} />)}
                {hereRecent.map((change) => <ChangeCard key={change.id} change={change} />)}
              </section>
            )}

            <ComposerPrimitive.Root className="as-composer">
              <ComposerPrimitive.Input className="as-input" placeholder="让助手帮你找文件、整理内容，或修改你的文件…" rows={1} autoFocus submitOnEnter />
              {running
                ? <ComposerPrimitive.Cancel className="as-send stop" aria-label="停止"><Square /></ComposerPrimitive.Cancel>
                : <ComposerPrimitive.Send className="as-send" aria-label="发送"><ArrowUp /></ComposerPrimitive.Send>}
            </ComposerPrimitive.Root>
          </ThreadPrimitive.Root>
        </AssistantRuntimeProvider>
      </main>
    </div>
  );
}

function toThreadMessage(message: ChatMessage): ThreadMessageLike {
  const createdAt = message.timestamp ? new Date(message.timestamp * 1000) : undefined;
  if (message.role === "user") return { id: message.id, role: "user", content: message.parts, createdAt };
  return {
    id: message.id,
    role: message.role === "system" ? "system" : "assistant",
    content: message.parts,
    createdAt,
    status: message.pending ? { type: "running" }
      : message.interrupted ? { type: "incomplete", reason: "cancelled" }
      : message.error ? { type: "incomplete", reason: "error" }
      : { type: "complete", reason: "stop" },
  } as ThreadMessageLike;
}

function SessionRow({ session, active, waiting, onOpen, onRename, onDelete }: { session: AssistantSession; active: boolean; waiting: number; onOpen(): void; onRename(title: string): void; onDelete(): void }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(session.title);
  const finish = () => {
    setEditing(false);
    if (draft.trim() && draft.trim() !== session.title) onRename(draft.trim());
  };
  return (
    <li className={`as-session${active ? " active" : ""}`}>
      {editing ? (
        <input className="as-session-edit" value={draft} autoFocus maxLength={100} onChange={(event) => setDraft(event.target.value)} onBlur={finish}
          onKeyDown={(event) => { if (event.key === "Enter") finish(); if (event.key === "Escape") { setDraft(session.title); setEditing(false); } }} />
      ) : (
        <button type="button" className="as-session-open" onClick={onOpen}>
          <span className="as-session-line">
            <strong>{session.title || session.preview || "新对话"}</strong>
            {waiting > 0 && <span className="as-session-badge" title={`${waiting} 项更改待确认`}>{waiting}</span>}
          </span>
          <small>{relativeTime(session.lastActive ?? session.startedAt)}{waiting > 0 && <em> · 待确认</em>}</small>
        </button>
      )}
      {!editing && (
        <span className="as-session-actions">
          <button type="button" aria-label="改名" onClick={() => { setDraft(session.title); setEditing(true); }}><Pencil /></button>
          <button type="button" aria-label="删除" onClick={onDelete}><Trash2 /></button>
        </span>
      )}
    </li>
  );
}

function Welcome({ username, onPick }: { username: string; onPick(text: string): void }) {
  return (
    <div className="as-welcome">
      <span className="as-welcome-mark"><Bot /></span>
      <h3>你好，{username}</h3>
      <p>我能读你的个人空间和共享空间，帮你查找、总结和整理文件。<br />要改动文件时，我会先把改动交给你确认。</p>
      <div className="as-suggestions">
        {SUGGESTIONS.map((text) => <button type="button" key={text} onClick={() => onPick(text)}>{text}<ChevronRight /></button>)}
      </div>
    </div>
  );
}

function UserMessage() {
  return (
    <MessagePrimitive.Root className="as-msg user">
      <div className="as-bubble"><MessagePrimitive.Parts /></div>
    </MessagePrimitive.Root>
  );
}

function AssistantMessage() {
  return (
    <MessagePrimitive.Root className="as-msg assistant">
      <span className="as-avatar"><Bot /></span>
      <div className="as-answer">
        <MessagePrimitive.Parts components={ASSISTANT_PARTS} />
        <Outcome />
      </div>
    </MessagePrimitive.Root>
  );
}

// How a reply that did not finish ended: stopped by the member, or failed.
function Outcome() {
  const reason = useAuiState((state) => (state.message.status?.type === "incomplete" ? state.message.status.reason : undefined));
  if (reason === "cancelled") return <p className="as-outcome stopped"><Square />已停止</p>;
  if (reason === "error") return <p className="as-outcome failed"><CircleAlert />这次回答没有完成</p>;
  return null;
}

const MarkdownText = () => <StreamdownTextPrimitive className="as-markdown" />;

// One folded row per run of reasoning and tool calls, like Hermes Desktop's
// run summary: the member sees what the assistant did without the noise.
function Steps() {
  const count = useAuiState((state) => state.chainOfThought.parts.length);
  const collapsed = useAuiState((state) => state.chainOfThought.collapsed);
  const running = useAuiState((state) => state.chainOfThought.status.type === "running");
  const latest = useAuiState((state) => state.chainOfThought.parts.at(-1));
  const tools = useAuiState((state) => state.chainOfThought.parts.filter((part) => part.type === "tool-call").length);
  const current = latest?.type === "tool-call" ? describeTool(latest.toolName, (latest.args ?? {}) as Record<string, unknown>) : undefined;
  return (
    <ChainOfThoughtPrimitive.Root className={`as-steps${collapsed ? "" : " open"}${running ? " running" : ""}`}>
      <ChainOfThoughtPrimitive.AccordionTrigger className="as-steps-trigger">
        {running ? <LoaderCircle className="spin" /> : <ListChecks />}
        <span>{running ? "正在处理" : "处理过程"}</span>
        <small>{running && current ? `${current.label}${current.detail ? ` ${current.detail}` : ""}` : tools ? `${tools} 个动作` : `${count} 步`}</small>
        <ChevronRight className="chevron" />
      </ChainOfThoughtPrimitive.AccordionTrigger>
      {!collapsed && <div className="as-steps-body"><ChainOfThoughtPrimitive.Parts components={STEP_PARTS} /></div>}
    </ChainOfThoughtPrimitive.Root>
  );
}

// assistant-ui also renders Empty for a reply that ended without text; the
// dots only mean something while the reply is still coming.
function Thinking() {
  const running = useAuiState((state) => state.message.status?.type === "running");
  return running ? <div className="as-thinking"><span /><span /><span /></div> : null;
}

function Reasoning({ text }: { text: string }) {
  if (!text.trim()) return null;
  return (
    <details className="as-reasoning">
      <summary><Brain />思考过程</summary>
      <p>{text}</p>
    </details>
  );
}

// A friendlier name, with an icon, for each tool the assistant has.
function describeTool(name: string, args: Record<string, unknown>): { icon: ReactNode; label: string; detail: string } {
  const path = typeof args.path === "string" ? friendlyPath(args.path) : "";
  switch (name) {
    case "read_file": return { icon: <FileText />, label: "读取", detail: path };
    case "search_files": return { icon: <Search />, label: "搜索", detail: [typeof args.pattern === "string" ? `“${args.pattern}”` : "", path && `在 ${path}`].filter(Boolean).join(" ") };
    case "write_file": return { icon: <FilePen />, label: "写入草稿", detail: path };
    case "patch": return { icon: <FilePen />, label: "修改草稿", detail: path };
    case "mcp__anas__submit_changes": return { icon: <Send />, label: "提交更改", detail: typeof args.summary === "string" ? args.summary : "等你确认" };
    case "memory": return { icon: <Brain />, label: "记住", detail: "" };
    case "session_search": return { icon: <FileSearch />, label: "查找以前的对话", detail: "" };
    case "skill_view": case "skills_list": case "skill_manage": return { icon: <Wrench />, label: "查看技能", detail: typeof args.name === "string" ? args.name : "" };
    default: return { icon: <Wrench />, label: name, detail: "" };
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

// Every pending change of the member, from whichever conversation; each can
// be decided here or opened in its conversation.
function Inbox({ changes, sessions, activeId, onOpen, onClose, onDecide }: {
  changes: PendingChange[]; sessions: AssistantSession[]; activeId?: string;
  onOpen(id: string): void; onClose(): void; onDecide(change: PendingChange, confirm: boolean): Promise<void>;
}) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const key = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    // The pill toggles the panel itself, so a press on it is not "outside".
    const press = (event: PointerEvent) => { if (!panel.current?.parentElement?.contains(event.target as Node)) onClose(); };
    document.addEventListener("keydown", key);
    document.addEventListener("pointerdown", press);
    return () => {
      document.removeEventListener("keydown", key);
      document.removeEventListener("pointerdown", press);
    };
  }, [onClose]);
  return (
    <div className="as-inbox" role="dialog" aria-label="待确认的更改" ref={panel}>
      <header><strong>待确认的更改</strong><small>确认前文件不会变化</small></header>
      <div className="as-inbox-list">
        {changes.map((change) => {
          const session = sessions.find((item) => item.id === change.sessionId);
          return (
            <section key={change.id}>
              <div className="as-inbox-source">
                <MessageSquare />
                {change.sessionId === undefined ? <span>来源对话未知</span>
                  : change.sessionId === activeId ? <span>来自当前对话</span>
                  : session ? (
                    <>
                      <span>来自“{session.title || session.preview || "新对话"}”</span>
                      <button type="button" onClick={() => onOpen(session.id)}>打开对话<ChevronRight /></button>
                    </>
                  ) : <span>来自一个已删除的对话</span>}
              </div>
              <ChangeCard change={change} onDecide={(confirm) => onDecide(change, confirm)} />
            </section>
          );
        })}
      </div>
    </div>
  );
}

function ChangeCard({ change, onDecide }: { change: PendingChange; onDecide?(confirm: boolean): Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const choose = (confirm: boolean) => {
    setBusy(true);
    void onDecide?.(confirm).finally(() => setBusy(false));
  };
  const decided = change.status !== "pending";
  return (
    <article className={`as-change ${change.status}${!decided && stale(change) ? " stale" : ""}`}>
      <header>
        <span className="as-change-mark">{change.status === "applied" ? <Check /> : change.status === "rejected" ? <X /> : change.status === "failed" ? <CircleAlert /> : <ShieldCheck />}</span>
        <div>
          <strong>{change.summary || "助手提交的更改"}</strong>
          <small>{statusText(change)}</small>
        </div>
      </header>
      {!decided && <ul className="as-change-ops">{change.operations.map((operation, index) => <OperationRow key={index} operation={operation} />)}</ul>}
      {!decided && onDecide && (
        <footer>
          <button type="button" className="as-reject" disabled={busy} onClick={() => choose(false)}>拒绝</button>
          <button type="button" className="as-confirm" disabled={busy || stale(change)} onClick={() => choose(true)}>{busy ? <LoaderCircle /> : <Check />}确认更改</button>
        </footer>
      )}
    </article>
  );
}

function statusText(change: PendingChange) {
  switch (change.status) {
    case "applied": return "已按你的确认完成；被替换或删除的旧文件在回收站";
    case "rejected": return "你拒绝了这项更改，文件没有变化";
    case "failed": return `没有完成：${change.error ?? "未知原因"}`;
    default: return stale(change) ? "内容已变化，请让助手重新提交" : `${change.operations.length} 项操作 · 确认前文件不会变化`;
  }
}

// An operation the store can no longer preview, as when a later draft replaced
// the submitted one; confirming it would be refused.
const stale = (change: PendingChange) => change.operations.some((operation) => operation.problem);

function OperationRow({ operation }: { operation: ChangeOperation }) {
  const target = friendlyPath(operation.path);
  if (operation.op === "write") {
    const created = !operation.exists;
    return (
      <li>
        <div className="as-op-title"><FilePen /><span>{created ? "新建" : "修改"}</span><code>{target}</code></div>
        {operation.problem
          ? <p className="as-op-problem"><CircleAlert />{operation.problem}，确认会被拒绝</p>
          : <Diff before={operation.before ?? ""} after={operation.after ?? ""} />}
      </li>
    );
  }
  const verb = operation.op === "delete" ? "删除（移入回收站）" : operation.op === "move" ? "移动" : "新建文件夹";
  const icon = operation.op === "delete" ? <Trash2 /> : operation.op === "move" ? <MoveRight /> : <FolderPlus />;
  return (
    <li><div className="as-op-title">{icon}<span>{verb}</span><code>{target}</code>{operation.to && <><MoveRight /><code>{friendlyPath(operation.to)}</code></>}</div></li>
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
        return shown.map((line, row) => <span key={`${index}-${row}`} className={kind}><i aria-hidden>{kind === "add" ? "+" : kind === "del" ? "−" : ""}</i>{line || " "}</span>);
      })}
    </pre>
  );
}

// assistant-ui keeps its message state keyed on these, so they must not be
// recreated per render (Hermes Desktop hoists them the same way).
const ASSISTANT_PARTS = { Text: MarkdownText, ChainOfThought: Steps, Empty: Thinking };
const STEP_PARTS = { Reasoning, tools: { Fallback: ToolActivity } };
const THREAD_COMPONENTS = { UserMessage, AssistantMessage, SystemMessage: AssistantMessage };

function friendlyPath(path: string) {
  return path.replace(/^\/nas\//, "").replace(/^助手\/待确认\//, "草稿/");
}

function relativeTime(seconds: number) {
  const elapsed = Date.now() / 1000 - seconds;
  if (elapsed < 60) return "刚刚";
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)} 分钟前`;
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)} 小时前`;
  const date = new Date(seconds * 1000);
  return `${date.getMonth() + 1}月${date.getDate()}日`;
}
