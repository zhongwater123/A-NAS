import createClient from "openapi-fetch";

import type {
  FileEntry,
  Health,
  HealthFinding,
  HostStateResponse,
  LocalDisplay,
  MetricsResponse,
  Notification,
  paths,
  Session,
  StoragePlan,
  TrashRestore,
  ViewingGrant,
  VolumeLayout,
  WallpaperStyle,
} from "./api.generated";

export type {
  DataSource,
  DiskRole,
  FileEntry,
  Health,
  HealthFinding,
  LocalDisplay,
  Notification,
  PlannedDisk,
  Profile,
  RestoredEntry,
  Session,
  Space,
  StoragePlan,
  Transport,
  TrashItem,
  User,
  ViewingGrant,
  Volume,
  VolumeConversion,
  VolumeLayout,
  Wallpaper,
  WallpaperStyle,
} from "./api.generated";
export type HealthFindingCode = HealthFinding["code"];
export type HostState = HostStateResponse;
export type HostMetrics = MetricsResponse;
// A run of one of the member's VNAS scheduled tasks that finished; at most one waits per task.
export type AssistantTaskNotice = NonNullable<Notification["task"]>;
export type ViewingScope = ViewingGrant["scope"];
// The step a running storage plan is at on the device, or the one it stopped in.
export type StorageStep = NonNullable<StoragePlan["step"]>;

let sessionEndedListener: (() => void) | undefined;

// onSessionEnded lets the signed-in desktop return to the login screen; it
// returns the unsubscribe function.
export function onSessionEnded(listener: () => void): () => void {
  sessionEndedListener = listener;
  return () => {
    if (sessionEndedListener === listener) sessionEndedListener = undefined;
  };
}

// sessionEnded reports a 401 that arrived outside apiFetch, such as on an
// upload part that tus-js-client sent.
export function sessionEnded() {
  sessionEndedListener?.();
}

// A write gate holds every write the page sends (any method but GET, HEAD and
// OPTIONS) until it says whether the write may go: VNAS's controller asks the
// member about the writes its steps on the desktop cause (assistant/webosControl.ts).
export type WriteGate = (method: string, path: string) => Promise<boolean>;
let writeGate: WriteGate | undefined;
export function setWriteGate(gate: WriteGate): () => void {
  writeGate = gate;
  return () => {
    if (writeGate === gate) writeGate = undefined;
  };
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
  const result = await client.GET("/api/v1/host-state", { signal }).catch((error: unknown) => {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw error;
    }
    throw new HostStateError();
  });
  if (!result.response.ok) {
    throw new HostStateError(result.response.status);
  }
  const value: unknown = result.data;
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
    !(
      system.healthFindings === undefined ||
      (Array.isArray(system.healthFindings) && system.healthFindings.every(isHealthFinding))
    ) ||
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
  return (
    isRecord(value) &&
    typeof value.code === "string" &&
    ["warning", "critical"].includes(String(value.severity)) &&
    (value.diskId === undefined || typeof value.diskId === "string") &&
    (value.value === undefined || typeof value.value === "number")
  );
}

function isHealth(value: unknown): value is Health {
  return ["healthy", "warning", "critical", "unknown"].includes(String(value));
}

export async function readHostMetrics(signal?: AbortSignal): Promise<HostMetrics> {
  const { data, response } = await client.GET("/api/v1/metrics", { signal });
  if (!response.ok) throw new HostStateError(response.status);
  const value: unknown = data;
  if (!isHostMetrics(value)) throw new HostStateError();
  return value;
}

function isHostMetrics(value: unknown): value is HostMetrics {
  if (
    !isRecord(value) ||
    !["simulated", "live"].includes(String(value.dataSource)) ||
    typeof value.observedAt !== "string"
  )
    return false;
  const { cpu, memory, network } = value;
  return (
    isRecord(cpu) &&
    isNumberIn(cpu.usagePercent, 0, 100) &&
    isNumberIn(cpu.logicalCores, 1, Infinity) &&
    isRecord(memory) &&
    isNumberIn(memory.totalBytes, 1, Infinity) &&
    isNumberIn(memory.usedBytes, 0, Number(memory.totalBytes)) &&
    isRecord(network) &&
    isNumberIn(network.receiveBytesPerSecond, 0, Infinity) &&
    isNumberIn(network.transmitBytesPerSecond, 0, Infinity)
  );
}

