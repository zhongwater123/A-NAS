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

export class HostStateError extends Error {
  constructor(readonly status?: number) {
    super(status ? `host state request failed with status ${status}` : "host state request failed");
  }
}

export async function readHostState(signal?: AbortSignal): Promise<HostState> {
  let response: Response;
  try {
    response = await fetch("/api/v1/host-state", {
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

export interface User { id: string; username: string; role: "admin" | "member"; status: "pending" | "active" | "disabled" | "error"; createdAt: string }
export interface Session { csrfToken: string; expiresAt: string; user: User }
export interface Space { id: string; kind: "private" | "shared"; name: string; ownerUserId?: string; createdAt: string }
export interface FileEntry { id: string; spaceId: string; parentId?: string; name: string; kind: "file" | "directory"; sizeBytes: number; modifiedAt: string }
export interface TrashItem { id: string; entryId: string; spaceId: string; name: string; deletedBy: string; deletedAt: string }
export interface Volume { id: string; diskId: string; filesystemUuid?: string; capacityBytes?: number; availableBytes?: number; state: "creating" | "available" | "unavailable" | "read_only" }
export interface StoragePlan { id: string; diskId: string; diskModel: string; capacityBytes: number; fingerprint: string; signatures: string[]; confirmationPhrase: string; actions: Array<{kind: string; description: string}>; state: string; expiresAt: string; failure?: string; volume?: Volume }
export interface Snapshot { id: string; spaceId: string; name: string; createdBy: string; createdAt: string }
export interface SnapshotEntry { id: string; snapshotId: string; parentId?: string; name: string; kind: "file" | "directory"; sizeBytes: number }

export class APIError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) { super(message); }
}

let csrfToken = "";
export function setSession(session?: Session) { csrfToken = session?.csrfToken ?? ""; }

async function request<T>(path: string, init: RequestInit = {}, mutation = false): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (mutation && csrfToken) headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(path, { ...init, headers, credentials: "same-origin" });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: { code: "request_failed", message: `请求失败 (${response.status})` } }));
    throw new APIError(response.status, body?.error?.code ?? "request_failed", body?.error?.message ?? "请求失败");
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

const json = (value: unknown): RequestInit => ({ method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });

export const getSetupStatus = () => request<{setupRequired: boolean}>("/api/v1/setup/status");
export async function setupAdministrator(username: string, password: string) { const value = await request<Session>("/api/v1/setup/admin", json({ username, password })); setSession(value); return value; }
export async function login(username: string, password: string) { const value = await request<Session>("/api/v1/session", json({ username, password })); setSession(value); return value; }
export async function currentSession() { const value = await request<Session>("/api/v1/session"); setSession(value); return value; }
export async function logout() { await request<void>("/api/v1/session", { method: "DELETE" }, true); setSession(); }
export const listSpaces = async () => (await request<{items: Space[]}>("/api/v1/spaces")).items;
export const listEntries = async (spaceId: string, parentId = "") => (await request<{items: FileEntry[]}>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/entries?parentId=${encodeURIComponent(parentId)}`)).items;
export const createDirectory = (spaceId: string, parentId: string, name: string) => request<FileEntry>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/directories`, json({ parentId, name }), true);
export function uploadFile(spaceId: string, parentId: string, file: File) { const body = new FormData(); body.set("parentId", parentId); body.set("file", file); return request<FileEntry>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/uploads`, { method: "POST", body }, true); }
export const deleteFile = (fileId: string) => request<TrashItem>(`/api/v1/files/${encodeURIComponent(fileId)}`, { method: "DELETE" }, true);
export const fileDownloadURL = (fileId: string) => `/api/v1/files/${encodeURIComponent(fileId)}/content`;
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
export const listSnapshots = async (spaceId: string) => (await request<{items: Snapshot[]}>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/snapshots`)).items;
export const createSnapshot = (spaceId: string, name: string) => request<Snapshot>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/snapshots`, json({ name }), true);
export const deleteSnapshot = (id: string) => request<void>(`/api/v1/snapshots/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const listSnapshotEntries = async (id: string, parentId = "") => (await request<{items: SnapshotEntry[]}>(`/api/v1/snapshots/${encodeURIComponent(id)}/entries?parentId=${encodeURIComponent(parentId)}`)).items;
export const restoreSnapshotEntry = (snapshotId: string, entryId: string, name: string) => request<FileEntry>(`/api/v1/snapshots/${encodeURIComponent(snapshotId)}/entries/${encodeURIComponent(entryId)}/restore`, json({ parentId: "", name }), true);
