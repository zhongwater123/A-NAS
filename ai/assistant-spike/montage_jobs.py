"""剪辑规划这一轮在 Hermes 里的任务（Hermes 探针 P1，docs/research/montage-hermes-probe.md）。

要用 `python -P` 运行：/spike 里有 spike 自己的 gateway.py，脚本目录在模块搜索路径最前面时，
它会盖住 Hermes 的 gateway 包，删会话时就会导入错。

在成员自己的沙箱里运行：montage-plan.sh 照探针单元生成 anas-assistant-montage-jobs@，以成员
身份执行本脚本。用 Hermes 自己的任务表与会话库，与 cron_round.py 一样依赖 Hermes 的内部模块。
参数从助手目录的 montage-jobs-args.json 读（一个字符串数组），结果写到 montage-jobs-out.json：

  create NAME PROMPT    建一个暂停的任务，只开放 montage 工具组；回答任务编号
  plain NAME PROMPT     建一个普通任务（不限定工具组，不暂停），对照成员自己的定时任务
  trigger JOB           使它到期（trigger_job，暂停的任务随之恢复）
  remove JOB            先删它的运行留下的会话，再删任务
  show JOB              任务记录与它的运行会话
  memory JOB MARKER     它最近一次运行的系统提示里有没有 MARKER（成员的记忆会不会带进这一轮）
"""

import json
import sys
from pathlib import Path

from cron.jobs import create_job, get_job, remove_job, trigger_job
from hermes_state import SessionDB

FILES = Path("/nas/助手")


def runs(db, job_id):
    # 运行是 source='cron' 的会话：老的编号是 cron_<任务>_<时间>，统一运行时之后是 local-<哈希>，
    # 由调度器的准入记录挂到任务上；Hermes 自己的“运行记录”接口也这样列（web_routers/cron.py）。
    rows = db.list_cron_job_runs(job_id, limit=100, offset=0)
    return sorted(str(r.get("id") or r.get("session_id")) for r in rows)


def act(argv):
    action, db = argv[0], SessionDB()
    if action in ("create", "plain"):
        restricted = action == "create"
        job = create_job(
            prompt=argv[2],
            schedule="every 1440m",  # 不靠时间表到期：规划的任务只由 trigger 使它到期
            name=argv[1],
            enabled_toolsets=["montage"] if restricted else None,
            paused=restricted,
            paused_reason="剪辑规划，等回合脚本触发" if restricted else None,
        )
        return {"id": job["id"]}
    if action == "trigger":
        return {"triggered": trigger_job(argv[1]) is not None}
    if action == "remove":
        deleted = [sid for sid in runs(db, argv[1]) if db.delete_session(sid)]
        return {"removed": remove_job(argv[1]), "sessions_deleted": deleted}
    if action == "show":
        return {"job": get_job(argv[1]), "runs": runs(db, argv[1])}
    if action == "memory":
        sessions = runs(db, argv[1])
        if not sessions:
            return {"session": None}
        session = db.get_session(sessions[-1]) or {}
        return {"session": sessions[-1], "marker_in_system_prompt": argv[2] in json.dumps(session, ensure_ascii=False)}
    raise SystemExit(f"unknown action {action}")


def main(argv):
    if argv and argv[0].startswith("@"):
        argv = json.loads(Path(argv[0][1:]).read_text(encoding="utf-8"))
    out = act(argv)
    text = json.dumps(out, ensure_ascii=False, default=str)
    (FILES / "montage-jobs-out.json").write_text(text, encoding="utf-8")
    print(text)


if __name__ == "__main__":
    main(sys.argv[1:])
