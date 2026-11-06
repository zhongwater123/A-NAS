import type { WebSearchPausedEngine } from "./api";

export const pauseReasons: Record<WebSearchPausedEngine["reason"], string> = {
  captcha: "要求验证码",
  denied: "拒绝访问",
  too_many: "请求过多",
  unreadable: "返回无法识别的页面",
};
