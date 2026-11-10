// What VNAS does in a turn, as the companion shows it: each kind of step has a clip of its own, so that
// the member can tell reading from searching or running a command, and the lines its bubble picks from.
// A line with {detail} names what the step works on (behaviorLine); the others do not.
import { toolLabel } from "../assistant/toolLabels";
import { isLocalConsole } from "../desktop/LocalConsoleScreenSaver";

/** A kind of step of a VNAS turn, or how the turn ended. */
export type Behavior =
  | "thinking"
  | "read"
  | "search"
  | "write"
  | "remember"
  | "run"
  | "look"
  | "desktop-look"
  | "desktop-operate"
  | "delegate"
  | "schedule"
  | "submit"
  | "waiting"
  | "other-tool"
  | "step-done"
  | "success"
  | "error"
  | "stopped";

/** Each behaviour's clip and lines. */
export const BEHAVIORS: Record<Behavior, { clip: string; lines: readonly string[] }> = {
  thinking: { clip: "工作状态-思考冒泡", lines: ["正在认真想下一步呢", "嗯……让我想想哦~"] },
  read: { clip: "工作状态-清点归档", lines: ["我在看 {detail} 哦", "一页一页认真读~"] },
  search: { clip: "照镜子", lines: ["帮你找找 {detail}~", "找找看藏在哪里~"] },
  write: { clip: "轻快记录", lines: ["在写 {detail} 的草稿呢", "一笔一笔写好它~"] },
  remember: { clip: "原地跳跃抓碎头顶物品", lines: ["这个我记住啦！", "收进小脑袋里~"] },
  run: { clip: "写代码", lines: ["跑一下 {detail}，稍等哦", "噼里啪啦敲命令中~"] },
  look: { clip: "扑克魔术", lines: ["看看这张照片~", "{detail} 我瞧瞧哦"] },
  "desktop-look": { clip: "东张西望", lines: ["看看桌面上有什么~", "我先瞅一眼屏幕哦"] },
  "desktop-operate": { clip: "下五子棋", lines: ["帮你点一下 {detail}~", "在桌面上动动小手~"] },
  delegate: { clip: "变鸽子", lines: ["派小鸽子去帮忙啦~", "这件交给小帮手咯！"] },
  schedule: { clip: "抽陀螺", lines: ["定好时间啦，到点自己转~", "设个定时任务~"] },
  submit: { clip: "拆礼物", lines: ["改好啦，请你过目~", "给你看看我做的~"] },
  waiting: { clip: "工作状态-原地踱步张望", lines: ["等你回话哦~", "你来定，我乖乖等着"] },
  "other-tool": { clip: "工作状态-忙碌点按", lines: ["用一下小工具 {detail}", "叮叮当当忙一下~"] },
  "step-done": { clip: "工作状态-雀跃庆祝", lines: ["搞定一步，继续看看哦~", "这步好啦！"] },
  success: { clip: "放烟花", lines: ["全部搞定啦！", "任务完成，撒花~"] },
  error: { clip: "工作状态-垂头叹气冒汗", lines: ["呜……这次没做成", "出错了，对不起嘛"] },
  stopped: { clip: "女仆屈膝礼仪", lines: ["好的，停下啦", "听你的，不做了~"] },
};

/**
 * The clips that show a step, which play only for one: not in the random chain, a turn or a click. With
 * them 被吓一跳, kept for a step that failed, which the gateway does not tell (its tool.complete carries
 * no error), so that no step shows it yet.
 */
export const TASK_CLIPS: ReadonlySet<string> = new Set([
  ...Object.values(BEHAVIORS).map((behavior) => behavior.clip),
  "被吓一跳",
]);

/**
 * The behaviour of each of VNAS's tools; any other is other-tool. clarify asks the member, as a card waiting
 * for them does. The companion's own tools (mcp__companion__*) are VNAS moving the pet and show no step of
 * their own (workStatus.ts).
 */
const TOOL_BEHAVIORS: Readonly<Record<string, Behavior>> = {
  read_file: "read",
  skill_view: "read",
  skills_list: "read",
  mcp__anas__recent_changes: "read",
  mcp__anas__photo_details: "read",
  search_files: "search",
  mcp__anas__search_photos: "search",
  mcp__anas__similar_photos: "search",
  session_search: "search",
  write_file: "write",
  patch: "write",
  memory: "remember",
  terminal: "run",
  execute_code: "run",
  process_manage: "run",
  vision_analyze: "look",
  video_analyze: "look",
  mcp__anas__look_at_photo: "look",
  mcp__anas__browse_photos: "look",
  browser_snapshot: "desktop-look",
  browser_vision: "desktop-look",
  browser_click: "desktop-operate",
  browser_type: "desktop-operate",
  browser_press: "desktop-operate",
  browser_scroll: "desktop-operate",
  delegate_task: "delegate",
  cronjob_manage: "schedule",
  mcp__anas__submit_changes: "submit",
  clarify: "waiting",
};

export function behaviorOfTool(name: string): Behavior {
  return Object.hasOwn(TOOL_BEHAVIORS, name) ? TOOL_BEHAVIORS[name] : "other-tool";
}

/** About this many characters of a detail fit in the bubble, which is half the pet wide. */
const DETAIL_MAX = 12;

/** A step's detail cut to what the bubble shows, by code point so that no character is cut in half. */
export function shortDetail(detail: string): string {
  const points = Array.from(detail.replace(/\s+/g, " ").trim());
  return points.length > DETAIL_MAX ? `${points.slice(0, DETAIL_MAX).join("")}…` : points.join("");
}

/**
 * One of the behaviour's lines, {detail} filled with the step's detail cut short. Without a detail, or on
 * the screen the family shares (the local console, as TaskToasts.tsx tells it), only a line that names
 * nothing: what VNAS works on stays for the member who asked. null when the behaviour has no such line.
 */
export function behaviorLine(behavior: Behavior, detail = "", shared = isLocalConsole()): string | null {
  const shown = shared ? "" : shortDetail(detail);
  const lines = BEHAVIORS[behavior].lines.filter((line) => shown !== "" || !line.includes("{detail}"));
  if (lines.length === 0) return null;
  return lines[Math.floor(Math.random() * lines.length)].replace("{detail}", shown);
}

/** The behaviour a call of one of VNAS's tools shows, and what the call works on (toolLabels.ts). */
export function toolStep(name: string, args: unknown): { behavior: Behavior; detail: string } {
  const values = args && typeof args === "object" ? (args as Record<string, unknown>) : {};
  return { behavior: behaviorOfTool(name), detail: toolLabel(name, values).detail };
}
