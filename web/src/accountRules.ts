import { APIError } from "./api";

// The rules for account names and passwords, as internal/accounts enforces
// them: an account name is also the Linux and SMB user name, and the password
// is also the SMB password.

export const usernameRuleText = "3–32 位英文字母、数字、- 或 _，以字母开头，按小写保存";
export const passwordRuleText = "12–64 个字符，可用空格、中文和符号";

const usernameText = `账号名需为 ${usernameRuleText}`;
const reservedNames = new Set(["global", "homes", "printers", "shared", "a-nas"]);
const encoder = new TextEncoder();

// usernameProblem says what is wrong with a new account name before it is
// sent, or nothing.
export function usernameProblem(value: string): string | undefined {
  const name = value.trim().toLowerCase();
  if (!/^[a-z][a-z0-9_-]{2,31}$/.test(name)) return usernameText;
  if (reservedNames.has(name) || name.startsWith("a-nas-") || name.startsWith("app-")) return `“${name}”留给系统使用，请换一个账号名`;
  return undefined;
}

// passwordProblem says what is wrong with a password being set, or nothing.
// Lengths count characters as the server does, not UTF-16 units.
export function passwordProblem(value: string): string | undefined {
  const length = [...value].length;
  if (/[\u0000-\u001f\u007f-\u009f]/.test(value)) return "密码不能包含换行、制表符等控制字符";
  if (length < 12) return "密码至少需要 12 个字符";
  if (length > 64 || encoder.encode(value).length > 254) return "密码最多 64 个字符";
  return undefined;
}

// accountErrorText words the API's account errors.
export function accountErrorText(error: unknown): string | undefined {
  if (!(error instanceof APIError)) return undefined;
  switch (error.code) {
    case "invalid_credentials": return "账号或密码不正确";
    case "invalid_username": return usernameText;
    case "username_unavailable": return "这个账号名已被使用、留给系统，或与设备上已有的 Linux 账号重名，请换一个";
    case "invalid_password": return `密码需为 ${passwordRuleText}，不能包含换行等控制字符`;
    case "password_unchanged": return "新密码不能与当前密码相同";
    case "reauthentication_failed": return "密码不正确";
    case "setup_complete": return "设备已经启用，请刷新页面后登录";
    case "credential_provision_failed": return "SMB 凭据没有设置成功，请稍后重试";
  }
  return undefined;
}
