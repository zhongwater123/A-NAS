// Accounts: the signed-in user's own password, nickname and avatar, and the
// members an administrator manages in 设置 → 用户与权限.
import type { CredentialInput, PasswordChange, ProfileChange, User, ViewingRequest } from "../../api.generated";
import { user } from "../fixtures";
import { apiError, http, pathOf } from "../server";

// alice is a member; the fixtures' session signs in owner, an administrator.
export const alice = user({ id: "user:alice", username: "alice", role: "member" });

// accountHandlers answers the signed-in account's own changes as the server
// answers signedIn, and records what was sent. A nickname that is another
// account's name, such as owner, is refused.
export function accountHandlers(signedIn: User, taken: string[] = ["owner"]) {
  const sent = {
    passwords: [] as PasswordChange[],
    profiles: [] as ProfileChange[],
    avatars: [] as Array<{ type: string | null; csrf: string | null; bytes: string }>,
    avatarRemovals: 0,
  };
  const handlers = [
    http.post("/api/v1/session/password", async ({ request, response }) => {
      sent.passwords.push(await request.json());
      return response(204).empty();
    }),
    http.patch("/api/v1/session/profile", async ({ request, response }) => {
      const change = await request.json();
      sent.profiles.push(change);
      if (taken.includes(change.nickname)) return response(409).json(apiError("nickname_unavailable"));
      return response(200).json({ ...signedIn, nickname: change.nickname || undefined });
    }),
    http.put("/api/v1/session/avatar", async ({ request, response }) => {
      sent.avatars.push({
        type: request.headers.get("Content-Type"),
        csrf: request.headers.get("X-CSRF-Token"),
        bytes: await (await request.blob()).text(),
      });
      return response(200).json({ ...signedIn, avatarVersion: "0123456789abcdef" });
    }),
    http.delete("/api/v1/session/avatar", ({ response }) => {
      sent.avatarRemovals += 1;
      return response(200).json({ ...signedIn, avatarVersion: undefined });
    }),
  ];
  return { sent, handlers };
}

// membersHandlers answers 用户与权限 with users and accepts what an
// administrator does to them, recording each request with the path the
// client wrote: creating, resetting a password, read-only viewing, disabling.
export function membersHandlers(users: User[]) {
  const sent = {
    created: [] as CredentialInput[],
    resets: [] as Array<{ path: string; body: { password: string } }>,
    viewings: [] as Array<{ path: string; body: ViewingRequest }>,
    disabled: [] as string[],
  };
  const handlers = [
    http.get("/api/v1/users", ({ response }) => response(200).json({ items: users })),
    http.post("/api/v1/users", async ({ request, response }) => {
      const input = await request.json();
      sent.created.push(input);
      return response(201).json(
        user({ id: "user:new", username: input.username.trim().toLowerCase(), role: "member" }),
      );
    }),
    http.patch("/api/v1/users/{userId}/credential", async ({ request, response }) => {
      sent.resets.push({ path: pathOf(request), body: await request.json() });
      return response(204).empty();
    }),
    http.post("/api/v1/users/{userId}/viewing", async ({ params, request, response }) => {
      const body = await request.json();
      sent.viewings.push({ path: pathOf(request), body });
      return response(201).json({
        id: "grant:1",
        scope: body.scope ?? "space",
        spaceId: `space:${params.userId}`,
        ownerUserId: params.userId,
        reason: body.reason,
        grantedAt: "2026-10-07T10:00:00Z",
        expiresAt: "2026-10-08T10:00:00Z",
      });
    }),
    http.delete("/api/v1/users/{userId}", ({ request, response }) => {
      sent.disabled.push(pathOf(request));
      return response(204).empty();
    }),
  ];
  return { sent, handlers };
}
