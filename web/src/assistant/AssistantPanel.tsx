// AI 助手 window (ADR 0024 decision 9). The thread is assistant-ui — the chat
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
  type ThreadMessageLike,
  type ToolCallMessagePartComponent,
} from "@assistant-ui/react";
import { StreamdownTextPrimitive } from "@assistant-ui/react-streamdown";
import { diffLines } from "diff";
import {
  ArrowUp, Brain, Check, ChevronRight, CircleAlert, Clock, FilePen, FileSearch, FileText, FolderPlus, History,
  Image as ImageIcon, ListChecks, LoaderCircle, MessageSquarePlus, MoveRight, Pencil, Search, Send, ShieldCheck, Square, Trash2, Wrench, X,
} from "lucide-react";
import { type ComponentPropsWithoutRef, type ReactNode, createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";

import { AssistantGlyph } from "../AssistantGlyph";
import {
  type Approval, type AssistantSession, type ChangeOperation,
  decideApproval, deleteSession, failureText, listApprovals, listSessions, renameSession,
} from "./assistantApi";
import type { ChatMessage } from "./hermes/lib/chat-messages/types";
import type { SessionActiveListResult } from "./hermes/shared/gateway-contract.generated";
import { MemoryView } from "./MemoryView";
import { linkPhotos, photoOf } from "./photoLinks";
import { useAssistantGateway } from "./useAssistantGateway";
import { useAssistantThread } from "./useAssistantThread";
import "./assistant.css";

const SUGGESTIONS = [
  "总结一下共享空间里有哪些文件",
  "帮我在个人空间的 计划.md 里加一项：交电费",
  "我之前删掉过一个文件，帮我在回收站找找",
];

// Opens a photo the assistant named in the photo app, from the desktop.
const PhotoOpener = createContext<((assetId: string) => void) | undefined>(undefined);

export function AssistantPanel({ username, onShowPhoto }: { username: string; onShowPhoto?(assetId: string): void }) {
  const gateway = useAssistantGateway();
  const [sessions, setSessions] = useState<AssistantSession[]>([]);
  const [sessionsLoaded, setSessionsLoaded] = useState(false);
  const [activeId, setActiveId] = useState<string>();
  // The window shows a conversation, or what the assistant remembers.
  const [view, setView] = useState<"chat" | "memory">("chat");
  const [approvals, setApprovals] = useState<Map<string, Approval>>(new Map());
  // Stored conversations with a turn running in the instance, wherever it was started.
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const [notice, setNotice] = useState("");

  // A list that failed while the instance was still starting says so until a
  // later load succeeds.
  const listFailure = useRef("");
  const refreshSessions = useCallback(() => listSessions()
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
    }), []);
  const refreshBusy = useCallback(() => {
    if (!gateway.ready) return;
    gateway.client.request<SessionActiveListResult>("session.active_list")
      .then((result) => setBusy(new Set(result.sessions.filter((item) => item.status !== "idle").map((item) => item.session_key))))
      .catch(() => undefined);
  }, [gateway.client, gateway.ready]);
  const keep = useCallback((approval: Approval) => setApprovals((all) => new Map(all).set(approval.requestId, approval)), []);
  const reloadApprovals = useCallback(() => {
    listApprovals().then((list) => setApprovals(new Map(list.map((approval) => [approval.requestId, approval])))).catch(() => undefined);
  }, []);

  useEffect(() => { void refreshSessions(); }, [refreshSessions]);
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
  const known = useRef(new Set<string>());
  known.current = new Set(sessions.map((session) => session.id));
  useEffect(() => gateway.client.onEvent((event) => {
    const type = event.type as string;
    if (type === "anas.approval" && event.payload) {
      const approval = event.payload as unknown as Approval;
      keep(approval);
      if (approval.storedSessionId && !known.current.has(approval.storedSessionId)) void refreshSessions();
    }
    if (type === "sessions.changed" || type === "session.title") void refreshSessions();
    if (type === "sessions.changed" || type === "message.complete") refreshBusy();
  }), [gateway.client, keep, refreshBusy, refreshSessions]);

  const changed = useCallback(() => {
    void refreshSessions();
    refreshBusy();
    // Hermes names a new conversation in the background after its first turn.
    window.setTimeout(() => void refreshSessions(), 4000);
  }, [refreshSessions, refreshBusy]);
  const thread = useAssistantThread(gateway, activeId, { onCreated: setActiveId, onChanged: changed });
  const { send, cancel } = thread;

  // assistant-ui re-reads its store whenever the adapter changes, so it only
  // changes with the messages or the running state.
  const visible = useMemo(() => thread.messages.filter((message) => !message.hidden), [thread.messages]);
  const adapter = useMemo(() => ({
    messages: visible,
    isRunning: thread.running,
    convertMessage: toThreadMessage,
    onNew: async (message: AppendMessage) => send(message.content.map((part) => (part.type === "text" ? part.text : "")).join("")),
    onCancel: async () => cancel(),
  }), [visible, thread.running, send, cancel]);
  const runtime = useExternalStoreRuntime<ChatMessage>(adapter);

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
    const updated = await renameSession(session.id, title).catch((reason) => { setNotice(failureText(reason)); return undefined; });
    if (updated) setSessions((list) => list.map((item) => (item.id === updated.id ? { ...item, title: updated.title } : item)));
  };

  const decide = async (approval: Approval, confirm: boolean) => {
    try {
      keep(await decideApproval(approval.requestId, confirm));
    } catch (reason) {
      setNotice(failureText(reason));
      reloadApprovals();
    }
  };

  // A card belongs to the conversation it was asked in and shows only there; the
  // list marks the conversations waiting for an answer.
  const active = sessions.find((session) => session.id === activeId);
  const waiting = new Map<string, number>();
  for (const approval of approvals.values()) {
    if (approval.status === "awaiting" && approval.storedSessionId) waiting.set(approval.storedSessionId, (waiting.get(approval.storedSessionId) ?? 0) + 1);
  }
  const here = [...approvals.values()]
    .filter((approval) => (activeId && approval.storedSessionId === activeId) || (thread.liveId && approval.sessionId === thread.liveId))
    .filter((approval) => approval.status === "awaiting" || approval.status === "deciding" || Date.now() / 1000 - approval.createdAt < 600)
    .sort((a, b) => a.createdAt - b.createdAt);
  const unreachable = !gateway.ready && gateway.problem;

  return (
    <PhotoOpener.Provider value={onShowPhoto}>
    <div className="as-app">
      <aside className="as-side">
        <div className="as-brand">
          <span className="as-brand-mark"><AssistantGlyph /></span>
          <div><strong>AI 助手</strong><small>{username} 的助手</small></div>
        </div>
        <button type="button" className="as-new" onClick={() => { setActiveId(undefined); setView("chat"); }}><MessageSquarePlus />新对话</button>
        <button type="button" className={`as-nav${view === "memory" ? " active" : ""}`} aria-pressed={view === "memory"} onClick={() => setView("memory")}>
          <Brain />记忆<small>助手记住的内容</small>
        </button>
        <div className="as-side-label"><History />对话</div>
        <ul className="as-sessions">
          {sessions.map((session) => (
            <SessionRow key={session.id} session={session} active={session.id === activeId} waiting={waiting.get(session.id) ?? 0}
              running={busy.has(session.id)}
              onOpen={() => { setActiveId(session.id); setView("chat"); }} onRename={(title) => void rename(session, title)} onDelete={() => void remove(session)} />
          ))}
          {!sessions.length && <li className="as-sessions-empty">{sessionsLoaded ? "还没有对话" : <><LoaderCircle />正在启动助手…</>}</li>}
        </ul>
        <div className="as-side-foot"><ShieldCheck /><span>助手只能读你的个人空间和共享空间；改动文件都要你确认。</span></div>
      </aside>

      <main className="as-main">
        <header className="as-head">
          <h2>{view === "memory" ? "记忆" : active?.title || (activeId ? "新对话" : "开始一个对话")}</h2>
          {thread.running && <span className="as-working"><LoaderCircle />正在处理</span>}
          {!gateway.ready && !unreachable && <span className="as-working"><LoaderCircle />正在连接助手</span>}
        </header>

        {view === "memory" && <MemoryView onConversationsDeleted={() => { setActiveId(undefined); void refreshSessions(); }} />}
        <AssistantRuntimeProvider runtime={runtime}>
          <ThreadPrimitive.Root className="as-thread" hidden={view !== "chat"}>
            <ThreadPrimitive.Viewport className="as-viewport" autoScroll={Boolean(activeId)}>
              {!activeId && !thread.messages.length ? (
                <Welcome username={username} onPick={(text) => void send(text)} />
              ) : thread.loading && !thread.messages.length ? (
                <div className="as-loading"><LoaderCircle />正在打开对话…</div>
              ) : (
                <ThreadPrimitive.Messages components={THREAD_COMPONENTS} />
              )}
              {(thread.error || notice || unreachable) && (
                <div className="as-error" role="alert">
                  <CircleAlert /><span>{thread.error || notice || unreachable}</span>
                  {notice && <button type="button" aria-label="关闭" onClick={() => setNotice("")}><X /></button>}
                </div>
              )}
              <div className="as-viewport-spacer" />
            </ThreadPrimitive.Viewport>

            {here.length > 0 && (
              <section className="as-changes" aria-label="本对话中等你确认的更改">
                {here.map((approval) => (
                  <ApprovalCard key={approval.requestId} approval={approval} onDecide={(confirm) => decide(approval, confirm)} />
                ))}
              </section>
            )}

            <ComposerPrimitive.Root className="as-composer">
              <ComposerPrimitive.Input className="as-input" placeholder="让助手帮你找文件、整理内容，或修改你的文件…" rows={1} autoFocus submitOnEnter />
              {thread.running
                ? <ComposerPrimitive.Cancel className="as-send stop" aria-label="停止"><Square /></ComposerPrimitive.Cancel>
                : <ComposerPrimitive.Send className="as-send" aria-label="发送"><ArrowUp /></ComposerPrimitive.Send>}
            </ComposerPrimitive.Root>
          </ThreadPrimitive.Root>
        </AssistantRuntimeProvider>
      </main>
    </div>
    </PhotoOpener.Provider>
  );
}

