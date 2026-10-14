export type Health = "healthy" | "warning" | "critical" | "unknown";
export type DiskRole = "system" | "data" | "unassigned";
export type Transport = "nvme" | "sata" | "usb" | "unknown";
export type DataSource = "simulated" | "live";

export interface HostState {
  dataSource: DataSource;
  productVersion: string;
  observedAt: string;
  system: {
    id: string;
    hostname: string;
    operatingSystem: { name: string; version: string };
    architecture: string;
    uptimeSeconds: number;
    health: Health;
  };
  disks: Array<{
    id: string;
    model: string;
    transport: Transport;
    capacityBytes: number;
    rotational: boolean;
	removable: boolean;
	inUse: boolean;
	filesystems: string[];
    role: DiskRole;
    health: Health;
	smartStatus: Health;
	eligibleForDataVolume: boolean;
	ineligibleReasons: string[];
    temperatureCelsius?: number;
  }>;
}

let sessionEndedListener: (() => void) | undefined;

// onSessionEnded lets the signed-in desktop return to the login screen; it
// returns the unsubscribe function.
export function onSessionEnded(listener: () => void): () => void {
  sessionEndedListener = listener;
  return () => { if (sessionEndedListener === listener) sessionEndedListener = undefined; };
}

// sessionEnded reports a 401 that arrived outside apiFetch, such as on an
// upload part that tus-js-client sent.
export function sessionEnded() { sessionEndedListener?.(); }

// apiFetch is fetch for product API calls. Once signed in, every 401 means the
// session ended (expired, signed out elsewhere or the account was disabled);
// wrong passwords are 401 only at login, before any listener exists.
export async function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const response = await fetch(input, init);
  if (response.status === 401) sessionEndedListener?.();
  return response;
}

export class HostStateError extends Error {
  constructor(readonly status?: number) {
    super(status ? `host state request failed with status ${status}` : "host state request failed");
  }
}

