"""Link checks: what a plan's runs did against what its pipeline.yaml declares.

Findings are reported, never judged: the report lists them for people to read
(docs/specs/video-edit-evaluation.md#核对与报告). Standard library only.
"""

import collections

from eval.video_edit import EXTERNAL, RUNS_ON, trace

# Sent bytes above this, from a plan that declares no external computing, are
# reported; below it they are DNS and the like.
NETWORK_NOTICE_BYTES = 1 << 20


def findings(pipeline, cases):
    """Sorted findings; one that repeats across cases is written once, with the cases.

    cases maps a case id to {"run": run.json, "spans": parsed spans or None
    when trace.json is missing or unreadable, "timeline_ok": True, False or
    None when there is no timeline}.
    """
    stages = {stage["id"]: stage for stage in pipeline["stages"]}
    declares_external = any(stage["runs_on"] in EXTERNAL for stage in stages.values())
    found = collections.defaultdict(list)
    appeared = set()
    traced = 0
    for case, data in sorted(cases.items()):
        run, spans = data["run"], data["spans"]
        if spans is None:
            if run.get("outputs", {}).get("trace.json"):
                found["trace.json 无法读取"].append(case)
        else:
            traced += 1
            appeared |= {span["name"] for span in spans} & stages.keys()
            for name in trace.undeclared(spans, stages):
                found[f"未声明的步骤 {name}"].append(case)
            for call in trace.external_calls(spans, stages):
                where = f"{RUNS_ON.get(call['target'], call['target'])}，{call['service'] or '未写服务'}"
                if call["stage"] is None:
                    found[f"不在任何步骤内的外部调用（{where}）"].append(case)
                elif stages[call["stage"]]["runs_on"] != call["target"]:
                    declared = RUNS_ON[stages[call["stage"]]["runs_on"]]
                    found[f"步骤 {call['stage']} 声明的运行位置是{declared}，却有外部调用（{where}）"].append(case)
        sent = run.get("net_sent_bytes", 0)
        if not declares_external and sent > NETWORK_NOTICE_BYTES:
            found["没有声明外部计算，容器却发送了网络数据"].append(f"{case}（{sent / 2**20:.1f} MiB）")
        if data.get("timeline_ok") is False:
            found["剪辑决策无法读取"].append(case)
    lines = [f"{message}：{'、'.join(where)}" for message, where in found.items()]
    if traced:
        lines += [f"步骤 {stage} 在 {traced} 个有 trace 的用例里都没有出现" for stage in stages if stage not in appeared]
    return sorted(lines)
