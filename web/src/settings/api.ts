import { call, client } from "../api/client";
import type { HealthFinding, StoragePlan, ViewingGrant, VolumeLayout } from "../api.generated";

export type HealthFindingCode = HealthFinding["code"];
export type ViewingScope = ViewingGrant["scope"];
// The step a running storage plan is at on the device, or the one it stopped in.
export type StorageStep = NonNullable<StoragePlan["step"]>;

export const listUsers = async () => (await call(client.GET("/api/v1/users"))).items;
export const createMember = (username: string, password: string) =>
  call(client.POST("/api/v1/users", { body: { username, password } }));
export const resetMember = (userId: string, password: string) =>
  call(client.PATCH("/api/v1/users/{userId}/credential", { params: { path: { userId } }, body: { password } }));
export const disableMember = (userId: string) =>
  call(client.DELETE("/api/v1/users/{userId}", { params: { path: { userId } } }));
export const startViewing = (userId: string, password: string, reason: string, scope: ViewingScope = "space") =>
  call(
    client.POST("/api/v1/users/{userId}/viewing", { params: { path: { userId } }, body: { password, reason, scope } }),
  );
export const endViewing = (grantId: string) =>
  call(client.DELETE("/api/v1/viewing/{grantId}", { params: { path: { grantId } } }));

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
