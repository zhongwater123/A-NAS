import { APIError, call, client } from "../api/client";

export const terminalStatusPath = "/api/v1/terminal";
export const terminalSessionPath = "/api/v1/terminal/session";

export type SessionStatus = { state: "connecting" } | { state: "connected" } | { state: "ended"; exitCode?: number };

// readTerminalAvailability says whether the terminal can be opened from here:
// it may be turned off, and the internet entry never offers it (ADR 0032).
export async function readTerminalAvailability(signal?: AbortSignal): Promise<"enabled" | "disabled" | "internet"> {
  const data = await call(client.GET("/api/v1/terminal", { signal }));
  if (!data) throw new APIError(200, "malformed_response");
  if (data.internet) return "internet";
  return data.enabled ? "enabled" : "disabled";
}

export function terminalSessionURL(location: Pick<Location, "protocol" | "host"> = window.location): string {
  return `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${terminalSessionPath}`;
}

export function resizeMessage(cols: number, rows: number): string {
  return JSON.stringify({ type: "resize", cols, rows });
}

// The server closes with reason "exit <code>" when the shell itself exits.
export function exitCodeFromReason(reason: string): number | undefined {
  const match = /^exit (-?\d+)$/.exec(reason);
  return match ? Number(match[1]) : undefined;
}
