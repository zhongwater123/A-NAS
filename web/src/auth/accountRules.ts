import { APIError } from "../api/client";

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
  if (reservedNames.has(name) || name.startsWith("a-nas-") || name.startsWith("app-"))
    return `“${name}”留给系统使用，请换一个账号名`;
  return undefined;
}

// passwordProblem says what is wrong with a password being set, or nothing.
// Lengths count characters as the server does, not UTF-16 units.
export function passwordProblem(value: string): string | undefined {
  const length = [...value].length;
  // oxlint-disable-next-line no-control-regex -- control characters are what it looks for
  if (/[\u0000-\u001f\u007f-\u009f]/.test(value)) return "密码不能包含换行、制表符等控制字符";
  if (length < 12) return "密码至少需要 12 个字符";
  if (length > 64 || encoder.encode(value).length > 254) return "密码最多 64 个字符";
  return undefined;
}

export const nicknameRuleText = "最多 24 个字符，可用中文、空格和表情；不能与其他人的账号名或昵称相同";

// nicknameProblem checks a nickname the way the server does after trimming
// it: invisible formatting characters other than the joiner inside emoji are
// refused, so one name cannot be made to look like another.
export function nicknameProblem(value: string): string | undefined {
  if (/[\p{Cc}\p{Cf}\p{Co}\p{Cn}\p{Zl}\p{Zp}]/u.test(value.replaceAll("\u200d", "")))
    return "昵称不能包含换行或不可见的格式字符";
  if ([...value.normalize("NFC").trim().split(/\s+/).join(" ")].length > 24) return "昵称最多 24 个字符";
  return undefined;
}

// accountErrorText words the API's account errors.
export const accountErrors: Record<string, string> = {
  invalid_credentials: "账号或密码不正确",
  invalid_username: usernameText,
  username_unavailable: "这个账号名已被使用、留给系统，或与设备上已有的 Linux 账号重名，请换一个",
  invalid_password: `密码需为 ${passwordRuleText}，不能包含换行等控制字符`,
  password_unchanged: "新密码不能与当前密码相同",
  reauthentication_failed: "密码不正确",
  setup_complete: "设备已经启用，请刷新页面后登录",
  setup_local_only: "只能在 NAS 本机屏幕上（或经 SSH 隧道）启用设备",
  smb_disable_pending:
    "账号已禁用，网页登录已失效；SMB 凭据暂未停用。可以再点一次“禁用账号”重试，产品服务重启时也会自动停用",
  credential_provision_failed: "SMB 凭据没有设置成功，请稍后重试",
  invalid_nickname: "昵称最多 24 个字符，不能包含换行或不可见的格式字符",
  nickname_unavailable: "这个名字已经是其他人的账号名或昵称，请换一个",
  unsupported_image: "这张图片无法读取。请选择 JPEG、PNG、GIF 或 WebP 图片",
  image_too_large: "图片太大了，请选择 12 MB 以内的图片",
};
export function accountErrorText(error: unknown): string | undefined {
  return error instanceof APIError && Object.hasOwn(accountErrors, error.code) ? accountErrors[error.code] : undefined;
}
