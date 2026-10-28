import { call, client, rawBody, setSession } from "../api/client";

export const getSetupStatus = () => call(client.GET("/api/v1/setup/status"));
// localConsole asks for a session that lasts until sign-out; the server grants it only to the device's own screen.
const credentials = (username: string, password: string, localConsole: boolean) => ({
  username,
  password,
  ...(localConsole ? { localConsole } : {}),
});
export async function setupAdministrator(username: string, password: string, localConsole = false) {
  const value = await call(client.POST("/api/v1/setup/admin", { body: credentials(username, password, localConsole) }));
  setSession(value);
  return value;
}
export async function login(username: string, password: string, localConsole = false) {
  const value = await call(client.POST("/api/v1/session", { body: credentials(username, password, localConsole) }));
  setSession(value);
  return value;
}
export async function currentSession() {
  const value = await call(client.GET("/api/v1/session"));
  setSession(value);
  return value;
}
export async function logout() {
  await call(client.DELETE("/api/v1/session"));
  setSession();
}
export const changePassword = (currentPassword: string, newPassword: string) =>
  call(client.POST("/api/v1/session/password", { body: { currentPassword, newPassword } }));
export const updateNickname = (nickname: string) =>
  call(client.PATCH("/api/v1/session/profile", { body: { nickname } }));
// uploadAvatar sends the cropped picture; the server makes the stored avatar from it.
export const uploadAvatar = (picture: Blob) =>
  call(
    client.PUT("/api/v1/session/avatar", {
      ...rawBody(picture),
      headers: { "Content-Type": picture.type || "image/jpeg" },
    }),
  );
export const removeAvatar = () => call(client.DELETE("/api/v1/session/avatar"));