function isNumberIn(value: unknown, min: number, max: number): value is number {
  return typeof value === "number" && value >= min && value <= max;
}

export class APIError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

let csrfToken = "";
export function setSession(session?: Session) {
  csrfToken = session?.csrfToken ?? "";
}
// csrfHeader is the header that carries the session's CSRF token on a request
// of this method: the server checks it on every method but GET and HEAD. The
// typed client, uploads with progress and tus all take it from here.
export function csrfHeader(method: string): Record<string, string> {
  const verb = method.toUpperCase();
  return csrfToken && verb !== "GET" && verb !== "HEAD" ? { "X-CSRF-Token": csrfToken } : {};
}

// The contract requires X-CSRF-Token of every write. The client sends it
// itself, so calls leave it out of their header parameters.
type WithoutCSRF<Operation> = Operation extends { parameters: { header: infer Header } }
  ? Omit<Operation, "parameters"> & {
      parameters: Omit<Operation["parameters"], "header"> & { header?: Omit<Header, "X-CSRF-Token"> };
    }
  : Operation;
type ClientPaths = { [Path in keyof paths]: { [Method in keyof paths[Path]]: WithoutCSRF<paths[Path][Method]> } };

// client calls the product API with the types generated from its contract
// (api.generated.ts, `npm run generate`). It sends what apiFetch sends, with
// the CSRF token on writes; call turns its result into data or an APIError.
// The page's origin is the base because a Request needs an absolute URL.
export const client = createClient<ClientPaths>({
  baseUrl: window.location.origin,
  headers: { Accept: "application/json" },
  fetch: (request) => apiFetch(request),
});
client.use({
  onRequest({ request }) {
    for (const [name, value] of Object.entries(csrfHeader(request.method))) request.headers.set(name, value);
    return request;
  },
});

// call waits for a client call and returns its data; a failure throws an
// APIError with the code and message of the server's error body.
export async function call<Data>(pending: Promise<{ data?: Data; error?: unknown; response: Response }>) {
  const { data, error, response } = await pending;
  if (!response.ok) throw failure(response.status, error);
  return data as Data;
}

function failure(status: number, body: unknown): APIError {
  const detail = isRecord(body) && isRecord(body.error) ? body.error : {};
  return new APIError(
    status,
    typeof detail.code === "string" ? detail.code : "request_failed",
    typeof detail.message === "string" ? detail.message : `请求失败 (${status})`,
  );
}

// uploadForm posts a multipart form as the client would, reporting upload
// progress as a fraction; fetch cannot report it.
export async function uploadForm<T>(
  path: string,
  body: FormData,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<T> {
  await passWriteGate("POST", path);
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", path);
    xhr.setRequestHeader("Accept", "application/json");
    for (const [name, value] of Object.entries(csrfHeader("POST"))) xhr.setRequestHeader(name, value);
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && event.total > 0) onProgress(event.loaded / event.total);
    };
    xhr.onload = () => {
      if (xhr.status === 401) sessionEndedListener?.();
      let body: { error?: { code?: string; message?: string } } | undefined;
      try {
        body = xhr.responseText ? JSON.parse(xhr.responseText) : undefined;
      } catch {
        body = undefined;
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as T);
      else
        reject(
          new APIError(
            xhr.status,
            body?.error?.code ?? "request_failed",
            body?.error?.message ?? `请求失败 (${xhr.status})`,
          ),
        );
    };
    xhr.onerror = () => reject(new APIError(0, "network_error", "网络连接中断"));
    xhr.onabort = () => reject(new DOMException("upload aborted", "AbortError"));
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(body);
  });
}

// rawBody sends a Blob or a FormData as it is. The contract types binary
// content as a string, which a Blob is not, so the typed body is set aside.
export function rawBody(content: Blob | FormData) {
  return { body: content as never, bodySerializer: () => content };
}

const id = (value: string) => encodeURIComponent(value);

