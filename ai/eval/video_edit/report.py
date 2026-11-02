"""Writes the plan cards and the comparison report as Markdown. Standard library only.

A result is what inner.load_result gathers for one plan: its pipeline,
session, environment, per-case metrics, relative times against the baseline,
offline outcomes, link-check findings, summary and seconds per category.
"""

from eval.video_edit import CATEGORIES, RUNS_ON, metrics


def size(value):
    if value is None:
        return "—"
    if value >= 10 * 2**30:
        return f"{value / 2**30:.1f} GiB"
    return f"{value / 2**20:,.0f} MiB"


def duration(seconds):
    if seconds is None:
        return "—"
    if seconds < 60:
        return f"{seconds:.1f} 秒"
    minutes, rest = divmod(round(seconds), 60)
    if minutes < 60:
        return f"{minutes} 分 {rest} 秒"
    hours, minutes = divmod(minutes, 60)
    return f"{hours} 小时 {minutes} 分"


def factor(value):
    return "—" if value is None else f"{value:.2f}"


def cell(text):
    return str(text).replace("|", "\\|").replace("\n", " ")


def table(header, rows):
    lines = ["| " + " | ".join(header) + " |", "|" + "---|" * len(header)]
    lines += ["| " + " | ".join(cell(value) for value in row) + " |" for row in rows]
    return "\n".join(lines)


def external_text(external):
    if not external["calls"]:
        return "无"
    parts = [f"{external['calls']} 次", f"上传 {size(external['upload_bytes'])}", f"耗时 {duration(external['seconds'])}"]
    if external["input_tokens"] or external["output_tokens"]:
        parts.append(f"token {external['input_tokens']:,} / {external['output_tokens']:,}")
    parts += [f"{amount:g} {currency}" for currency, amount in external["cost"].items()]
    return "，".join(parts)


def sends_out(stage):
    return "；".join(f"{item['data']} → {item['to']}" for item in stage["sends_out"]) or "无"


def mermaid(stages):
    lines = ["```mermaid", "flowchart LR"]
    for number, stage in enumerate(stages):
        label = f"{stage['id']}<br>{CATEGORIES[stage['category']]} · {RUNS_ON[stage['runs_on']]}".replace('"', "'")
        lines.append(f'  s{number}["{label}"]')
    lines += [f"  s{number} --> s{number + 1}" for number in range(len(stages) - 1)]
    lines.append("```")
    return "\n".join(lines)


def render_card(result):
    pipeline, env, summary = result["pipeline"], result["env"], result["summary"]
    totals = {stage["id"]: 0.0 for stage in pipeline["stages"]}
    for case in result["cases"].values():
        for stage, seconds in case["stage_seconds"].items():
            totals[stage] += seconds
    steps = [(f"`{stage['id']}`", CATEGORIES[stage["category"]], stage["does"],
              f"{'、'.join(stage['inputs'])} → {'、'.join(stage['outputs'])}", "、".join(stage["uses"]),
              RUNS_ON[stage["runs_on"]], sends_out(stage), stage["on_failure"], duration(totals[stage["id"]]))
             for stage in pipeline["stages"]]
    parts = [(item["name"], item.get("revision") or item.get("version", "—"), item["license"],
              size(item.get("bytes")), item.get("source", "—")) for item in env["models"]]
    parts += [(item["name"], item["version"], item["license"], "—", item.get("source", "—"))
              for item in pipeline.get("dependencies", [])]
    lines = [
        f"# 方案卡：方案 {result['name']}",
        "",
        pipeline["summary"],
        "",
        f"- 入口：`{' '.join(pipeline['entry'])}`",
        f"- 环境：`/env` {size(env['env_bytes'])}，方案目录 {size(env['plan_bytes'])}",
        f"- 用例：{summary['ok']}/{summary['cases']} 成功",
        "",
        "## 链路",
        "",
        mermaid(pipeline["stages"]),
        "",
        "## 每一步",
        "",
        "实测合计是全部用例里这一步的时间之和。",
        "",
        table(("步骤", "类别", "做什么", "输入 → 输出", "用什么", "在哪里跑", "离开宿主机的数据", "失败时", "实测合计"), steps),
        "",
        "## 模型与依赖",
        "",
        table(("名称", "版本或 revision", "许可证", "体积", "来源"), parts) if parts else "没有声明。",
        "",
        "## 链路核对",
        "",
        "\n".join(f"- {line}" for line in result["findings"]) or "没有发现问题。",
        "",
    ]
    return "\n".join(lines)


def offline_text(result):
    if result["offline"] is None:
        return "未声明外部计算，不需要"
    return f"{sum(result['offline'].values())}/{len(result['offline'])}"


def case_text(case, relative):
    if case is None:
        return ("—",) * 4
    wall = duration(case["wall_seconds"]) if case["ok"] else f"失败：{case['failure']}"
    return wall, factor(relative), factor(case["processing_ratio"]), size(case["memory_peak_bytes"])


