import { call, client } from "../api/client";

export const listProfiles = async () => (await call(client.GET("/api/v1/profiles"))).items;
// A version names one picture for good, so the browser keeps it cached.
export const avatarURL = (userId: string, version: string) =>
  `/api/v1/users/${encodeURIComponent(userId)}/avatar?v=${encodeURIComponent(version)}`;
