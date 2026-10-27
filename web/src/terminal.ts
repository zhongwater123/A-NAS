import { APIError, call, client } from "./api";

export const terminalStatusPath = "/api/v1/terminal";
export const terminalSessionPath = "/api/v1/terminal/session";

export type SessionStatus = { state: "connecting" } | { state: "connected" } | { state: "ended"; exitCode?: number };

export async function readTerminalEnabled(signal?: AbortSignal): Promise<boolean> {
  const data = await call(client.GET("/api/v1/terminal", { signal }));
  if (!data) throw new APIError(200, "malformed_response");
  return data.enabled;
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