function toThreadMessage(message: ChatMessage): ThreadMessageLike {
  const createdAt = message.timestamp ? new Date(message.timestamp * 1000) : undefined;
  if (message.role === "user") return { id: message.id, role: "user", content: message.parts, createdAt };
  return {
    id: message.id,
    role: message.role === "system" ? "system" : "assistant",
    content: message.parts.map((part) => (part.type === "text" ? { ...part, text: linkPhotos(part.text) } : part)),
    createdAt,
    status: message.pending ? { type: "running" }
      : message.interrupted ? { type: "incomplete", reason: "cancelled" }
      : message.error ? { type: "incomplete", reason: "error" }
      : { type: "complete", reason: "stop" },
  } as ThreadMessageLike;
}

function SessionRow({ session, active, waiting, running, onOpen, onRename, onDelete }: {
  session: AssistantSession; active: boolean; waiting: number; running: boolean; onOpen(): void; onRename(title: string): void; onDelete(): void;
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
        <input className="as-session-edit" value={draft} autoFocus maxLength={100} onChange={(event) => setDraft(event.target.value)} onBlur={finish}
          onKeyDown={(event) => { if (event.key === "Enter") finish(); if (event.key === "Escape") { setDraft(session.title); setEditing(false); } }} />
      ) : (
        <button type="button" className="as-session-open" onClick={onOpen}>
          <span className="as-session-line">
            <strong>{session.title || session.preview || "新对话"}</strong>
            {waiting > 0 ? <span className="as-session-badge" title={`${waiting} 项更改等你确认`}>{waiting}</span>
              : running && <LoaderCircle className="as-session-running" aria-label="正在回答" />}
          </span>
          <small>{relativeTime(session.lastActive ?? session.startedAt)}{waiting > 0 ? <em> · 等你确认</em> : running && " · 正在回答"}</small>
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
      <span className="as-welcome-mark"><AssistantGlyph /></span>
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
      <span className="as-avatar"><AssistantGlyph /></span>
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

const MarkdownText = () => <StreamdownTextPrimitive className="as-markdown" components={MARKDOWN_COMPONENTS} />;

// A photo the assistant names shows as its thumbnail and opens in the photo
// app; other links open in a new tab.
function MarkdownLink({ href, children, node: _node, ...rest }: ComponentPropsWithoutRef<"a"> & { node?: unknown }) {
  const open = useContext(PhotoOpener);
  const [broken, setBroken] = useState(false);
  const photo = photoOf(href);
  if (!photo) return <a href={href} target="_blank" rel="noreferrer" {...rest}>{children}</a>;
  return (
    <button type="button" className="as-photo" title="在相册中打开" disabled={!open} onClick={() => open?.(photo)}>
      {broken ? <span className="as-photo-none"><ImageIcon /></span>
        : <img src={`/api/v1/photos/assets/${encodeURIComponent(photo)}/thumbnail`} alt="" loading="lazy" onError={() => setBroken(true)} />}
      <span>{children}</span>
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

function ApprovalCard({ approval, onDecide }: { approval: Approval; onDecide(confirm: boolean): Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const choose = (confirm: boolean) => {
    setBusy(true);
    void onDecide(confirm).finally(() => setBusy(false));
  };
  const { change, status } = approval;
  const open = status === "awaiting" || status === "deciding";
  return (
    <article className={`as-change ${status}`}>
      <header>
        <span className="as-change-mark">{CARD_MARKS[status]}</span>
        <div>
          <strong>{change.summary || "助手提交的更改"}</strong>
          <small>{statusText(approval)}</small>
        </div>
      </header>
      {open && <ul className="as-change-ops">{change.operations.map((operation, index) => <OperationRow key={index} operation={operation} />)}</ul>}
      {open && (
        <footer>
          <button type="button" className="as-reject" disabled={busy || status !== "awaiting"} onClick={() => choose(false)}>拒绝</button>
          <button type="button" className="as-confirm" disabled={busy || status !== "awaiting"} onClick={() => choose(true)}>
            {busy || status === "deciding" ? <LoaderCircle /> : <Check />}确认更改
          </button>
        </footer>
      )}
    </article>
  );
}

const CARD_MARKS: Record<Approval["status"], ReactNode> = {
  awaiting: <ShieldCheck />, deciding: <ShieldCheck />, approved: <LoaderCircle />, applied: <Check />,
  failed: <CircleAlert />, rejected: <X />, expired: <Clock />, stopped: <Square />,
};

function statusText({ status, change }: Approval) {
  switch (status) {
    case "approved": return "已确认，正在执行";
    case "applied": return "已按你的确认完成；被替换或删除的旧文件在回收站";
    case "failed": return `没有完成：${change.error ?? "未知原因"}`;
    case "rejected": return "你拒绝了这项更改，文件没有变化";
    case "expired": return "等待确认超时，文件没有变化";
    case "stopped": return "这一轮已停止，文件没有变化";
    default: return `${change.operations.length} 项操作 · 确认前文件不会变化`;
  }
}

function OperationRow({ operation }: { operation: ChangeOperation }) {
  const target = friendlyPath(operation.path);
  if (operation.op === "write") {
    const created = !operation.exists;
    return (
      <li>
        <div className="as-op-title"><FilePen /><span>{created ? "新建" : "修改"}</span><code>{target}</code></div>
        <Diff before={operation.before ?? ""} after={operation.after ?? ""} />
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
const MARKDOWN_COMPONENTS = { a: MarkdownLink };
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
