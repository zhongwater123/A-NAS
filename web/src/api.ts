export type Health = "healthy" | "warning" | "critical" | "unknown";
export type DiskRole = "system" | "data" | "unassigned";
export type Transport = "nvme" | "sata" | "usb" | "unknown";
export type DataSource = "simulated" | "live";
export type HealthFindingCode =
  | "disk_failing" | "disk_warning" | "disk_health_unknown" | "disk_hot"
  | "volume_offline" | "volume_read_only" | "volume_full" | "volume_nearly_full" | "volume_errors";
// HealthFinding is one reason the system is not plainly healthy; value is the
// temperature, percentage used or error count its code describes.
export interface HealthFinding { code: HealthFindingCode; severity: "warning" | "critical"; diskId?: string; value?: number }

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
    // healthFindings say why, most severe first; empty when healthy.
    healthFindings: HealthFinding[];
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

// A write gate holds every write the page sends (any method but GET, HEAD and
// OPTIONS) until it says whether the write may go: VNAS's controller asks the
// member about the writes its steps on the desktop cause (assistant/webosControl.ts).
export type WriteGate = (method: string, path: string) => Promise<boolean>;
let writeGate: WriteGate | undefined;
export function setWriteGate(gate: WriteGate): () => void {
  writeGate = gate;
  return () => { if (writeGate === gate) writeGate = undefined; };
}

// passWriteGate waits for the write gate, and throws when the write may not go;
// requests sent outside apiFetch (tus-js-client's upload creation) call it too.
export async function passWriteGate(method: string, url: string) {
  const verb = method.toUpperCase();
  if (!writeGate || verb === "GET" || verb === "HEAD" || verb === "OPTIONS") return;
  if (!(await writeGate(verb, new URL(url, window.location.href).pathname))) {
    throw new APIError(403, "not_allowed_for_vnas", "没有允许 VNAS 这一步，什么都没有改");
  }
}

// apiFetch is fetch for product API calls. Once signed in, every 401 means the
// session ended (expired, signed out elsewhere or the account was disabled);
// wrong passwords are 401 only at login, before any listener exists.
export async function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const given = input instanceof Request ? input : undefined;
  await passWriteGate(init?.method ?? given?.method ?? "GET", given?.url ?? String(input));
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
  // A Product Service from before the assessment sends no findings.
  (value.system as { healthFindings?: HealthFinding[] }).healthFindings ??= [];
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
    !(system.healthFindings === undefined || (Array.isArray(system.healthFindings) && system.healthFindings.every(isHealthFinding))) ||
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

