export type LocalCommand =
  | { kind: "focus" | "break"; minutes?: number; invalid?: boolean }
  | { kind: "todo" | "complete"; text: string }
  | { kind: "pause" | "resume" | "stop" | "list" | "cancel" };

// Deliberately bounded user commands, not instructions extracted from AI replies.
// A question or a compound plan falls through to VNAS instead of doing an action.
export function localCommand(input: string): LocalCommand | undefined {
  const text = input
    .trim()
    .replace(/[。！!]+$/u, "")
    .replace(/^(大肥鱼|奶蛙)[，,\s]*/u, "")
    .replace(/^请/u, "");
  if (/^(取消|算了|先不记了|不记了)$/u.test(text)) return { kind: "cancel" };
  if (/^(暂停|暂停专注|暂停计时|暂停番茄钟)$/u.test(text)) return { kind: "pause" };
  if (/^(继续|继续专注|继续计时|继续番茄钟)$/u.test(text)) return { kind: "resume" };
  if (/^(结束专注|结束本轮|结束计时|停止计时)$/u.test(text)) return { kind: "stop" };
  if (/^(看看待办|查看待办|我的待办|待办列表)$/u.test(text)) return { kind: "list" };
  const todo = /^(?:帮我记(?:一件事|一下)|记下|添加(?:一条)?待办|新增待办|待办)[:：，,\s]*(.*)$/u.exec(text);
  if (todo) return { kind: "todo", text: todo[1].trim() };
  const complete = /^(?:完成待办|勾掉待办|标记完成)[:：，,\s]*(.*)$/u.exec(text);
  if (complete) return { kind: "complete", text: complete[1].trim() };
  const focus =
    /^(?:帮我|开始|我想|我要)?(专注|番茄钟|休息)(?:计时)?\s*(-?[\d.]+|[一二两三四五六七八九十]+)?\s*(?:分钟|分)?(?:吧|啦)?$/u.exec(
      text,
    );
  if (!focus) return undefined;
  if (!focus[2]) return { kind: focus[1] === "休息" ? "break" : "focus" };
  const digits: Record<string, number> = { 一: 1, 二: 2, 两: 2, 三: 3, 四: 4, 五: 5, 六: 6, 七: 7, 八: 8, 九: 9 };
  const value = focus[2];
  const chinese = /^([一二两三四五六七八九])?十([一二三四五六七八九])?$/u.exec(value);
  const minutes = /^-?\d+(?:\.\d+)?$/u.test(value)
    ? Number(value)
    : chinese
      ? (chinese[1] ? digits[chinese[1]] : 1) * 10 + (chinese[2] ? digits[chinese[2]] : 0)
      : digits[value];
  return {
    kind: focus[1] === "休息" ? "break" : "focus",
    minutes,
    invalid: !Number.isInteger(minutes) || minutes < 1 || minutes > 90,
  };
}
