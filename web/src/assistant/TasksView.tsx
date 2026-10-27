// 定时任务: what VNAS does on its own at set times — Hermes's cron jobs, which
// the member makes by asking in a conversation. Each task shows when it runs
// next, how its last run went and its runs, each a conversation to read or
// continue; the member can run it now, pause, resume or delete it. Nothing
// here makes or edits a task: that stays in the member's own words.
import {
  CalendarClock,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  CircleAlert,
  CircleCheck,
  History,
  Info,
  LoaderCircle,
  MessageSquarePlus,
  Pause,
  Play,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import {
  type AssistantTask,
  type AssistantTaskRun,
  deleteTask,
  failureText,
  listTasks,
  pauseTask,
  resumeTask,
  runTask,
  taskRuns,
} from "./api";
import { loadTaskNotices, useTaskNotices } from "./taskNotices";
import { scheduleText, whenText } from "./taskSchedule";

// What a member might hand VNAS to do at set times; picking one asks for it in a new conversation.
const IDEAS = [
  "每天早上 8 点，告诉我共享空间昨天新增了哪些文件",
  "每周日晚上 9 点，看看我的个人空间里有没有重复的大文件，列给我",
  "今天下午 3 点提醒我交电费",
];

export function TasksView({
  onOpenRun,
  onPlan,
}: {
  onOpenRun(id: string, title: string): void;
  onPlan(text?: string): void;
}) {
  const [tasks, setTasks] = useState<AssistantTask[]>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(() => {
    void loadTaskNotices();
    return listTasks()
      .then((list) => {
        setTasks(list);
        setError("");
      })
      .catch((reason) => setError(failureText(reason)));
  }, []);
  const unread = new Set(useTaskNotices().map((item) => item.task?.taskId));
  useEffect(() => {
    void load();
  }, [load]);
  // A task runs on its own while the page is open; a running one is followed closely.
  const running = Boolean(tasks?.some((task) => task.running));
  useEffect(() => {
    const timer = window.setInterval(() => void load(), running ? 4000 : 30_000);
    return () => window.clearInterval(timer);
  }, [load, running]);

  const replace = (updated: AssistantTask) =>
    setTasks((list) => list?.map((task) => (task.id === updated.id ? updated : task)));
  const act = async (work: () => Promise<void>) => {
    try {
      await work();
    } catch (reason) {
      setNotice(failureText(reason));
      void load();
    }
  };
  const run = (task: AssistantTask) =>
    act(async () => {
      replace({ ...task, running: true });
      const result = await runTask(task.id);
      if (result.finished && result.task) {
        replace(result.task);
        setNotice(`“${task.name}”运行完成`);
      } else {
        setNotice(`“${task.name}”已开始运行，完成后会通知你`);
        void load();
      }
    });
  const pause = (task: AssistantTask) => act(async () => replace(await pauseTask(task.id)));
  const resume = (task: AssistantTask) => act(async () => replace(await resumeTask(task.id)));
  const remove = (task: AssistantTask) =>
    act(async () => {
      await deleteTask(task.id);
      setTasks((list) => list?.filter((item) => item.id !== task.id));
      setNotice(`已删除“${task.name}”`);
    });

  if (!tasks) {
    return (
      <div className="as-memory as-tasks">
        {error ? (
          <div className="as-error" role="alert">
            <CircleAlert />
            <span>{error}</span>
          </div>
        ) : (
          <div className="as-loading">
            <LoaderCircle />
            正在读取…
          </div>
        )}
      </div>
    );
  }
  const ordered = [...tasks].sort((a, b) => rank(a) - rank(b) || (a.nextRunAt ?? Infinity) - (b.nextRunAt ?? Infinity));
  return (
    <div className="as-memory as-tasks">
      <section className="as-memory-intro">
        <span className="as-memory-mark">
          <CalendarClock />
        </span>
        <div>
          <h3>定时任务</h3>
          <p>在对话里告诉 VNAS 什么时候做什么，它就会按时去做；做完会在桌面上提醒你，结果在这里查看。</p>
        </div>
        {tasks.length > 0 && (
          <button type="button" className="as-task-plan" onClick={() => onPlan()}>
            <MessageSquarePlus />
            安排新任务
          </button>
        )}
      </section>
      <p className="as-memory-note">
        <ShieldCheck />
        <span>
          定时任务运行时你不一定在场，所以它只查看内容、在“助手”文件夹里写东西。需要改动你的文件时，这一步会被拒绝，结果里会说明，你可以回到对话里确认。
        </span>
      </p>
      {notice && (
        <div className="as-memory-notice" role="status">
          <span>{notice}</span>
          <button type="button" aria-label="关闭" onClick={() => setNotice("")}>
            <X />
          </button>
        </div>
      )}
      {error && (
        <div className="as-memory-error" role="alert">
          <CircleAlert />
          {error}
        </div>
      )}

      {tasks.length === 0 ? (
        <div className="as-tasks-empty">
          <CalendarClock />
          <strong>还没有定时任务</strong>
          <span>说一句话就能安排，比如：</span>
          <div className="as-suggestions">
            {IDEAS.map((text) => (
              <button type="button" key={text} onClick={() => onPlan(text)}>
                {text}
                <ChevronRight />
              </button>
            ))}
          </div>
        </div>
      ) : (
        <ul className="as-task-list">
          {ordered.map((task) => (
            <TaskCard
              key={task.id}
              task={task}
              fresh={unread.has(task.id)}
              onRun={() => void run(task)}
              onPause={() => void pause(task)}
              onResume={() => void resume(task)}
              onDelete={() => void remove(task)}
              onOpenRun={onOpenRun}
              onFailure={setNotice}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

// Running and waiting tasks come first, then paused ones, then the ones that will not run again.
function rank(task: AssistantTask): number {
  if (task.running) return 0;
  return { scheduled: 1, error: 2, paused: 3, completed: 4 }[task.state] ?? 2;
}

function TaskCard({
  task,
  fresh,
  onRun,
  onPause,
  onResume,
  onDelete,
  onOpenRun,
  onFailure,
}: {
  task: AssistantTask;
  fresh: boolean;
  onRun(): void;
  onPause(): void;
  onResume(): void;
  onDelete(): void;
  onOpenRun(id: string, title: string): void;
  onFailure(text: string): void;
}) {
  const [runs, setRuns] = useState<AssistantTaskRun[]>();
  const [showRuns, setShowRuns] = useState(false);
  const [asking, setAsking] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const paused = task.state === "paused";
  const done = task.state === "completed";
  const failed = Boolean(task.lastRunAt && task.lastStatus && task.lastStatus !== "ok");

  const loadRuns = useCallback(
    () =>
      taskRuns(task.id).then((list) => {
        setRuns(list);
        return list;
      }),
    [task.id],
  );
  // A finished run changes the list.
  useEffect(() => {
    if (showRuns) void loadRuns().catch(() => undefined);
  }, [showRuns, loadRuns, task.lastRunAt]);
  const runTitle = (run: AssistantTaskRun) => `${task.name} · ${whenText(run.startedAt)}`;
  const openLatest = async () => {
    try {
      const latest = (runs ?? (await loadRuns())).find((run) => run.id);
      if (latest?.id) onOpenRun(latest.id, runTitle(latest));
      else onFailure("这次运行没有留下对话");
    } catch (reason) {
      onFailure(failureText(reason));
    }
  };

  const state = task.running
    ? { key: "running", text: "正在运行" }
    : paused
      ? { key: "paused", text: "已暂停" }
      : done
        ? { key: "done", text: "已完成" }
        : task.state === "error"
          ? { key: "failed", text: "无法安排" }
          : { key: "on", text: "已开启" };
  const next = task.running
    ? "正在运行"
    : paused
      ? "暂停中，不会运行"
      : done
        ? "不再运行"
        : task.nextRunAt
          ? whenText(task.nextRunAt)
          : "—";

  return (
    <li className={`as-task ${state.key}`}>
      <header className="as-task-head">
        <div>
          <strong>{task.name}</strong>
          <span className="as-task-schedule">
            <CalendarClock />
            {scheduleText(task.schedule)}
          </span>
        </div>
        <span className={`as-task-state ${state.key}`}>
          {task.running && <LoaderCircle />}
          {state.text}
        </span>
      </header>
      <p className={`as-task-prompt${expanded ? "" : " clamped"}`}>{task.prompt}</p>
      {task.prompt.length > 90 && (
        <button type="button" className="as-task-more" onClick={() => setExpanded((open) => !open)}>
          {expanded ? (
            <>
              <ChevronUp />
              收起
            </>
          ) : (
            <>
              <ChevronDown />
              展开
            </>
          )}
        </button>
      )}
      <dl className="as-task-facts">
        <div>
          <dt>下次运行</dt>
          <dd>{next}</dd>
        </div>
        <div>
          <dt>上次运行</dt>
          <dd>
            {task.lastRunAt ? (
              <>
                <span className={`as-task-outcome ${failed ? "failed" : "ok"}`}>
                  {failed ? <CircleAlert /> : <CircleCheck />}
                  {whenText(task.lastRunAt)} · {failed ? "没有完成" : "完成"}
                </span>
                {fresh && <span className="as-task-fresh">新结果</span>}
                <button type="button" className="as-task-link" onClick={() => void openLatest()}>
                  查看结果
                  <ChevronRight />
                </button>
              </>
            ) : (
              "还没运行过"
            )}
          </dd>
        </div>
      </dl>
      {failed && task.lastError && (
        <p className="as-task-error">
          <CircleAlert />
          <span>{task.lastError}</span>
        </p>
      )}

      {asking ? (
        <div className="as-forget-ask">
          <span>删除“{task.name}”？它不会再运行，运行记录也会一起删除。</span>
          <button type="button" className="danger" onClick={onDelete}>
            删除
          </button>
          <button type="button" onClick={() => setAsking(false)}>
            取消
          </button>
        </div>
      ) : (
        <footer className="as-task-actions">
          <button
            type="button"
            onClick={onRun}
            disabled={task.running}
            title={paused ? "运行一次，并恢复定时" : undefined}
          >
            <Play />
            立即运行
          </button>
          {paused ? (
            <button type="button" onClick={onResume}>
              <Play />
              继续
            </button>
          ) : (
            !done && (
              <button type="button" onClick={onPause} disabled={task.state === "error"}>
                <Pause />
                暂停
              </button>
            )
          )}
          <button type="button" aria-expanded={showRuns} onClick={() => setShowRuns((open) => !open)}>
            <History />
            运行记录{task.completed > 0 && <small>{task.completed}</small>}
          </button>
          <button type="button" className="as-task-delete" aria-label="删除任务" onClick={() => setAsking(true)}>
            <Trash2 />
          </button>
        </footer>
      )}

      {showRuns && (
        <div className="as-task-runs">
          {!runs ? (
            <p className="as-memory-loading">
              <LoaderCircle />
              正在读取…
            </p>
          ) : !runs.length ? (
            <p className="as-task-runs-empty">
              <Info />
              还没有运行记录
            </p>
          ) : (
            <ul>
              {runs.map((run) => (
                <li key={run.id ?? run.startedAt}>
                  {run.id ? (
                    <button type="button" onClick={() => onOpenRun(run.id as string, runTitle(run))}>
                      <span>{whenText(run.startedAt)}</span>
                      <small>{duration(run)}</small>
                      <ChevronRight />
                    </button>
                  ) : (
                    <span className="as-task-run-plain">
                      <span>{whenText(run.startedAt)}</span>
                      <small>只运行了脚本</small>
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </li>
  );
}

function duration(run: AssistantTaskRun): string {
  if (!run.endedAt) return "进行中";
  const seconds = Math.max(1, Math.round(run.endedAt - run.startedAt));
  return seconds < 60 ? `用时 ${seconds} 秒` : `用时 ${Math.round(seconds / 60)} 分钟`;
}