def render_report(a, b, cases, header):
    """a and b are results; cases maps a case id to its prepare.json entry; header holds the shared versions."""
    names = (f"方案 {a['name']}", f"方案 {b['name']}")
    machines = [result["session"]["machine"] for result in (a, b)]
    device = [
        ("CPU", *(machine["cpu"] for machine in machines)),
        ("CPU 数", *(machine["cpus"] for machine in machines)),
        ("内存", *(size(machine["memory_bytes"]) for machine in machines)),
        ("系统", *(f"{machine['os']}，内核 {machine['kernel']}" for machine in machines)),
        ("Docker", *(machine["docker"] for machine in machines)),
    ]
    sa, sb = a["summary"], b["summary"]
    overview = [
        ("成功的用例", f"{sa['ok']}/{sa['cases']}", f"{sb['ok']}/{sb['cases']}"),
        ("内存峰值，含页缓存（最高 / 中位）", *(f"{size(s['memory_peak_max'])} / {size(s['memory_peak_median'])}" for s in (sa, sb))),
        ("匿名内存峰值（最高）", size(sa["anon_peak_max"]), size(sb["anon_peak_max"])),
        ("OOM 的用例", sa["oom_cases"], sb["oom_cases"]),
        ("`/env` 与方案目录", *(f"{size(r['env']['env_bytes'])} + {size(r['env']['plan_bytes'])}" for r in (a, b))),
        ("临时文件与输出（最高）", size(sa["disk_peak_max"]), size(sb["disk_peak_max"])),
        ("外部计算", external_text(sa["external"]), external_text(sb["external"])),
        ("断网能出片", offline_text(a), offline_text(b)),
        ("链路核对发现", f"{len(a['findings'])} 条", f"{len(b['findings'])} 条"),
        ("相对耗时（中位）", factor(sa["relative_median"]), factor(sb["relative_median"])),
        ("耗时合计", duration(sa["wall_total"]), duration(sb["wall_total"])),
        ("CPU 时间合计", duration(sa["cpu_total"]), duration(sb["cpu_total"])),
    ]
    per_case = [(case, info["provider"], duration(info["source_seconds"]),
                 *case_text(a["cases"].get(case), a["relative"].get(case)),
                 *case_text(b["cases"].get(case), b["relative"].get(case)))
                for case, info in sorted(cases.items())]
    categories = []
    totals = [sum(result["categories"].values()) for result in (a, b)]
    for key, label in CATEGORIES.items():
        values = [result["categories"].get(key, 0.0) for result in (a, b)]
        if any(values):
            categories.append((label, *(item for value, total in zip(values, totals, strict=True)
                                        for item in (duration(value), f"{value / total:.0%}" if total else "—"))))
    providers = []
    for provider in sorted({info["provider"] for info in cases.values()}):
        chosen = [case for case, info in cases.items() if info["provider"] == provider]
        row = [f"方案 {provider} 提供的 {len(chosen)} 个"]
        for result in (a, b):
            ok = sum(result["cases"][case]["ok"] for case in chosen if case in result["cases"])
            middle = metrics.median(result["relative"].get(case) for case in chosen)
            peaks = [result["cases"][case]["memory_peak_bytes"] for case in chosen if case in result["cases"]]
            row.append(f"成功 {ok}/{len(chosen)}，相对耗时中位 {factor(middle)}，内存峰值最高 {size(max(peaks, default=None))}")
        providers.append(row)
    lines = [
        "# 视频剪辑方案测评报告",
        "",
        f"口径 {header['method_version']} · 管线提交 `{header['harness_commit'][:12]}` · "
        f"环境指纹 `{header['fingerprint'][:12]}` · 测试集清单 `{header['manifest_sha256'][:12]}`",
        "",
        "成片质量由项目人员盲评判断，本报告只有资源、耗时与链路。",
        "",
        "## 设备",
        "",
        table(("", *names), device),
        "",
        "## 总览",
        "",
        "内存、体积、外部计算与链路核对跟设备无关，可以直接比较。耗时跟设备有关；相对耗时是除以同一台设备上对照方案的耗时，只是近似值。",
        "",
        table(("指标", *names), overview),
        "",
        "## 逐个用例",
        "",
        "倍率是耗时 ÷ 素材时长。",
        "",
        table(("用例", "提供方", "素材时长", *(f"{name} {column}" for name in names for column in ("耗时", "相对耗时", "倍率", "内存峰值"))), per_case),
        "",
        "## 按类别的耗时",
        "",
        table(("类别", *(f"{name} {column}" for name in names for column in ("合计", "占比"))), categories)
        if categories else "两套方案的 trace 都没有可计入的步骤。",
        "",
        "## 按提供方拆开",
        "",
        table(("用例", *names), providers),
        "",
        "## 链路核对",
        "",
    ]
    for name, result in zip(names, (a, b), strict=True):
        lines += [f"### {name}", "", "\n".join(f"- {line}" for line in result["findings"]) or "没有发现问题。", ""]
    lines += [f"每一步的说明见方案卡：card-{a['name']}.md、card-{b['name']}.md。", ""]
    return "\n".join(lines)
