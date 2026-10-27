import { APIError } from "./client";
import { accountErrors } from "../auth/accountRules";
import { nameErrors } from "../lib/fileNames";
// Only known product copy reaches the interface. Server/browser messages
// remain diagnostic data on APIError, never the fallback presented to users.
const messages: Record<string, string> = {
  photos_unavailable: "相册暂时不可用：数据卷未就绪，或此设备尚未启用相册",
  ai_unavailable: "暂时无法识别图片，请稍后再试",
  ...accountErrors,
  ...nameErrors,
  volume_unavailable: "数据卷当前不可用，请检查存储状态后重试",
  network_error: "网络连接中断，请检查网络后重试",
  authentication_required: "登录已失效或账号不可用，请重新登录",
  csrf_failed: "登录状态已变化，请刷新页面后重试",
  forbidden: "没有权限执行这项操作",
  not_found: "内容已不存在，请刷新后重试",
  insufficient_storage: "数据卷剩余空间不足",
  validation_failed: "填写的内容不符合要求",
  malformed_response: "服务返回的内容不完整，请稍后重试",
  not_allowed_for_vnas: "没有允许 VNAS 这一步，什么都没有改",
  request_failed: "请求失败，请稍后重试",
};
const domains = {
  imageSearch: {
    unsupported_media_type: "无法读取这张图片，换一张试试",
    too_large: "图片太大，换一张小一些的试试",
  },
  photos: {
    unsupported_media_type: "支持 JPEG、PNG、HEIC 照片与 MP4、MOV 等常见视频",
    too_large: "文件超过导入大小上限：照片 256 MiB，视频 4 GiB",
    media_unavailable: "设备的媒体解码服务暂不可用，请稍后重试",
    media_busy: "设备正在同时转换太多视频，请稍后再试",
    forbidden: "你不能修改这张照片",
    conflict: "名称已存在，或照片状态已经变化",
    validation_failed: "名称无效",
    not_found: "照片或相册已不存在",
  },
  shares: {
    request_id_unavailable: "浏览器无法生成请求标识，请更新浏览器后重试。",
    share_not_found: "找不到这次分享，可能已被删除",
    share_recipient_unavailable: "所选成员的账号当前不可用",
    share_inactive: "分享已到期或已撤回",
    invalid_share_request: "请求无效，请刷新后重试",
    share_request_conflict: "请求与之前的提交不一致，请刷新后重试",
    share_busy: "正在处理的分享任务较多，请稍后重试",
    shares_unavailable: "分享服务暂不可用，请稍后重试",
  },
  transfers: {
    file_unreadable: "无法读取本地文件，可能已被移动或删除",
    csrf_failed: "登录状态已变化",
    forbidden: "没有写入这个位置的权限",
    not_found: "目标文件夹已不存在",
    name_conflict: "目标位置已有同名文件",
    folder_blocked: "已有同名文件，无法创建文件夹",
    validation_failed: "文件名无效，或 NAS 不接受这个请求",
    file_too_large: "文件大于数据卷可用空间",
    too_many_uploads: "未完成的上传过多",
    upload_lost: "NAS 上的上传进度已丢失",
    service_restarted: "NAS 服务重启，上传需要重新开始",
    expired: "超过一天没有继续，已自动清除",
    not_started: "浏览器没有开始下载，可能被拦截",
    canceled: "已在其他设备上取消",
    interrupted: "连接中断",
    transfer_state: "传输状态已变化",
    internal_error: "NAS 处理失败",
  },
  backup: {
    forbidden: "没有权限执行这项操作",
    not_found: "快照或文件已不存在，请刷新后重试",
    name_conflict: "目标位置已有同名项目",
    validation_failed: "填写的内容不符合要求",
  },
  trash: {
    forbidden: "只有删除它的人可以恢复或永久删除",
    not_found: "这个项目已不在回收站，请刷新后重试",
    name_conflict: "目标位置已有同名项目",
    validation_failed: "名称不符合要求",
  },
  media: {
    media_unavailable: "影视中心暂时不可用。",
    media_busy: "设备正在为其他人转码，请稍后再试。",
    folders_overlap: "所选文件夹已属于另一个媒体库。",
    name_conflict: "这个文件夹里已经有同名的文件。",
    forbidden: "你没有权限执行这个操作。",
    not_found: "内容不存在或已被移除。",
  },
  apps: {
    plan_changed: "应用清单已变化，请重新确认安装计划",
    app_busy: "另一个应用正在安装或卸载，请稍候",
    already_installed: "该应用已经安装",
    not_installed: "该应用尚未安装",
    port_in_use: "端口已被占用",
    name_in_use: "容器名已被占用",
    policy_violation: "该应用不符合安装安全策略",
    address_pool_missing: "Docker 尚未配置 A-NAS 网络地址池，暂不能安装会新建网络的应用",
    docker_runtime_outdated: "Docker 或 Docker Compose 版本过旧，无法把应用限制在它的文件夹内，请先升级",
    apps_unavailable: "无法连接容器代理",
  },
  music: { not_found: "内容已不存在" },
  storage: {
    volume_busy: "数据卷正在转换，完成后才能加入新硬盘。",
    invalid_layout: "这个冗余方式与硬盘数量不符。",
    layout_too_small: "数据卷上的文件放不进所选的冗余方式，请换一种方式。",
    disk_not_eligible: "这块硬盘不能使用：它可能已被挂载、正被其他阵列使用，或不是 SATA/NVMe 硬盘。",
    plan_state_conflict: "另一个存储计划正在执行，或这个计划已被取消、取代，请等它结束后再试。",
    volume_exists: "设备上已经有一个没有登记的数据卷，可能来自一次没有完成的操作。请重试那次操作来接管它，不要新建。",
    no_conversion: "数据卷已经在转换，或者没有需要转换的内容。",
  },
} satisfies Record<string, Record<string, string>>;
export type ErrorDomain = keyof typeof domains;
export function errorCodeText(code: string, domain?: ErrorDomain): string | undefined {
  const overrides: Record<string, string> | undefined = domain ? domains[domain] : undefined;
  return overrides && Object.hasOwn(overrides, code)
    ? overrides[code]
    : Object.hasOwn(messages, code)
      ? messages[code]
      : undefined;
}
export function errorText(error: unknown, domain?: ErrorDomain): string {
  if (error instanceof DOMException && error.name === "AbortError") return "";
  if (error instanceof TypeError) return messages.network_error;
  if (error instanceof APIError) {
    return (
      errorCodeText(error.code, domain) ??
      (error.status === 401
        ? messages.authentication_required
        : error.status >= 500
          ? "服务暂时不可用，请稍后重试"
          : messages.request_failed)
    );
  }
  return messages.request_failed;
}