export const getSetupStatus = () => call(client.GET("/api/v1/setup/status"));
// localConsole asks for a session that lasts until sign-out; the server grants it only to the device's own screen.
const credentials = (username: string, password: string, localConsole: boolean) => ({
  username,
  password,
  ...(localConsole ? { localConsole } : {}),
});
export async function setupAdministrator(username: string, password: string, localConsole = false) {
  const value = await call(client.POST("/api/v1/setup/admin", { body: credentials(username, password, localConsole) }));
  setSession(value);
  return value;
}
export async function login(username: string, password: string, localConsole = false) {
  const value = await call(client.POST("/api/v1/session", { body: credentials(username, password, localConsole) }));
  setSession(value);
  return value;
}
export async function currentSession() {
  const value = await call(client.GET("/api/v1/session"));
  setSession(value);
  return value;
}
export const changePassword = (currentPassword: string, newPassword: string) =>
  call(client.POST("/api/v1/session/password", { body: { currentPassword, newPassword } }));
export const updateNickname = (nickname: string) =>
  call(client.PATCH("/api/v1/session/profile", { body: { nickname } }));
// uploadAvatar sends the cropped picture; the server makes the stored avatar from it.
export const uploadAvatar = (picture: Blob) =>
  call(
    client.PUT("/api/v1/session/avatar", {
      ...rawBody(picture),
      headers: { "Content-Type": picture.type || "image/jpeg" },
    }),
  );
export const removeAvatar = () => call(client.DELETE("/api/v1/session/avatar"));
// readLocalDisplay needs no session: the sign-in page on the local console reads it too.
export const readLocalDisplay = () => call(client.GET("/api/v1/local-display"));
export const updateLocalDisplay = (sleepAfterMinutes: LocalDisplay["sleepAfterMinutes"]) =>
  call(client.PUT("/api/v1/local-display", { body: { sleepAfterMinutes } }));
export const readWallpaper = () => call(client.GET("/api/v1/wallpaper"));
export const updateWallpaperStyle = (style: WallpaperStyle) =>
  call(client.PATCH("/api/v1/wallpaper", { body: { style } }));
export const uploadWallpaper = (picture: Blob) =>
  call(
    client.PUT("/api/v1/wallpaper/image", {
      ...rawBody(picture),
      headers: { "Content-Type": picture.type || "image/jpeg" },
    }),
  );
export const resetWallpaper = () => call(client.DELETE("/api/v1/wallpaper/image"));
// A version names one picture for good, so the browser keeps it cached.
export const wallpaperImageURL = (version: string) => `/api/v1/wallpaper/image?v=${id(version)}`;
export const listProfiles = async () => (await call(client.GET("/api/v1/profiles"))).items;
// A version names one picture for good, so the browser keeps it cached.
export const avatarURL = (userId: string, version: string) => `/api/v1/users/${id(userId)}/avatar?v=${id(version)}`;
export const listNotifications = async () => (await call(client.GET("/api/v1/notifications"))).items;
export const acknowledgeNotification = (notificationId: string) =>
  call(client.POST("/api/v1/notifications/{notificationId}/acknowledge", { params: { path: { notificationId } } }));
export const startViewing = (userId: string, password: string, reason: string, scope: ViewingScope = "space") =>
  call(
    client.POST("/api/v1/users/{userId}/viewing", { params: { path: { userId } }, body: { password, reason, scope } }),
  );
export const endViewing = (grantId: string) =>
  call(client.DELETE("/api/v1/viewing/{grantId}", { params: { path: { grantId } } }));
export async function logout() {
  await call(client.DELETE("/api/v1/session"));
  setSession();
}
export const listSpaces = async () => (await call(client.GET("/api/v1/spaces"))).items;
export const listEntries = async (spaceId: string, parentId = "") =>
  (await call(client.GET("/api/v1/spaces/{spaceId}/entries", { params: { path: { spaceId }, query: { parentId } } })))
    .items;
export const createDirectory = (spaceId: string, parentId: string, name: string) =>
  call(
    client.POST("/api/v1/spaces/{spaceId}/directories", { params: { path: { spaceId } }, body: { parentId, name } }),
  );
export function uploadFile(spaceId: string, parentId: string, file: File) {
  const body = new FormData();
  body.set("parentId", parentId);
  body.set("file", file);
  return call(client.POST("/api/v1/spaces/{spaceId}/uploads", { params: { path: { spaceId } }, ...rawBody(body) }));
}
// sendFile uploads like uploadFile and reports progress as a fraction; parentId must precede the file in the form.
export function sendFile(
  spaceId: string,
  parentId: string,
  file: File,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
) {
  const body = new FormData();
  body.set("parentId", parentId);
  body.set("file", file);
  return uploadForm<FileEntry>(`/api/v1/spaces/${id(spaceId)}/uploads`, body, onProgress, signal);
}
// Without spaceId the target folder is in the entry's own space.
export const moveFile = (fileId: string, parentId: string, name: string, spaceId?: string) =>
  call(client.PATCH("/api/v1/files/{fileId}", { params: { path: { fileId } }, body: { spaceId, parentId, name } }));
