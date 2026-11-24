// Client for /api/v1/assistant-key (internal/assistantkeyapi): the household's DeepSeek key
// for VNAS, which only administrators save, replace or delete. The key itself is never read
// back (docs/decisions/2026-11-17-assistant-v1-cloud-model.md).
import { call, client } from "../api/client";
import type { AssistantKeyState } from "../api.generated";

export type { AssistantKeyState };

export const readAssistantKey = () => call(client.GET("/api/v1/assistant-key"));

// saveAssistantKey saves a key; acknowledged is the administrator's confirmation that what
// VNAS handles goes to DeepSeek, without which nothing is saved.
export const saveAssistantKey = (key: string, acknowledged: boolean) =>
  call(client.PUT("/api/v1/assistant-key", { body: { key, acknowledged } }));

export const deleteAssistantKey = () => call(client.DELETE("/api/v1/assistant-key"));
