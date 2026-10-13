import { request } from "../api";

export type TransferDirection = "upload" | "download";
export type TransferState = "pending" | "running" | "paused" | "interrupted" | "completed" | "failed" | "canceled";
export interface TransferFolder { id: string; name: string }
export interface TransferLocation { spaceId: string; spaceKind: "private" | "shared"; folders: TransferFolder[] }
export interface TransferFailure { code: string; message: string }

// Transfer mirrors internal/transfers.Transfer.
export interface Transfer {
  id: string;
  direction: TransferDirection;
  kind: "file" | "archive";
  name: string;
  location: TransferLocation;
  totalBytes: number;
  transferredBytes: number;
  bytesPerSecond: number;
  state: TransferState;
  error?: TransferFailure;
  batch?: string;
  relativePath?: string;
  lastModified?: number;
  entryId?: string;
  savedName?: string;
  replaced?: boolean;
  skipped?: number;
  contentUrl?: string;
  createdAt: string;
  updatedAt: string;
  finishedAt?: string;
}

const base = "/api/v1/transfers";
const id = (value: string) => encodeURIComponent(value);
const post = (): RequestInit => ({ method: "POST" });

export const uploadEndpoint = "/api/v1/uploads";
export const uploadURL = (transferId: string) => `${uploadEndpoint}/${id(transferId)}`;
export const listTransfers = async () => (await request<{ items: Transfer[] }>(base)).items;
export const getTransfer = (transferId: string) => request<Transfer>(`${base}/${id(transferId)}`);
export const createDownload = (entryIds: string[]) => request<Transfer>(`${base}/downloads`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ entryIds }) }, true);
export const pauseTransfer = (transferId: string) => request<Transfer>(`${base}/${id(transferId)}/pause`, post(), true);
export const cancelTransfer = (transferId: string) => request<Transfer>(`${base}/${id(transferId)}/cancel`, post(), true);
export const removeTransfer = (transferId: string) => request<void>(`${base}/${id(transferId)}`, { method: "DELETE" }, true);
export const clearFinishedTransfers = () => request<void>(base, { method: "DELETE" }, true);