export const copyFile = (fileId: string, parentId: string, name: string, spaceId?: string) =>
  call(
    client.POST("/api/v1/files/{fileId}/copies", { params: { path: { fileId } }, body: { spaceId, parentId, name } }),
  );
export const deleteFile = (fileId: string) =>
  call(client.DELETE("/api/v1/files/{fileId}", { params: { path: { fileId } } }));
export const fileDownloadURL = (fileId: string) => `/api/v1/files/${id(fileId)}/content`;
export const fileRenderedPreviewURL = (fileId: string, size: "thumbnail" | "preview") =>
  `/api/v1/files/${id(fileId)}/preview?size=${size}`;
export const filePreviewURL = (fileId: string) => `${fileDownloadURL(fileId)}?disposition=inline`;
export async function readFileTextPreview(fileId: string) {
  const { data, response } = await client.GET("/api/v1/files/{fileId}/content", {
    params: { path: { fileId } },
    parseAs: "text",
  });
  if (!response.ok) throw new APIError(response.status, "preview_failed", "无法读取文件预览");
  return data ?? "";
}
export const listTrash = async () => (await call(client.GET("/api/v1/trash"))).items;
// RestoreConflict is what a restore does when its name is taken: ask answers
// name_conflict, rename keeps both under "name (恢复)", replace (files only)
// sends the file of that name to the recycle bin.
export type RestoreConflict = TrashRestore["conflict"];
export const restoreTrash = (trashId: string, name: string, conflict: RestoreConflict = "ask") =>
  call(
    client.POST("/api/v1/trash/{trashId}/restore", {
      params: { path: { trashId } },
      body: { parentId: "", name, conflict },
    }),
  );
export const purgeTrash = (trashId: string) =>
  call(client.DELETE("/api/v1/trash/{trashId}", { params: { path: { trashId } } }));
export const listUsers = async () => (await call(client.GET("/api/v1/users"))).items;
export const createMember = (username: string, password: string) =>
  call(client.POST("/api/v1/users", { body: { username, password } }));
export const resetMember = (userId: string, password: string) =>
  call(client.PATCH("/api/v1/users/{userId}/credential", { params: { path: { userId } }, body: { password } }));
export const disableMember = (userId: string) =>
  call(client.DELETE("/api/v1/users/{userId}", { params: { path: { userId } } }));
export const listVolumes = async () => (await call(client.GET("/api/v1/volumes"))).items;
export const createStoragePlan = (diskIds: string[], layout?: VolumeLayout) =>
  call(client.POST("/api/v1/storage/plans", { body: { diskIds, layout } }));
export const continueVolumeConversion = (volumeId: string) =>
  call(client.POST("/api/v1/volumes/{volumeId}/conversion", { params: { path: { volumeId } } }));
export const confirmStoragePlan = (planId: string, confirmationPhrase: string) =>
  call(
    client.POST("/api/v1/storage/plans/{planId}/confirm", {
      params: { path: { planId } },
      body: { confirmationPhrase },
    }),
  );
// executeStoragePlan starts a confirmed plan, or retries one that needs
// attention; the device runs it on whether or not this page stays open.
export const executeStoragePlan = (planId: string) =>
  call(client.POST("/api/v1/storage/plans/{planId}/execute", { params: { path: { planId } } }));
export const getStoragePlan = (operationId: string) =>
  call(client.GET("/api/v1/operations/{operationId}", { params: { path: { operationId } } }));
// listUnfinishedStoragePlans finds a plan running on the device, or one that
// stopped, after the page was closed or reloaded.
export const listUnfinishedStoragePlans = async () => (await call(client.GET("/api/v1/storage/plans"))).items;
export const cancelStoragePlan = (planId: string) =>
  call(client.POST("/api/v1/storage/plans/{planId}/cancel", { params: { path: { planId } } }));