function isHealthFinding(value: unknown): value is HealthFinding {
  return isRecord(value) && typeof value.code === "string" && ["warning", "critical"].includes(String(value.severity)) &&
    (value.diskId === undefined || typeof value.diskId === "string") && (value.value === undefined || typeof value.value === "number");
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

// nickname, when set, is what pages show instead of username; avatarVersion
// names the current avatar (see avatarURL).
export interface User { id: string; username: string; role: "admin" | "member"; status: "pending" | "active" | "disabled" | "error"; createdAt: string; mustChangePassword?: boolean; nickname?: string; avatarVersion?: string }
// Profile is how an account appears to everyone signed in.
export interface Profile { id: string; username: string; nickname?: string; avatarVersion?: string }
// credentialReset is the latest time an administrator chose the user's
// password; the user keeps seeing it.
export interface Session { csrfToken: string; expiresAt: string; user: User; credentialReset?: { actorUsername: string; at: string } }
export interface Space { writable?: boolean; id: string; kind: "private" | "shared"; name: string; ownerUserId?: string; createdAt: string; viewing?: { grantId: string; expiresAt: string } }
export interface Notification { id: string; kind: "admin_viewing" | "admin_library_viewing" | "credential_reset" | "assistant_task"; actorUsername: string; reason?: string; expiresAt?: string; task?: AssistantTaskNotice; createdAt: string }
/** A run of one of the member's VNAS scheduled tasks that finished; at most one waits per task. */
export interface AssistantTaskNotice { taskId: string; name: string; status: string; ranAt: string; sessionId?: string; summary?: string }
export type ViewingScope = "space" | "library";
export interface ViewingGrant { id: string; scope: ViewingScope; spaceId: string; ownerUserId: string; reason: string; grantedAt: string; expiresAt: string }
export interface FileEntry { id: string; spaceId: string; parentId?: string; name: string; kind: "file" | "directory"; sizeBytes: number; modifiedAt: string }
// TrashItem mirrors internal/files.TrashItem: originalPath is the folder a
// restore without a parentId puts the item back in, relative to the space
// root; when originalGone is set that folder is gone and the item goes to
// the root instead.
export interface TrashItem {
  id: string; entryId: string; spaceId: string; name: string; kind: "file" | "directory"; sizeBytes: number;
  originalPath: string; originalGone: boolean; deletedBy: string; deletedAt: string; purgeAt: string;
}
// VolumeLayout is how the data volume keeps files on its disks (ADR 0025).
export type VolumeLayout = "single" | "mirror" | "span" | "stripe";
// VolumeConversion is set while Btrfs converts the volume to its layout
// after disks were added; waiting means it stopped and continues later.
export interface VolumeConversion { state: "running" | "waiting"; convertedBytes: number; totalBytes: number }
export interface Volume {
  id: string; diskIds: string[]; filesystemUuid?: string; layout: VolumeLayout; capacityBytes?: number; availableBytes?: number;
  state: "creating" | "available" | "unavailable" | "read_only"; conversion?: VolumeConversion;
}
export interface PlannedDisk { id: string; model: string; capacityBytes: number; fingerprint: string; signatures: Array<{ type: string; label?: string }> }
// A plan creates the data volume on its disks, or adds them to it; the
// capacities estimate the space for files before and after.
export interface StoragePlan {
  id: string; kind: "create_volume" | "add_disks"; layout: VolumeLayout; disks: PlannedDisk[]; capacityBeforeBytes?: number; capacityAfterBytes: number;
  confirmationPhrase: string; actions: Array<{kind: string; description: string}>; state: string; expiresAt: string; failure?: string; volume?: Volume;
}

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
// progress as a fraction; fetch cannot report it.
export async function uploadForm<T>(path: string, body: FormData, onProgress: (fraction: number) => void, signal?: AbortSignal): Promise<T> {
  await passWriteGate("POST", path);
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", path);
    xhr.setRequestHeader("Accept", "application/json");
    if (csrfToken) xhr.setRequestHeader("X-CSRF-Token", csrfToken);
    xhr.upload.onprogress = (event) => { if (event.lengthComputable && event.total > 0) onProgress(event.loaded / event.total); };
    xhr.onload = () => {
      if (xhr.status === 401) sessionEndedListener?.();
      let body: { error?: { code?: string; message?: string } } | undefined;
      try { body = xhr.responseText ? JSON.parse(xhr.responseText) : undefined; } catch { body = undefined; }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as T);
      else reject(new APIError(xhr.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `请求失败 (${xhr.status})`));
    };
    xhr.onerror = () => reject(new APIError(0, "network_error", "网络连接中断"));
    xhr.onabort = () => reject(new DOMException("upload aborted", "AbortError"));
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(body);
  });
}

const json = (value: unknown): RequestInit => ({ method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });

