import {
  JsonRpcGatewayClient,
  type GatewayClientOptions,
} from "../../../web/src/assistant/hermes/shared/json-rpc-gateway";
import type {
  SessionCreateResult,
  SessionResumeResult,
} from "../../../web/src/assistant/hermes/shared/gateway-contract.generated";
import {
  NasClient,
  unwrap,
  errorText,
  type AssistantApproval,
  type AssistantSession,
} from "../nas/client";

export type Message = { id: string; role: "user" | "assistant"; text: string };
export interface ChatState {
  ready: boolean;
  busy: boolean;
  loading: boolean;
  error: string;
  activity: string;
  storedId?: string;
  liveId?: string;
  messages: Message[];
  history: AssistantSession[];
  approvals: AssistantApproval[];
}
export function messageText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .flatMap((part) =>
      part && typeof part === "object" && typeof part.text === "string"
        ? [part.text]
        : [],
    )
    .join("\n");
}

/** One member's connection; leaving a screen never re-submits a prompt. */
export class ChatController {
  private state: ChatState = {
    ready: false,
    busy: false,
    loading: false,
    error: "",
    activity: "",
    messages: [],
    history: [],
    approvals: [],
  };
  private listeners = new Set<() => void>();
  private gateway: JsonRpcGatewayClient;
  private disposed = false;
  private active = true;
  private epoch = 0;
  private reconnect?: ReturnType<typeof setTimeout>;
  private poll?: ReturnType<typeof setInterval>;
  private tries = 0;
  private replyId = "";
  constructor(
    private nas: NasClient,
    socketFactory: GatewayClientOptions["socketFactory"],
  ) {
    this.gateway = new JsonRpcGatewayClient({ socketFactory, replay: true });
    this.gateway.on("gateway.ready", () => {
      this.tries = 0;
      this.set({ ready: true, error: "" });
      void this.refreshHistory();
      if (this.state.storedId) void this.open(this.state.storedId);
    });
    this.gateway.onState((state) => {
      if (state !== "closed" && state !== "error") return;
      this.set({
        ready: false,
        activity: "",
        error: this.active ? "连接已断开，正在重新连接。" : "",
      });
      if (!this.disposed && this.active && !this.reconnect)
        this.reconnect = setTimeout(
          () => {
            this.reconnect = undefined;
            void this.start();
          },
          Math.min(15_000, 1000 * 2 ** this.tries++),
        );
    });
    this.gateway.onEvent((event) => {
      if (event.session_id !== this.state.liveId) return;
      const p = (event.payload ?? {}) as Record<string, unknown>;
      if (event.type === "message.start")
        this.set({ busy: true, activity: "正在思考" });
      if (event.type === "message.delta" && typeof p.text === "string") {
        if (!this.replyId) this.replyId = `reply-${Date.now()}`;
        const found = this.state.messages.some((m) => m.id === this.replyId);
        const messages = found
          ? this.state.messages.map((m) =>
              m.id === this.replyId ? { ...m, text: m.text + p.text } : m,
            )
          : [
              ...this.state.messages,
              { id: this.replyId, role: "assistant" as const, text: p.text },
            ];
        this.set({ messages, busy: true, activity: "正在回复" });
      }
      if (event.type === "tool.start")
        this.set({ activity: "正在处理你的请求" });
      if (event.type === "message.complete") {
        this.replyId = "";
        this.set({
          busy: false,
          activity: "",
          error:
            p.status === "error"
              ? "VNAS 没能完成这次回答，请查看对话后重试。"
              : "",
        });
        if (this.state.storedId)
          void this.loadMessages(this.state.storedId, this.epoch);
        void this.refreshHistory();
        void this.refreshApprovals();
      }
    });
  }
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  snapshot = () => this.state;
  private set(patch: Partial<ChatState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
  }
  async start() {
    if (!this.active || this.disposed) return;
    try {
      const status = await unwrap(this.nas.api.GET("/api/v1/assistant/status"));
      if (!status.available) {
        this.set({ error: "这个账号的 VNAS 尚未启用。传输功能仍然可以使用。" });
        return;
      }
      if (this.disposed || !this.active) return;
      await this.gateway.connect(this.nas.websocketURL);
      if (!this.poll)
        this.poll = setInterval(() => {
          if (this.state.ready) void this.refreshApprovals();
        }, 2500);
    } catch (e) {
      this.set({ error: errorText(e), ready: false });
    }
  }
  async refreshHistory() {
    try {
      this.set({
        history: (await unwrap(this.nas.api.GET("/api/v1/assistant/sessions")))
          .sessions,
      });
    } catch (e) {
      this.set({ error: errorText(e) });
    }
  }
  private async loadMessages(id: string, epoch: number) {
    try {
      const result = await unwrap(
        this.nas.api.GET("/api/v1/assistant/sessions/{sessionId}/messages", {
          params: { path: { sessionId: id } },
        }),
      );
      if (epoch !== this.epoch || this.state.storedId !== id) return;
      const messages: Message[] = result.messages.flatMap((row, index) => {
        if (row.role !== "user" && row.role !== "assistant") return [];
        const metadata = row.display_metadata as
          { display_kind?: string } | undefined;
        if (metadata?.display_kind === "hidden") return [];
        const text = messageText(row.content);
        return text
          ? [{ id: String(row.id ?? `${id}-${index}`), role: row.role, text }]
          : [];
      });
      this.set({ messages });
    } catch (e) {
      if (epoch === this.epoch) this.set({ error: errorText(e) });
    }
  }
  async open(id?: string) {
    const epoch = ++this.epoch;
    this.replyId = "";
    this.set({
      storedId: id,
      liveId: undefined,
      messages: [],
      approvals: [],
      busy: false,
      loading: Boolean(id),
      error: "",
      activity: "",
    });
    if (!id) return;
    await this.loadMessages(id, epoch);
    if (epoch !== this.epoch) return;
    try {
      if (this.state.ready) {
        const result = await this.gateway.request<SessionResumeResult>(
          "session.resume",
          { session_id: id, cols: 80, omit_messages: true },
        );
        if (epoch === this.epoch)
          this.set({
            liveId: result.session_id,
            busy: Boolean(result.running),
          });
      }
    } catch {
      if (epoch === this.epoch)
        this.set({ error: "暂时无法继续此对话，请重新连接后重试。" });
    } finally {
      if (epoch === this.epoch) this.set({ loading: false });
    }
  }
  async send(text: string): Promise<boolean> {
    if (
      !text.trim() ||
      !this.state.ready ||
      this.state.busy ||
      this.state.loading
    )
      return false;
    const epoch = this.epoch;
    this.set({ busy: true, error: "", activity: "正在发送" });
    this.replyId = "";
    try {
      let id = this.state.liveId;
      if (!id && this.state.storedId) {
        const result = await this.gateway.request<SessionResumeResult>(
          "session.resume",
          { session_id: this.state.storedId, cols: 80, omit_messages: true },
        );
        if (epoch !== this.epoch) return false;
        id = result.session_id;
        if (result.running) {
          this.set({
            liveId: id,
            busy: true,
            activity: "VNAS 仍在处理上一条消息",
          });
          return false;
        }
      }
      if (!id) {
        const result = await this.gateway.request<SessionCreateResult>(
          "session.create",
          { cols: 80 },
        );
        if (epoch !== this.epoch) return false;
        id = result.session_id;
        this.set({ storedId: result.stored_session_id });
      }
      if (epoch !== this.epoch) return false;
      this.set({
        liveId: id,
        messages: [
          ...this.state.messages,
          { id: `user-${Date.now()}`, role: "user", text },
        ],
      });
      await this.gateway.request("prompt.submit", { session_id: id, text });
      return true;
    } catch {
      if (epoch === this.epoch)
        this.set({
          busy: false,
          activity: "",
          error: "发送结果尚未确认。重新连接并核对历史后再决定是否重发。",
        });
      return false;
    }
  }
  async stop() {
    if (!this.state.liveId) return;
    try {
      await this.gateway.request("session.interrupt", {
        session_id: this.state.liveId,
      });
    } catch {
      this.set({ error: "停止请求未确认，请重新连接后核对任务状态。" });
    }
  }
  async refreshApprovals() {
    try {
      const { approvals } = await unwrap(
        this.nas.api.GET("/api/v1/assistant/approvals"),
      );
      this.set({
        approvals: approvals.filter(
          (a) =>
            a.status === "awaiting" &&
            (a.sessionId === this.state.liveId ||
              (Boolean(this.state.storedId) &&
                a.storedSessionId === this.state.storedId)),
        ),
      });
    } catch {
      /* Keep the existing card; the server revalidates every decision. */
    }
  }
  async decide(
    approval: AssistantApproval,
    action: "confirm" | "reject" | "answer",
    answers?: Record<string, string | null>,
  ) {
    const params = {
      path: { requestId: approval.requestId },
      header: { "X-CSRF-Token": this.nas.csrf },
    };
    if (action === "answer")
      await unwrap(
        this.nas.api.POST("/api/v1/assistant/approvals/{requestId}/answer", {
          params,
          body: { answers: answers ?? {} },
        }),
      );
    else if (action === "confirm")
      await unwrap(
        this.nas.api.POST("/api/v1/assistant/approvals/{requestId}/confirm", {
          params,
        }),
      );
    else
      await unwrap(
        this.nas.api.POST("/api/v1/assistant/approvals/{requestId}/reject", {
          params,
        }),
      );
    await this.refreshApprovals();
  }
  setActive(active: boolean) {
    this.active = active;
    if (!active) {
      clearTimeout(this.reconnect);
      this.reconnect = undefined;
      this.gateway.close();
    } else void this.start();
  }
  dispose() {
    this.disposed = true;
    clearTimeout(this.reconnect);
    clearInterval(this.poll);
    this.gateway.close();
    this.listeners.clear();
  }
}
