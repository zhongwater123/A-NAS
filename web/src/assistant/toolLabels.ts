// What the assistant's tools are called for the member, and what a call works on: the steps of a reply
// (AssistantPanel.tsx, which adds an icon to each) and the companion's bubble (companion/behaviors.ts)
// read them from here. Plain text, nothing drawn.

/** A friendlier name for a call of one of the assistant's tools, and what the call works on. */
export function toolLabel(name: string, args: Record<string, unknown>): { label: string; detail: string } {
  const path = typeof args.path === "string" ? friendlyPath(args.path) : "";
  switch (name) {
    case "read_file":
      return { label: "读取", detail: path };
    case "search_files":
      return {
        label: "搜索",
        detail: [typeof args.pattern === "string" ? `“${args.pattern}”` : "", path && `在 ${path}`]
          .filter(Boolean)
          .join(" "),
      };
    case "write_file":
      return { label: "写入草稿", detail: path };
    case "patch":
      return { label: "修改草稿", detail: path };
    case "mcp__anas__submit_changes":
      return {
        label: "提交更改",
        detail: typeof args.summary === "string" ? args.summary : "等你确认",
      };
    case "terminal":
      return {
        label: "运行命令",
        detail: typeof args.command === "string" ? short(args.command) : "",
      };
    case "process_manage":
      return { label: "管理后台命令", detail: "" };
    case "execute_code":
      return { label: "运行代码", detail: "" };
    case "delegate_task":
      return { label: "分派子任务", detail: typeof args.goal === "string" ? short(args.goal) : "" };
    case "clarify":
      return { label: "问你", detail: "" };
    case "mcp__anas__recent_changes":
      return { label: "查看最近的更改", detail: "" };
    case "mcp__anas__search_photos":
      return { label: "搜索照片", detail: typeof args.query === "string" ? `“${args.query}”` : "" };
    case "mcp__anas__browse_photos":
      return { label: "浏览照片", detail: typeof args.month === "string" ? args.month : "" };
    case "mcp__anas__photo_details":
      return { label: "查看照片信息", detail: "" };
    case "mcp__anas__similar_photos":
      return { label: "找相似的照片", detail: "" };
    case "mcp__anas__look_at_photo":
      return { label: "取出照片画面", detail: "" };
    // A photo's view lands in Hermes's image cache; the question says more than that path.
    case "vision_analyze":
      return {
        label: "看图",
        detail:
          typeof args.question === "string" && args.question
            ? short(args.question)
            : typeof args.image_url === "string"
              ? friendlyPath(args.image_url)
              : "",
      };
    case "video_analyze":
      return {
        label: "看视频",
        detail: typeof args.video_url === "string" ? friendlyPath(args.video_url) : "",
      };
    case "memory":
      return { label: "记住", detail: "" };
    case "session_search":
      return { label: "查找以前的对话", detail: "" };
    case "skill_view":
    case "skills_list":
    case "skill_manage":
      return { label: "查看技能", detail: typeof args.name === "string" ? args.name : "" };
    // The desktop server's tools; the browser_* names are Hermes's own browser tools, which
    // conversations from before the desktop server still hold.
    case "mcp__desktop__snapshot":
    case "browser_snapshot":
      return { label: "查看桌面", detail: "" };
    case "mcp__desktop__look":
      return { label: "看桌面画面", detail: "" };
    case "browser_vision":
      return {
        label: "看桌面画面",
        detail: typeof args.question === "string" ? short(args.question) : "",
      };
    case "mcp__desktop__click":
    case "browser_click":
      return { label: "点击", detail: typeof args.ref === "string" ? args.ref : "" };
    case "mcp__desktop__type":
    case "browser_type":
      return {
        label: "输入",
        detail: typeof args.text === "string" ? `“${short(args.text)}”` : "",
      };
    case "mcp__desktop__press":
    case "browser_press":
      return { label: "按键", detail: typeof args.key === "string" ? args.key : "" };
    case "mcp__desktop__scroll":
    case "browser_scroll":
      return { label: "滚动", detail: args.direction === "up" ? "向上" : "向下" };
    default:
      return { label: name, detail: "" };
  }
}

/** The first line of a text, cut at 60 characters. */
export function short(text: string) {
  const line = text.split("\n")[0];
  return line.length > 60 ? `${line.slice(0, 60)}…` : line;
}

/** A path under the member's files as the member sees it. */
export function friendlyPath(path: string) {
  return path.replace(/^\/nas\//, "").replace(/^助手\/待确认\//, "草稿/");
}
