export type LocalCommand =
  | { kind: "focus" | "break"; minutes?: number; invalid?: boolean }
  | { kind: "todo" | "complete"; text: string }
  | { kind: "pause" | "resume" | "stop" | "list" | "cancel" };

// Deliberately bounded user commands, not instructions extracted from AI replies.
// A question or a compound plan falls through to VNAS instead of doing an action.
const LIST =
  /^(?:看看待办|查看待办|查看待办清单|我的待办|看看我的待办|待办列表|待办清单|待办有哪些|待办有什么|有哪些待办|有什么待办|还有哪些待办|还有什么待办|今天有什么待办|待办还有哪些|待办呢|我的待办呢)$/u;
// Asking about the command itself, not content to note: "帮我记一下，好吗".
const ASKING = /^(?:好吗|行吗|可以吗|了吗|了没|没有)$/u;
const PARTICLES = /^(?:吧|啊|呀|哦|噢|喔|嗯|了|啦|嘛|呗|哈|哟|呐)+$/u;

export function localCommand(input: string): LocalCommand | undefined {
  const raw = input.trim();
  const question = /(?:[?？]|吗|呢)[。！!?？~～…⋯]*$/u.test(raw);
  const text = raw
    .replace(/[。！!?？~～…⋯]+$/u, "")
    .replace(/^(大肥鱼|奶蛙)[，,\s]*/u, "")
    .replace(/^请/u, "");
  if (LIST.test(text)) return { kind: "list" };
  // After a separator the content is the member's, even when it ends like a question
  // ("帮我记一下：问老王周五能来吗"). A bare 待办 or 记下 needs that separator ("待办：买牛奶"), so
  // "待办有什么" is not saved as "有什么"; the spelled-out phrases may run straight on ("帮我记一下写周报").
  const todo =
    /^(?:(?:帮我记(?:一件事|一下)|添加(?:一条)?待办|新增待办)([:：，,\s]*)|(?:记下|待办)([:：，,\s]+|$))(.*)$/u.exec(
      text,
    );
  if (todo) {
    // Only a particle ("帮我记一下吧") is no content: ask what to note.
    const content = todo[3].trim().replace(PARTICLES, "");
    if (!ASKING.test(content) && (todo[1] || todo[2] || !question)) return { kind: "todo", text: content };
  }
  const complete = /^(?:完成待办|待办完成|勾掉待办|标记完成)([:：，,\s]*)(.*)$/u.exec(text);
  if (complete && (complete[1] || !question)) return { kind: "complete", text: complete[2].trim() };
  // Asking is not doing: "记下了吗？" or "专注25分钟好吗" goes to VNAS, it does not save or start anything.
  if (question) return undefined;
  if (/^(取消|算了|先不记了|不记了)$/u.test(text)) return { kind: "cancel" };
  if (/^(暂停|暂停专注|暂停计时|暂停番茄钟)$/u.test(text)) return { kind: "pause" };
  if (/^(继续|继续专注|继续计时|继续番茄钟)$/u.test(text)) return { kind: "resume" };
  if (/^(结束专注|结束本轮|结束计时|停止计时)$/u.test(text)) return { kind: "stop" };
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
