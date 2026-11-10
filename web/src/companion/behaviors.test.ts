import { afterEach, describe, expect, it, vi } from "vitest";

import { BEHAVIORS, type Behavior, behaviorLine, behaviorOfTool, shortDetail, toolStep } from "./behaviors";

afterEach(() => vi.restoreAllMocks());

describe("each step of a VNAS turn, as the companion shows it", () => {
  it("tells each of VNAS's tools by what it does", () => {
    const tools: Record<Behavior, string[]> = {
      read: ["read_file", "skill_view", "skills_list", "mcp__anas__recent_changes", "mcp__anas__photo_details"],
      search: ["search_files", "mcp__anas__search_photos", "mcp__anas__similar_photos", "session_search"],
      write: ["write_file", "patch"],
      remember: ["memory"],
      run: ["terminal", "execute_code", "process_manage"],
      look: ["vision_analyze", "video_analyze", "mcp__anas__look_at_photo", "mcp__anas__browse_photos"],
      "desktop-look": ["browser_snapshot", "browser_vision"],
      "desktop-operate": ["browser_click", "browser_type", "browser_press", "browser_scroll"],
      delegate: ["delegate_task"],
      schedule: ["cronjob_manage"],
      submit: ["mcp__anas__submit_changes"],
      waiting: ["clarify"],
      "other-tool": ["skill_manage", "some_new_tool", "toString"],
      thinking: [],
      "step-done": [],
      success: [],
      error: [],
      stopped: [],
    };
    for (const [behavior, names] of Object.entries(tools))
      for (const name of names) expect([name, behaviorOfTool(name)]).toEqual([name, behavior]);
  });

  it("names what a step works on in a few characters", () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    expect(toolStep("terminal", { command: "ls -la /nas/照片/2026 年的旅行" })).toEqual({
      behavior: "run",
      detail: "ls -la /nas/照片/2026 年的旅行",
    });
    expect(shortDetail("ls -la /nas/照片/2026 年的旅行")).toBe("ls -la /nas/…");
    expect(shortDetail("  短\n命令 ")).toBe("短 命令");
    expect(behaviorLine("run", "ls -la /nas/照片/2026 年的旅行", false)).toBe("跑一下 ls -la /nas/…，稍等哦");
    // A step with nothing to name says a line that names nothing.
    expect(behaviorLine("run", "", false)).toBe("噼里啪啦敲命令中~");
  });

  it("shows a detail as plain text", () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    // Not replacement patterns.
    expect(behaviorLine("run", "printf $'\\n'", false)).toBe("跑一下 printf $'\\n'，稍等哦");
    expect(behaviorLine("run", "a$&b$`c$$", false)).toBe("跑一下 a$&b$`c$$，稍等哦");
    // No control characters, and nothing that turns the rest of the line around.
    expect(shortDetail("a\u202Etxt.exe")).toBe("atxt.exe");
    expect(shortDetail("\u2066x\u2069\u200E\u200F\u061C\u202Ay")).toBe("xy");
    expect(shortDetail("bell\u0007\u0000\u007F\u009Bend")).toBe("bellend");
    expect(shortDetail("a\u2028b\u2029c")).toBe("a b c");
  });

  it("does not name what VNAS does on the desktop", () => {
    // A click's detail is an element's ref; typing, a key and scrolling are no click.
    for (const roll of [0, 0.99]) {
      vi.spyOn(Math, "random").mockReturnValue(roll);
      expect(behaviorLine("desktop-operate", "向下", false)).not.toContain("向下");
    }
  });

  it("names nothing on the local console's shared screen", () => {
    for (const roll of [0, 0.99]) {
      vi.spyOn(Math, "random").mockReturnValue(roll);
      for (const behavior of Object.keys(BEHAVIORS) as Behavior[]) {
        const line = behaviorLine(behavior, "家庭账本.xlsx", true);
        // Every behaviour has a line for it.
        expect(line).toBeTruthy();
        expect(line).not.toContain("家庭账本");
        expect(line).not.toContain("{detail}");
      }
    }
  });
});
