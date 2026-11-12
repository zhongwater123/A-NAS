import type { User } from "../api.generated";

// The words the settings sections use for an account's role and state.

export function roleLabel(role: User["role"]): string {
  return role === "admin" ? "管理员" : "成员";
}

export function statusLabel(status: User["status"]): string {
  return { active: "正常", pending: "创建中", disabled: "已禁用", error: "凭据异常" }[status];
}
