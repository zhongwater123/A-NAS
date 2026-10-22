// How a scheduled task's schedule and times read in the window. Hermes keeps a
// schedule as a cron expression, an interval in minutes or one time, and
// words it in English; cron expressions are worded by cronstrue's Chinese
// locale.
import cronstrue from "cronstrue";
import "cronstrue/locales/zh_CN";

import type { AssistantTask } from "./assistantApi";
import { whenText } from "./taskTime";

export { whenText };

export function scheduleText(schedule: AssistantTask["schedule"], now = new Date()): string {
  if (schedule.kind === "interval" && schedule.minutes) {
    const minutes = schedule.minutes;
    if (minutes % 1440 === 0) return minutes === 1440 ? "每天" : `每 ${minutes / 1440} 天`;
    if (minutes % 60 === 0) return minutes === 60 ? "每小时" : `每 ${minutes / 60} 小时`;
    return `每 ${minutes} 分钟`;
  }
  if (schedule.kind === "once" && schedule.runAt) return `仅一次 · ${whenText(schedule.runAt, now)}`;
  if (schedule.kind === "cron" && schedule.expr) {
    try {
      const text = cronstrue.toString(schedule.expr, { locale: "zh_CN", use24HourTimeFormat: true });
      // cronstrue says only the time of a daily expression ("在08:00").
      const [, , day, month, weekday] = schedule.expr.trim().split(/\s+/);
      if (day === "*" && month === "*" && weekday === "*" && text.startsWith("在"))
        return `每天 ${text.slice(1).trim()}`;
      return text;
    } catch {
      return schedule.expr;
    }
  }
  return schedule.display;
}
