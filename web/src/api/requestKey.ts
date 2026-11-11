import { APIError } from "./client";

// requestKey names one attempt at a request, so a retry is not done twice.
// LAN HTTP is a supported entry: randomUUID is restricted to secure contexts,
// but getRandomValues remains available. These IDs deduplicate requests only.
export function requestKey(): string {
  if (typeof globalThis.crypto?.getRandomValues !== "function") {
    throw new APIError(0, "request_id_unavailable");
  }
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
