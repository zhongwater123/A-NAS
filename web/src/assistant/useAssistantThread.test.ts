import { describe, expect, it } from "vitest";

import type { SessionMessage } from "./hermes/types/hermes";
import { fromTranscript } from "./useAssistantThread";

// Two turns as Hermes stores them: the second with tool calls and no answer of
// its own, closed by Hermes's failed-turn notice.
const rows = [
  { id: 11, role: "user", content: "打开相册", timestamp: 1_760_000_000 },
  { id: 12, role: "assistant", content: "相册已经打开。", timestamp: 1_760_000_001 },
  { id: 13, role: "user", content: "再打开文件管理", timestamp: 1_760_000_010 },
  {
    id: 14,
    role: "assistant",
    content: "",
    timestamp: 1_760_000_011,
    tool_calls: [{ id: "call_00_b", type: "function", function: { name: "browser_click", arguments: '{"ref":"e2"}' } }],
  },
  {
    id: 15,
    role: "tool",
    tool_call_id: "call_00_b",
    tool_name: "browser_click",
    content: "ok",
    timestamp: 1_760_000_012,
  },
  { id: 16, role: "user", content: "继续", timestamp: 1_760_000_020 },
  { id: 17, role: "assistant", content: "文件管理也打开了。", timestamp: 1_760_000_021 },
] as SessionMessage[];

describe("a stored conversation", () => {
  it("keeps each message's id when the page Hermes serves starts later", () => {
    const whole = fromTranscript(rows);
    // Past 500 rows Hermes serves the latest ones: the oldest drop out of the page.
    const later = fromTranscript(rows.slice(2));
    const ids = (messages: typeof whole) => messages.map((message) => message.id);
    expect(ids(later)).toEqual(ids(whole).slice(2));
    expect(new Set(ids(whole)).size).toBe(whole.length);
  });
});
