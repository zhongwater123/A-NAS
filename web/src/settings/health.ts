import { Database, FolderLock, HardDrive, Lock, ShieldAlert, Thermometer, Unplug, type LucideIcon } from "lucide-react";

import type { HealthFinding, HostState } from "../api";

// describeFinding words one reason the system is not plainly healthy
// (docs/specs/system-health.md): what is wrong and what the owner can do.
export function describeFinding(
  finding: HealthFinding,
  disks: HostState["disks"],
): { title: string; detail: string; icon: LucideIcon } {
  const disk = disks.find((candidate) => candidate.id === finding.diskId);
  // A model name is set off by spaces, the fallback is not.
  const lead = disk?.model ? `${disk.model} ` : "一块硬盘";
  const inner = disk?.model ? ` ${disk.model} ` : "一块硬盘";
  const value = finding.value ?? 0;
  switch (finding.code) {
    case "disk_failing":
      return {
        icon: HardDrive,
        title: `${lead}的 SMART 自检未通过`,
        detail: "这块硬盘可能即将损坏。请尽快把重要文件复制到别处，并准备更换硬盘。",
      };
    case "disk_warning":
      return { icon: HardDrive, title: `${lead}报告了需要注意的状况`, detail: "在存储中查看这块硬盘的状态与温度。" };
    case "disk_health_unknown":
      return {
        icon: HardDrive,
        title: `读不到${inner}的健康信息`,
        detail: "硬盘可能休眠了一整天，或不支持 SMART，暂时无法确认它是否正常。",
      };
    case "disk_hot":
      return {
        icon: Thermometer,
        title: `${lead}温度 ${value} °C，偏高`,
        detail: `${disk?.rotational === false ? "固态硬盘最好低于 70 °C" : "机械硬盘最好低于 55 °C"}。请检查机箱通风和风扇，长期高温会缩短硬盘寿命。`,
      };
    case "volume_offline":
      return {
        icon: Unplug,
        title: "数据卷没有挂载",
        detail: "文件、相册和应用数据暂时无法读写。请检查数据盘的连接，然后重启设备。",
      };
    case "volume_read_only":
      return {
        icon: Lock,
        title: "数据卷变成了只读",
        detail: "文件系统发现错误后会转为只读来保护数据，现在无法写入。请先备份重要文件，再检查硬盘。",
      };
    case "volume_full":
      return {
        icon: Database,
        title: `数据卷已用 ${value}%，新文件会被拒绝`,
        detail: "A-NAS 会留出一部分空间防止写满。清理回收站、旧快照或不需要的文件后就能继续写入。",
      };
    case "volume_nearly_full":
      return {
        icon: Database,
        title: `数据卷已用 ${value}%`,
        detail: "空间快用完了。可以清理回收站、旧快照或不需要的文件。",
      };
    case "volume_errors":
      return {
        icon: ShieldAlert,
        title: `数据卷记录到 ${value} 次读写或校验错误`,
        detail: "这是数据卷创建以来的累计次数，通常说明硬盘或线缆有问题。建议备份重要文件并检查硬盘。",
      };
    case "volume_reconcile_failed":
      return {
        icon: FolderLock,
        title: "数据卷的权限与共享设置没能更新",
        detail:
          "设备没能修复空间的访问权限、记录账号清单或刷新 SMB 共享设置，部分文件或共享可能无法正常使用。管理员可在“设置 → 日志”的 Host Agent 中查看原因；设备每 15 分钟重试一次。",
      };
    default:
      return { icon: ShieldAlert, title: "发现一项需要注意的状况", detail: "在存储中查看详情。" };
  }
}
