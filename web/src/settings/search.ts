import type { PinyinMatch } from "../music/library";
import type { SettingsSection } from "./SettingsPanel";

// The settings search: what can be found, and which row of which section it
// leads to. An anchor is the data-setting of the row to scroll to.
export interface SearchEntry {
  section: SettingsSection;
  title: string;
  // Words people type that the title does not contain.
  keywords?: string;
  anchor?: string;
  adminOnly?: boolean;
}

export const searchEntries: SearchEntry[] = [
  { section: "account", title: "头像", anchor: "avatar", keywords: "照片 图片" },
  { section: "account", title: "昵称", anchor: "nickname", keywords: "名字 名称 改名" },
  { section: "account", title: "修改密码", anchor: "password", keywords: "口令 smb" },
  { section: "account", title: "登录记录", anchor: "sign-ins", keywords: "登录历史" },
  { section: "account", title: "退出登录", anchor: "sign-out", keywords: "注销 登出" },
  { section: "overview", title: "系统健康", anchor: "health", keywords: "状态 故障 运行时间" },
  { section: "overview", title: "温度与风扇", anchor: "thermal", keywords: "散热 处理器 cpu 转速 发热" },
  { section: "overview", title: "关于本机", anchor: "about", keywords: "版本 操作系统 架构 设备标识 系统盘" },
  { section: "storage", title: "数据卷", anchor: "volume", keywords: "容量 空间 冗余 镜像 初始化 raid" },
  { section: "storage", title: "磁盘", anchor: "disks", keywords: "硬盘 ssd hdd 健康 smart" },
  {
    section: "system-update",
    title: "检查更新",
    anchor: "update-status",
    keywords: "升级 新版本 安装",
    adminOnly: true,
  },
  {
    section: "system-update",
    title: "自动更新",
    anchor: "update-settings",
    keywords: "自动下载 自动安装 时段",
    adminOnly: true,
  },
  {
    section: "system-update",
    title: "回到上一版本",
    anchor: "update-revert",
    keywords: "回退 降级 回滚",
    adminOnly: true,
  },
  { section: "task-manager", title: "设备运行状态", keywords: "cpu 内存 网络 gpu 性能 占用" },
  { section: "task-manager", title: "服务与进程", keywords: "任务 进程 程序" },
  { section: "personalization", title: "桌面壁纸", anchor: "wallpaper", keywords: "背景 图片", adminOnly: true },
  {
    section: "personalization",
    title: "壁纸样式",
    anchor: "wallpaper-style",
    keywords: "填充 适应 拉伸 居中 平铺",
    adminOnly: true,
  },
  { section: "personalization", title: "息屏", anchor: "sleep", keywords: "屏保 黑屏 屏幕 休眠", adminOnly: true },
  { section: "users", title: "添加成员", anchor: "add-member", keywords: "新建 账号 家人 用户", adminOnly: true },
  {
    section: "users",
    title: "成员账号",
    anchor: "members",
    keywords: "重置密码 禁用 只读查看 个人空间 私有图库",
    adminOnly: true,
  },
  { section: "web-search", title: "网页搜索开关", anchor: "web-search", keywords: "上网 搜索", adminOnly: true },
  { section: "web-search", title: "种子搜索", anchor: "torrents", keywords: "bt 磁力 下载", adminOnly: true },
  { section: "web-search", title: "搜索引擎", anchor: "engines", adminOnly: true },
  { section: "web-search", title: "开源许可", anchor: "license", keywords: "searxng 源码 许可证", adminOnly: true },
  { section: "logs", title: "活动记录", keywords: "审计 操作 谁 登录" },
  { section: "logs", title: "系统日志", keywords: "服务 报错 错误 error", adminOnly: true },
  { section: "terminal", title: "终端", keywords: "shell 命令行 bash", adminOnly: true },
];

const normalize = (value: string) => value.normalize("NFKC").toLocaleLowerCase("zh-CN").trim();

// searchSettings returns the entries a query finds, best first: a title that
// starts with it, a title that holds it, a keyword, then pinyin such as "mima".
export function searchSettings(
  entries: SearchEntry[],
  rawQuery: string,
  sectionLabel: (section: SettingsSection) => string,
  pinyin?: PinyinMatch,
): SearchEntry[] {
  const query = normalize(rawQuery);
  if (!query) return [];
  const latin = /^[a-z]+$/.test(query);
  const score = (entry: SearchEntry): number => {
    const title = normalize(entry.title);
    const section = normalize(sectionLabel(entry.section));
    if (title.startsWith(query)) return 5;
    if (title.includes(query)) return 4;
    if (section.includes(query)) return 3;
    if (entry.keywords && normalize(entry.keywords).includes(query)) return 2;
    if (latin && pinyin && (pinyin(entry.title, query) || pinyin(sectionLabel(entry.section), query))) return 1;
    return 0;
  };
  return entries
    .map((entry, index) => ({ entry, index, score: score(entry) }))
    .filter((item) => item.score > 0)
    .sort((left, right) => right.score - left.score || left.index - right.index)
    .map((item) => item.entry)
    .slice(0, 8);
}
