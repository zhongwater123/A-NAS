import createClient from "openapi-fetch";
import ipaddr from "ipaddr.js";
import { parseString, splitCookiesString } from "set-cookie-parser";
import type { paths, Session } from "../../../web/src/api.generated";
export type {
  Session,
  Space,
  FileEntry,
  Transfer,
  AssistantApproval,
  AssistantSession,
} from "../../../web/src/api.generated";
export interface Vault {
  read(): Promise<string | null>;
  write(value: string): Promise<void>;
  clear(): Promise<void>;
}
export class NasError extends Error {
  constructor(
    public code: string,
    public status = 0,
  ) {
    super(code);
  }
}
export function errorText(error: unknown): string {
  if (error instanceof NasError)
    return (
      (
        {
          unauthorized: "登录已过期，请重新登录。",
          authentication_required: "登录已过期，请重新登录。",
          invalid_credentials: "账号或密码不正确。",
          forbidden: "当前账号没有访问权限。",
          volume_unavailable: "NAS 数据卷暂时不可用。",
          insufficient_storage: "NAS 剩余空间不足。",
          assistant_unavailable: "这个账号的 VNAS 尚未就绪。",
          too_many_attempts: "登录尝试过于频繁，请稍后再试。",
          name_conflict: "目标已有同名文件。",
          wrong_origin: "连接地址发生变化，请检查 NAS 地址后重新连接。",
          invalid_address:
            "请输入 NAS 的局域网 IP 地址，例如 http://192.168.1.10。",
          host_not_configured:
            "这个测试包尚未配置该 NAS 的 HTTP 地址，请使用对应地址构建的测试包。",
          cookie_missing: "NAS 没有返回可用的登录会话，请检查入口配置。",
          file_changed: "文件在传输期间发生变化，请重新选择下载。",
          incomplete_download: "下载中断，点击重试继续。",
          invalid_download_range: "NAS 返回的续传位置不一致，请重新下载。",
        } as Record<string, string>
      )[error.code] ?? `NAS 请求失败（${error.code}），请重试。`
    );
  return "连接未完成，请确认手机与 NAS 在同一局域网后重试。";
}
export function normalizeAddress(input: string): string {
  try {
    const url = new URL(
      input.includes("://") ? input.trim() : `http://${input.trim()}`,
    );
    const host = url.hostname.replace(/^\[|\]$/g, "");
    if (
      !["http:", "https:"].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.search ||
      url.hash ||
      url.pathname !== "/" ||
      !ipaddr.isValid(host)
    )
      throw new Error();
    if (
      !["private", "loopback", "linkLocal", "uniqueLocal"].includes(
        ipaddr.process(host).range(),
      )
    )
      throw new Error();
    return url.origin;
  } catch {
    throw new NasError("invalid_address");
  }
}
export function sameOriginURL(origin: string, path: string): string {
  const url = new URL(path, `${origin}/`);
  if (url.origin !== origin || url.username || url.password || url.hash)
    throw new NasError("wrong_origin");
  return url.toString();
}
export function sessionCookie(value: string): string {
  const cookie = splitCookiesString(value)
    .map((c) => parseString(c))
    .find((c) => c?.name === "anas_session");
  if (!cookie?.value || /[\r\n;]/.test(cookie.value))
    throw new NasError("cookie_missing");
  return `anas_session=${cookie.value}`;
}
export class NasClient {
  private cookie = "";
  session: Session | null = null;
  private address = "";
  onExpired = () => {};
  setExpiredHandler(handler: () => void) {
    this.onExpired = handler;
  }
  constructor(
    private transport: typeof fetch,
    private vault: Vault,
    private lanHosts: string[],
  ) {}
  get origin() {
    return this.address;
  }
  get scope() {
    return `${this.address}|${this.session?.user.id ?? ""}`;
  }
  get headers() {
    return { Cookie: this.cookie, Origin: this.address };
  }
  get csrf() {
    return this.session?.csrfToken ?? "";
  }
  url(path: string) {
    return sameOriginURL(this.address, path);
  }
  get websocketURL() {
    return this.url("/api/v1/assistant/ws").replace(/^http/, "ws");
  }
  private setAddress(input: string) {
    const address = normalizeAddress(input),
      url = new URL(address);
    if (
      url.protocol === "http:" &&
      !this.lanHosts.includes(url.hostname.replace(/^\[|\]$/g, ""))
    )
      throw new NasError("host_not_configured");
    this.address = address;
  }
  api = createClient<paths>({
    baseUrl: "http://127.0.0.1",
    fetch: async (request: Request) =>
      this.request(
        new URL(request.url).pathname + new URL(request.url).search,
        {
          method: request.method,
          headers: request.headers,
          body: ["GET", "HEAD"].includes(request.method)
            ? undefined
            : await request.text(),
        },
      ),
  });
  async request(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers);
    if (this.cookie) headers.set("Cookie", this.cookie);
    headers.set("Origin", this.address);
    if (!["GET", "HEAD"].includes(init.method ?? "GET") && this.csrf)
      headers.set("X-CSRF-Token", this.csrf);
    const response = await this.transport(this.url(path), {
      ...init,
      headers,
      credentials: "omit",
      redirect: "error",
      signal: init.signal ?? AbortSignal.timeout(30_000),
    });
    if (response.url && new URL(response.url).origin !== this.address)
      throw new NasError("wrong_origin");
    if (response.status === 401 && this.session) {
      this.cookie = "";
      this.session = null;
      await this.vault.clear();
      this.onExpired();
    }
    return response;
  }
  async login(
    address: string,
    username: string,
    password: string,
  ): Promise<Session> {
    this.setAddress(address);
    this.session = null;
    this.cookie = "";
    const response = await this.request("/api/v1/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: username.trim(), password }),
    });
    const data = await response.json();
    if (!response.ok)
      throw new NasError(
        data?.error?.code ?? "invalid_credentials",
        response.status,
      );
    this.cookie = sessionCookie(response.headers.get("set-cookie") ?? "");
    this.session = data as Session;
    if (!this.session.csrfToken || !this.session.user?.id) {
      this.session = null;
      this.cookie = "";
      throw new NasError("malformed_response");
    }
    await this.vault.write(
      JSON.stringify({ origin: this.address, cookie: this.cookie }),
    );
    return this.session;
  }
  async restore(): Promise<Session | null> {
    const saved = await this.vault.read();
    if (!saved) return null;
    const profile = JSON.parse(saved);
    this.setAddress(profile.origin);
    this.cookie = sessionCookie(profile.cookie);
    try {
      this.session = await unwrap(this.api.GET("/api/v1/session"));
      return this.session;
    } catch (error) {
      this.cookie = "";
      this.session = null;
      if (error instanceof NasError && error.status === 401)
        await this.vault.clear();
      throw error;
    }
  }
  async logout() {
    try {
      await unwrap(
        this.api.DELETE("/api/v1/session", {
          params: { header: { "X-CSRF-Token": this.csrf } },
        }),
      );
    } finally {
      this.cookie = "";
      this.session = null;
      await this.vault.clear();
    }
  }
}
export async function unwrap<T>(
  request: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await request;
  if (!response.ok) {
    const problem = error as { error?: { code?: string } };
    throw new NasError(
      problem?.error?.code ??
        (response.status === 401 ? "unauthorized" : `http_${response.status}`),
      response.status,
    );
  }
  return data as T;
}
