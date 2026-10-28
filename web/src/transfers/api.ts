import { call, client } from "../api/client";
import type { Transfer, TransferLocation } from "../api.generated";

export type { Transfer, TransferFailure, TransferLocation } from "../api.generated";
export type TransferDirection = Transfer["direction"];
export type TransferState = Transfer["state"];
export type TransferFolder = TransferLocation["folders"][number];

const transfer = (transferId: string) => ({ params: { path: { transferId } } });

export const uploadEndpoint = "/api/v1/uploads";
export const uploadURL = (transferId: string) => `${uploadEndpoint}/${encodeURIComponent(transferId)}`;
export const listTransfers = async () => (await call(client.GET("/api/v1/transfers"))).items;
export const getTransfer = (transferId: string) =>
  call(client.GET("/api/v1/transfers/{transferId}", transfer(transferId)));
export const createDownload = (entryIds: string[]) =>
  call(client.POST("/api/v1/transfers/downloads", { body: { entryIds } }));
export const pauseTransfer = (transferId: string) =>
  call(client.POST("/api/v1/transfers/{transferId}/pause", transfer(transferId)));
export const cancelTransfer = (transferId: string) =>
  call(client.POST("/api/v1/transfers/{transferId}/cancel", transfer(transferId)));
export const removeTransfer = (transferId: string) =>
  call(client.DELETE("/api/v1/transfers/{transferId}", transfer(transferId)));
export const clearFinishedTransfers = () => call(client.DELETE("/api/v1/transfers"));