export async function readHostState(signal?: AbortSignal): Promise<HostState> {
  let response: Response;
  try {
    response = await apiFetch("/api/v1/host-state", {
      method: "GET",
      headers: { Accept: "application/json" },
      signal,
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw error;
    }
    throw new HostStateError();
  }
  if (!response.ok) {
    throw new HostStateError(response.status);
  }
  const value: unknown = await response.json();
  if (!isHostState(value)) {
    throw new HostStateError();
  }
  return value;
}

function isHostState(value: unknown): value is HostState {
  if (!isRecord(value) || !["simulated", "live"].includes(String(value.dataSource))) return false;
  if (typeof value.productVersion !== "string" || typeof value.observedAt !== "string") return false;
  if (!isRecord(value.system) || !Array.isArray(value.disks)) return false;
  const system = value.system;
  if (
    typeof system.id !== "string" ||
    typeof system.hostname !== "string" ||
    typeof system.architecture !== "string" ||
    typeof system.uptimeSeconds !== "number" ||
    !isHealth(system.health) ||
    !isRecord(system.operatingSystem) ||
    typeof system.operatingSystem.name !== "string" ||
    typeof system.operatingSystem.version !== "string"
  ) {
    return false;
  }
  return value.disks.every(
    (disk) =>
      isRecord(disk) &&
      typeof disk.id === "string" &&
      typeof disk.model === "string" &&
      ["nvme", "sata", "usb", "unknown"].includes(String(disk.transport)) &&
      typeof disk.capacityBytes === "number" &&
      typeof disk.rotational === "boolean" &&
	  typeof disk.removable === "boolean" &&
	  typeof disk.inUse === "boolean" &&
	  Array.isArray(disk.filesystems) &&
      ["system", "data", "unassigned"].includes(String(disk.role)) &&
      isHealth(disk.health) &&
	  isHealth(disk.smartStatus) &&
	  typeof disk.eligibleForDataVolume === "boolean" &&
	  Array.isArray(disk.ineligibleReasons) &&
      (disk.temperatureCelsius === undefined || typeof disk.temperatureCelsius === "number"),
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isHealth(value: unknown): value is Health {
  return ["healthy", "warning", "critical", "unknown"].includes(String(value));
}

export interface HostMetrics {
  dataSource: DataSource;
  observedAt: string;
  cpu: { usagePercent: number; logicalCores: number };
  memory: { totalBytes: number; usedBytes: number };
  network: { receiveBytesPerSecond: number; transmitBytesPerSecond: number };
}

export async function readHostMetrics(signal?: AbortSignal): Promise<HostMetrics> {
  const response = await apiFetch("/api/v1/metrics", { method: "GET", headers: { Accept: "application/json" }, credentials: "same-origin", signal });
  if (!response.ok) throw new HostStateError(response.status);
  const value: unknown = await response.json();
  if (!isHostMetrics(value)) throw new HostStateError();
  return value;
}

function isHostMetrics(value: unknown): value is HostMetrics {
  if (!isRecord(value) || !["simulated", "live"].includes(String(value.dataSource)) || typeof value.observedAt !== "string") return false;
  const { cpu, memory, network } = value;
  return (
    isRecord(cpu) && isNumberIn(cpu.usagePercent, 0, 100) && isNumberIn(cpu.logicalCores, 1, Infinity) &&
    isRecord(memory) && isNumberIn(memory.totalBytes, 1, Infinity) && isNumberIn(memory.usedBytes, 0, Number(memory.totalBytes)) &&
    isRecord(network) && isNumberIn(network.receiveBytesPerSecond, 0, Infinity) && isNumberIn(network.transmitBytesPerSecond, 0, Infinity)
  );
}

function isNumberIn(value: unknown, min: number, max: number): value is number {
  return typeof value === "number" && value >= min && value <= max;
}

export interface User { id: string; username: string; role: "admin" | "member"; status: "pending" | "active" | "disabled" | "error"; createdAt: string; mustChangePassword?: boolean }
export interface Session { csrfToken: string; expiresAt: string; user: User }
export interface Space { id: string; kind: "private" | "shared"; name: string; ownerUserId?: string; createdAt: string; viewing?: { grantId: string; expiresAt: string } }
export interface Notification { id: string; kind: "admin_viewing" | "admin_library_viewing" | "credential_reset"; actorUsername: string; reason?: string; expiresAt?: string; createdAt: string }
export type ViewingScope = "space" | "library";
export interface ViewingGrant { id: string; scope: ViewingScope; spaceId: string; ownerUserId: string; reason: string; grantedAt: string; expiresAt: string }
export interface FileEntry { id: string; spaceId: string; parentId?: string; name: string; kind: "file" | "directory"; sizeBytes: number; modifiedAt: string }
export interface TrashItem { id: string; entryId: string; spaceId: string; name: string; deletedBy: string; deletedAt: string }
export interface Volume { id: string; diskId: string; filesystemUuid?: string; capacityBytes?: number; availableBytes?: number; state: "creating" | "available" | "unavailable" | "read_only" }
export interface StoragePlan { id: string; diskId: string; diskModel: string; capacityBytes: number; fingerprint: string; signatures: string[]; confirmationPhrase: string; actions: Array<{kind: string; description: string}>; state: string; expiresAt: string; failure?: string; volume?: Volume }

export class APIError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) { super(message); }
}

let csrfToken = "";
export function setSession(session?: Session) { csrfToken = session?.csrfToken ?? ""; }
// csrfHeaders lets the Docker and App Center clients send the session's CSRF token on writes.
export function csrfHeaders(): Record<string, string> { return csrfToken ? { "X-CSRF-Token": csrfToken } : {}; }

// request sends a same-origin product API call; mutations carry the CSRF token.
export async function request<T>(path: string, init: RequestInit = {}, mutation = false): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (mutation && csrfToken) headers.set("X-CSRF-Token", csrfToken);
  const response = await apiFetch(path, { ...init, headers, credentials: "same-origin" });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: { code: "request_failed", message: `请求失败 (${response.status})` } }));
    throw new APIError(response.status, body?.error?.code ?? "request_failed", body?.error?.message ?? "请求失败");
  }
  // A success without a body (204, or an accepted job that reports nothing) is not JSON.
  if (response.status === 204 || response.headers?.get("Content-Length") === "0") return undefined as T;
  return response.json() as Promise<T>;
}

// uploadForm posts a multipart form like request does, reporting upload
// progress as a fraction; fetch cannot report it. A connection that hangs
// never fails by itself, so one that moves nothing for stallAfter, or whose
// answer does not come within answerWithin once all was sent, is given up
// as a lost connection.
export function uploadForm<T>(path: string, body: FormData, onProgress: (fraction: number) => void, signal?: AbortSignal,
  { stallAfter = 30_000, answerWithin = 60_000 }: { stallAfter?: number; answerWithin?: number } = {}): Promise<T> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    let movedAt = Date.now();
    let loaded = 0;
    let sentAt = 0;
    let stalled = false;
    const watch = setInterval(() => {
      const now = Date.now();
      if (sentAt ? now - sentAt > answerWithin : now - movedAt > stallAfter) {
        stalled = true;
        xhr.abort();
      }
    }, 1000);
    xhr.open("POST", path);
    xhr.setRequestHeader("Accept", "application/json");
    if (csrfToken) xhr.setRequestHeader("X-CSRF-Token", csrfToken);
    xhr.upload.onprogress = (event) => {
      // Browsers report progress also when nothing more was sent.
      if (event.loaded > loaded) {
        loaded = event.loaded;
        movedAt = Date.now();
      }
      if (event.lengthComputable && event.total > 0) onProgress(event.loaded / event.total);
    };
    xhr.upload.onload = () => { sentAt = Date.now(); };
    xhr.onload = () => {
      clearInterval(watch);
      if (xhr.status === 401) sessionEndedListener?.();
      let body: { error?: { code?: string; message?: string } } | undefined;
      try { body = xhr.responseText ? JSON.parse(xhr.responseText) : undefined; } catch { body = undefined; }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as T);
      else reject(new APIError(xhr.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `请求失败 (${xhr.status})`));
    };
    xhr.onerror = () => { clearInterval(watch); reject(new APIError(0, "network_error", "网络连接中断")); };
    xhr.onabort = () => { clearInterval(watch); reject(stalled ? new APIError(0, "network_error", "NAS 长时间没有响应") : new DOMException("upload aborted", "AbortError")); };
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(body);
  });
}

