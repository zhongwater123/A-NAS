import { call, client } from "../api/client";
import type { CompanionAction, CompanionClaim, CompanionHolder } from "../api.generated";

const bounded = (signal?: AbortSignal) =>
  signal ? AbortSignal.any([signal, AbortSignal.timeout(5000)]) : AbortSignal.timeout(5000);
export const companionAPI = {
  read: (signal?: AbortSignal) => call(client.GET("/api/v1/companion", { signal: bounded(signal) })),
  claim: (body: CompanionClaim, signal?: AbortSignal) =>
    call(client.POST("/api/v1/companion/claim", { body, signal: bounded(signal) })),
  heartbeat: (body: CompanionHolder, signal?: AbortSignal) =>
    call(client.POST("/api/v1/companion/heartbeat", { body, signal: bounded(signal) })),
  release: (body: CompanionHolder, signal?: AbortSignal) =>
    call(client.POST("/api/v1/companion/release", { body, signal: bounded(signal) })),
  act: (body: CompanionAction, signal?: AbortSignal) =>
    call(client.POST("/api/v1/companion/actions", { body, signal: bounded(signal) })),
};
