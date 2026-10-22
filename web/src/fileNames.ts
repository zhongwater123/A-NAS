import { APIError } from "./api";

// The rules for names a user chooses, as internal/files/names.go enforces
// them: they must fit the file system and work over SMB on Windows and macOS.

export const nameTooLongText = "名称太长：最多 255 字节，约 85 个汉字";
export const nameNotPortableText =
  '名称不能包含 \\ / : * ? " < > | 或控制字符，不能以空格或句点结尾，也不能是 CON、NUL 等系统保留名';

const reserved = new Set([
  "CON",
  "PRN",
  "AUX",
  "NUL",
  ...[1, 2, 3, 4, 5, 6, 7, 8, 9].flatMap((digit) => [`COM${digit}`, `LPT${digit}`]),
]);
const encoder = new TextEncoder();

// nameProblem says what is wrong with a name before it is sent, or nothing.
export function nameProblem(name: string): string | undefined {
  if (!name.trim() || name === "." || name === "..") return "请输入名称";
  if (name.startsWith(".a-nas-")) return "以 .a-nas- 开头的名称留给 A-NAS 自己使用";
  if (encoder.encode(name).length > 255) return nameTooLongText;
  if (/[\\/:*?"<>|\u0000-\u001f\u007f]/.test(name) || /[ .]$/.test(name)) return nameNotPortableText;
  if (reserved.has(name.split(".")[0].trimEnd().toUpperCase())) return nameNotPortableText;
  return undefined;
}

// nameErrorText words the API's name errors.
export function nameErrorText(error: unknown): string | undefined {
  if (!(error instanceof APIError)) return undefined;
  switch (error.code) {
    case "name_too_long":
      return nameTooLongText;
    case "name_not_portable":
      return nameNotPortableText;
    case "name_conflict":
      return "这里已有同名项目（不区分大小写）";
  }
  return undefined;
}