const json = (value: unknown): RequestInit => ({ method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });

export const getSetupStatus = () => request<{setupRequired: boolean}>("/api/v1/setup/status");
// localConsole asks for a session that lasts until sign-out; the server grants it only to the device's own screen.
const credentials = (username: string, password: string, localConsole: boolean) => json({ username, password, ...(localConsole ? { localConsole } : {}) });
export async function setupAdministrator(username: string, password: string, localConsole = false) { const value = await request<Session>("/api/v1/setup/admin", credentials(username, password, localConsole)); setSession(value); return value; }
export async function login(username: string, password: string, localConsole = false) { const value = await request<Session>("/api/v1/session", credentials(username, password, localConsole)); setSession(value); return value; }
export async function currentSession() { const value = await request<Session>("/api/v1/session"); setSession(value); return value; }
export const changePassword = (currentPassword: string, newPassword: string) => request<void>("/api/v1/session/password", json({ currentPassword, newPassword }), true);
export const listNotifications = async () => (await request<{items: Notification[]}>("/api/v1/notifications")).items;
export const acknowledgeNotification = (id: string) => request<void>(`/api/v1/notifications/${encodeURIComponent(id)}/acknowledge`, json({}), true);
export const startViewing = (userId: string, password: string, reason: string, scope: ViewingScope = "space") => request<ViewingGrant>(`/api/v1/users/${encodeURIComponent(userId)}/viewing`, json({ password, reason, scope }), true);
export const endViewing = (grantId: string) => request<void>(`/api/v1/viewing/${encodeURIComponent(grantId)}`, { method: "DELETE" }, true);
export async function logout() { await request<void>("/api/v1/session", { method: "DELETE" }, true); setSession(); }
export const listSpaces = async () => (await request<{items: Space[]}>("/api/v1/spaces")).items;
export const listEntries = async (spaceId: string, parentId = "") => (await request<{items: FileEntry[]}>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/entries?parentId=${encodeURIComponent(parentId)}`)).items;
export const createDirectory = (spaceId: string, parentId: string, name: string) => request<FileEntry>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/directories`, json({ parentId, name }), true);
// Without spaceId the target folder is in the entry's own space.
export const moveFile = (fileId: string, parentId: string, name: string, spaceId?: string) => request<FileEntry>(`/api/v1/files/${encodeURIComponent(fileId)}`, { ...json({ spaceId, parentId, name }), method: "PATCH" }, true);
export const copyFile = (fileId: string, parentId: string, name: string, spaceId?: string) => request<FileEntry>(`/api/v1/files/${encodeURIComponent(fileId)}/copies`, json({ spaceId, parentId, name }), true);
export const deleteFile = (fileId: string) => request<TrashItem>(`/api/v1/files/${encodeURIComponent(fileId)}`, { method: "DELETE" }, true);
export const fileDownloadURL = (fileId: string) => `/api/v1/files/${encodeURIComponent(fileId)}/content`;
export const filePreviewURL = (fileId: string) => `${fileDownloadURL(fileId)}?disposition=inline`;
export async function readFileTextPreview(fileId: string) {
  const response = await fetch(fileDownloadURL(fileId));
  if (!response.ok) throw new APIError(response.status, "preview_failed", "无法读取文件预览");
  return response.text();
}
export const listTrash = async () => (await request<{items: TrashItem[]}>("/api/v1/trash")).items;
export const restoreTrash = (id: string, name: string) => request<FileEntry>(`/api/v1/trash/${encodeURIComponent(id)}/restore`, json({ parentId: "", name }), true);
export const purgeTrash = (id: string) => request<void>(`/api/v1/trash/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const listUsers = async () => (await request<{items: User[]}>("/api/v1/users")).items;
export const createMember = (username: string, password: string) => request<User>("/api/v1/users", json({ username, password }), true);
export const resetMember = (id: string, password: string) => request<void>(`/api/v1/users/${encodeURIComponent(id)}/credential`, { ...json({ password }), method: "PATCH" }, true);
export const disableMember = (id: string) => request<void>(`/api/v1/users/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const listVolumes = async () => (await request<{items: Volume[]}>("/api/v1/volumes")).items;
export const createStoragePlan = (diskId: string) => request<StoragePlan>("/api/v1/storage/plans", json({ diskId }), true);
export const confirmStoragePlan = (id: string, confirmationPhrase: string) => request<StoragePlan>(`/api/v1/storage/plans/${encodeURIComponent(id)}/confirm`, json({ confirmationPhrase }), true);
export const executeStoragePlan = (id: string) => request<StoragePlan>(`/api/v1/storage/plans/${encodeURIComponent(id)}/execute`, json({}), true);
