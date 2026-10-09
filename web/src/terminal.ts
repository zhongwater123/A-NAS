import { apiFetch } from "./api";

export const terminalStatusPath = "/api/v1/terminal";
export const terminalSessionPath = "/api/v1/terminal/session";

export type SessionStatus =
  | { state: "connecting" }
  | { state: "connected" }
  | { state: "ended"; exitCode?: number };

export async function readTerminalEnabled(signal?: AbortSignal): Promise<boolean> {
  const response = await apiFetch(terminalStatusPath, { method: "GET", headers: { Accept: "application/json" }, signal });
  if (!response.ok) throw new Error(`terminal status request failed with status ${response.status}`);
  const value: unknown = await response.json();
  if (typeof value !== "object" || value === null || typeof (value as { enabled?: unknown }).enabled !== "boolean") {
    throw new Error("terminal status response is malformed");
  }
  return (value as { enabled: boolean }).enabled;
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
