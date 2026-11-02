import type { TaskPhase } from "./taskState";

export type PetAction =
  | "idle"
  | "wave"
  | "happy"
  | "concerned"
  | "thinking"
  | "working"
  | "waiting"
  | "left"
  | "right"
  | "breathe";

// This is a presentation adapter. It consumes VNAS state, never infers task
// success or the user's feelings from words in a model reply.
export const taskFeeling: Record<TaskPhase, { action: PetAction; label: string; words: string }> = {
  idle: { action: "idle", label: "自在", words: "我在。无聊的话，陪我玩一小会儿？" },
  connecting: { action: "waiting", label: "等候", words: "还在等连接恢复，我先在这里陪你。" },
  thinking: { action: "thinking", label: "专心", words: "收到啦，让我想一想。" },
  working: { action: "working", label: "认真", words: "正在忙这件事，做好这一轮就叫你。" },
  waiting: { action: "waiting", label: "期待", words: "轮到你决定啦，我陪你一起看看确认卡。" },
  success: { action: "happy", label: "开心", words: "这轮回答完成啦！一起看看结果？" },
  error: { action: "concerned", label: "有点沮丧", words: "这次遇到困难了。没关系，我们可以一起看看原因。" },
  stopped: { action: "idle", label: "放松", words: "好，停下来歇一会儿。我陪着你。" },
};

export function taskPose(phase: TaskPhase, settled: boolean) {
  const feeling = taskFeeling[phase];
  if (!settled || !["success", "error"].includes(phase)) return feeling;
  return {
    ...feeling,
    action: "idle" as const,
    words:
      phase === "success" ? "结果已经在小窝里了。接下来做什么，听你的。" : "先歇一下也好。想继续时，我们再看看这件事。",
  };
}