// setupAllowed is false where this connection may not set the device up:
// only its own screen or an SSH tunnel may.
export const getSetupStatus = () => request<{setupRequired: boolean; setupAllowed?: boolean}>("/api/v1/setup/status");
// localConsole asks for a session that lasts until sign-out; the server grants it only to the device's own screen.
const credentials = (username: string, password: string, localConsole: boolean) => json({ username, password, ...(localConsole ? { localConsole } : {}) });
export async function setupAdministrator(username: string, password: string, localConsole = false) { const value = await request<Session>("/api/v1/setup/admin", credentials(username, password, localConsole)); setSession(value); return value; }
export async function login(username: string, password: string, localConsole = false) { const value = await request<Session>("/api/v1/session", credentials(username, password, localConsole)); setSession(value); return value; }
export async function currentSession() { const value = await request<Session>("/api/v1/session"); setSession(value); return value; }
export const changePassword = (currentPassword: string, newPassword: string) => request<void>("/api/v1/session/password", json({ currentPassword, newPassword }), true);
export const updateNickname = (nickname: string) => request<User>("/api/v1/session/profile", { ...json({ nickname }), method: "PATCH" }, true);
// uploadAvatar sends the cropped picture; the server makes the stored avatar from it.
export const uploadAvatar = (picture: Blob) => request<User>("/api/v1/session/avatar", { method: "PUT", headers: { "Content-Type": picture.type || "image/jpeg" }, body: picture }, true);
export const removeAvatar = () => request<User>("/api/v1/session/avatar", { method: "DELETE" }, true);
// LocalDisplay is how the screen attached to the NAS behaves; sleepAfterMinutes 0 keeps it on.
export interface LocalDisplay { sleepAfterMinutes: number }
// readLocalDisplay needs no session: the sign-in page on the local console reads it too.
export const readLocalDisplay = () => request<LocalDisplay>("/api/v1/local-display");
export const updateLocalDisplay = (sleepAfterMinutes: number) => request<LocalDisplay>("/api/v1/local-display", { ...json({ sleepAfterMinutes }), method: "PUT" }, true);
// Wallpaper is the desktop every account sees; without image it is the built-in one.
export type WallpaperStyle = "fill" | "fit" | "stretch" | "center" | "tile";
export interface Wallpaper { style: WallpaperStyle; image?: { version: string; width: number; height: number } }
export const readWallpaper = () => request<Wallpaper>("/api/v1/wallpaper");
export const updateWallpaperStyle = (style: WallpaperStyle) => request<Wallpaper>("/api/v1/wallpaper", { ...json({ style }), method: "PATCH" }, true);
export const uploadWallpaper = (picture: Blob) => request<Wallpaper>("/api/v1/wallpaper/image", { method: "PUT", headers: { "Content-Type": picture.type || "image/jpeg" }, body: picture }, true);
export const resetWallpaper = () => request<Wallpaper>("/api/v1/wallpaper/image", { method: "DELETE" }, true);
// A version names one picture for good, so the browser keeps it cached.
export const wallpaperImageURL = (version: string) => `/api/v1/wallpaper/image?v=${encodeURIComponent(version)}`;
export const listProfiles = async () => (await request<{items: Profile[]}>("/api/v1/profiles")).items;
// A version names one picture for good, so the browser keeps it cached.
export const avatarURL = (userId: string, version: string) => `/api/v1/users/${encodeURIComponent(userId)}/avatar?v=${encodeURIComponent(version)}`;
export const listNotifications = async () => (await request<{items: Notification[]}>("/api/v1/notifications")).items;
export const acknowledgeNotification = (id: string) => request<void>(`/api/v1/notifications/${encodeURIComponent(id)}/acknowledge`, json({}), true);
export const startViewing = (userId: string, password: string, reason: string, scope: ViewingScope = "space") => request<ViewingGrant>(`/api/v1/users/${encodeURIComponent(userId)}/viewing`, json({ password, reason, scope }), true);
export const endViewing = (grantId: string) => request<void>(`/api/v1/viewing/${encodeURIComponent(grantId)}`, { method: "DELETE" }, true);
export async function logout() { await request<void>("/api/v1/session", { method: "DELETE" }, true); setSession(); }
export const listSpaces = async () => (await request<{items: Space[]}>("/api/v1/spaces")).items;
export const listEntries = async (spaceId: string, parentId = "") => (await request<{items: FileEntry[]}>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/entries?parentId=${encodeURIComponent(parentId)}`)).items;
export const createDirectory = (spaceId: string, parentId: string, name: string) => request<FileEntry>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/directories`, json({ parentId, name }), true);
export function uploadFile(spaceId: string, parentId: string, file: File) { const body = new FormData(); body.set("parentId", parentId); body.set("file", file); return request<FileEntry>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/uploads`, { method: "POST", body }, true); }
// sendFile uploads like uploadFile and reports progress as a fraction; parentId must precede the file in the form.
export function sendFile(spaceId: string, parentId: string, file: File, onProgress: (fraction: number) => void, signal?: AbortSignal) { const body = new FormData(); body.set("parentId", parentId); body.set("file", file); return uploadForm<FileEntry>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/uploads`, body, onProgress, signal); }
// Without spaceId the target folder is in the entry's own space.
export const moveFile = (fileId: string, parentId: string, name: string, spaceId?: string) => request<FileEntry>(`/api/v1/files/${encodeURIComponent(fileId)}`, { ...json({ spaceId, parentId, name }), method: "PATCH" }, true);
export const copyFile = (fileId: string, parentId: string, name: string, spaceId?: string) => request<FileEntry>(`/api/v1/files/${encodeURIComponent(fileId)}/copies`, json({ spaceId, parentId, name }), true);
export const deleteFile = (fileId: string) => request<TrashItem>(`/api/v1/files/${encodeURIComponent(fileId)}`, { method: "DELETE" }, true);
export const fileDownloadURL = (fileId: string) => `/api/v1/files/${encodeURIComponent(fileId)}/content`;
export const fileRenderedPreviewURL = (fileId: string, size: "thumbnail" | "preview") => `/api/v1/files/${encodeURIComponent(fileId)}/preview?size=${size}`;
export const filePreviewURL = (fileId: string) => `${fileDownloadURL(fileId)}?disposition=inline`;
export async function readFileTextPreview(fileId: string) {
  const response = await fetch(fileDownloadURL(fileId));
  if (!response.ok) throw new APIError(response.status, "preview_failed", "无法读取文件预览");
  return response.text();
}
export const listTrash = async () => (await request<{items: TrashItem[]}>("/api/v1/trash")).items;
// RestoreConflict is what a restore does when its name is taken: ask answers
// name_conflict, rename keeps both under "name (恢复)", replace (files only)
// sends the file of that name to the recycle bin.
export type RestoreConflict = "ask" | "rename" | "replace";
export interface RestoredEntry extends FileEntry { replaced?: boolean }
export const restoreTrash = (id: string, name: string, conflict: RestoreConflict = "ask") => request<RestoredEntry>(`/api/v1/trash/${encodeURIComponent(id)}/restore`, json({ parentId: "", name, conflict }), true);
export const purgeTrash = (id: string) => request<void>(`/api/v1/trash/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const listUsers = async () => (await request<{items: User[]}>("/api/v1/users")).items;
export const createMember = (username: string, password: string) => request<User>("/api/v1/users", json({ username, password }), true);
export const resetMember = (id: string, password: string) => request<void>(`/api/v1/users/${encodeURIComponent(id)}/credential`, { ...json({ password }), method: "PATCH" }, true);
export const disableMember = (id: string) => request<void>(`/api/v1/users/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const listVolumes = async () => (await request<{items: Volume[]}>("/api/v1/volumes")).items;
export const createStoragePlan = (diskIds: string[], layout?: VolumeLayout) => request<StoragePlan>("/api/v1/storage/plans", json({ diskIds, layout }), true);
export const continueVolumeConversion = (volumeId: string) => request<void>(`/api/v1/volumes/${encodeURIComponent(volumeId)}/conversion`, json({}), true);
export const confirmStoragePlan = (id: string, confirmationPhrase: string) => request<StoragePlan>(`/api/v1/storage/plans/${encodeURIComponent(id)}/confirm`, json({ confirmationPhrase }), true);
export const executeStoragePlan = (id: string) => request<StoragePlan>(`/api/v1/storage/plans/${encodeURIComponent(id)}/execute`, json({}), true);
export const cancelStoragePlan = (id: string) => request<StoragePlan>(`/api/v1/storage/plans/${encodeURIComponent(id)}/cancel`, json({}), true);
