"""编排层：切卡片 → 量画质 → 描述 → 第一步 → 第二步 → 填槽 → 渲染。每一步的结果写进数据目录，已有的直接复用
（换描述后旧的规划不能复用：结果里记着用的是哪份描述）。

只调用领域层和端口，不认识具体平台；平台由 config.load 选定的适配器决定。
"""

import statistics
import time
from dataclasses import dataclass
from pathlib import Path

from .adapters.store import HeldCache, read_json, write_json
from .config import Platform
from .domain import cards as cardrules, catalog, describe, fill, picture, plan, quality as qualityrules
from .domain.options import Case, CutOptions

POINT_WIDTH, DARK, SHAKE_FPS = 1920, 60, 10


class StaleStep(RuntimeError):
    """已有的规划不是按现在的描述做的。"""


@dataclass
class Montage:
    platform: Platform

    @property
    def store(self):
        return self.platform.store

    def log(self, *args):
        print(*args, flush=True)

    # 卡片 -----------------------------------------------------------------------------------------------

    def cards(self, case: Case) -> None:
        """切卡片、存关键帧图片（不用模型）；已有的卡片不动。"""
        ix = self.store.windows(case.index)
        path = self.store.cards_file(case.key)
        done = self.store.all_cards(case.key)
        for c in cardrules.plan_cards(ix, case.key):
            if c["id"] in done:
                continue
            file = self.store.source(ix, c["video"])
            frames = []
            for t in c["times"]:
                out = self.store.frame_path(case.key, c["video"], t)
                if not out.exists() and not self.platform.ffmpeg.still(file, t, str(out)):
                    raise RuntimeError(f"{file} 在 {t:.3f} 秒取不到帧")
                frames.append(str(out))
            done[c["id"]] = {**c, "frames": frames}
            write_json(done, path)
        self.log(case.key, len(done), "张卡片")

    def quality(self, case: Case) -> None:
        """每张卡片的模糊、抖动、遮挡、夜空锐点（quality-<键>.json）；已量过的跳过。"""
        ix = self.store.windows(case.index)
        path = self.store.quality_file(case.key)
        done = self.store.quality(case.key)
        ff = self.platform.ffmpeg
        for n, c in enumerate(self.store.all_cards(case.key).values(), 1):
            if c["id"] in done:
                continue
            file = self.store.source(ix, c["video"])
            blurs = [ff.blur(f) for f in c["frames"]]
            shaky = picture.jitter(ff.grey(file, c["start"], c["end"], picture.SHAKE_SIZE, SHAKE_FPS))
            marks = [picture.blocked(self.store.grey_image(f)) for f in c["frames"]]
            points = [picture.sharp_points(ff.grey_full(file, t, POINT_WIDTH)) if self.store.mean_light(f) <= DARK else None
                      for f, t in zip(c["frames"], c["times"], strict=True)]
            done[c["id"]] = {"blur": round(float(statistics.median(blurs)), 3), "blurs": [round(b, 3) for b in blurs],
                             "shake": shaky, "blocked": float(statistics.median(marks)), "blockedRule": marks, "points": points}
            write_json(done, path)
            self.log(case.key, n, c["id"], "模糊", done[c["id"]]["blur"], "抖动", shaky, "遮挡", done[c["id"]]["blocked"])

    def describe(self, case: Case, opts: CutOptions | None = None) -> None:
        """视觉模型描述每张画质合格的卡片；不合格的记下原因，不描述。中途停了再跑会接着做。"""
        opts = opts or CutOptions()
        ix = self.store.windows(case.index)
        path = self.store.cards_file(case.key)
        done = self.store.all_cards(case.key)
        bad = qualityrules.poor(self.store.quality(case.key), self.store.reviewed(case.index)) if opts.quality else {}
        for cid, c in done.items():
            if "answer" in c:
                continue
            if cid in bad:
                c["error"] = "画面质量差，未描述：" + "、".join(bad[cid])
                write_json(done, path)
                continue
            t0 = time.perf_counter()
            try:
                content, sch, spans = describe.request(c["frames"], [t - c["start"] for t in c["times"]],
                                                       list(range(1, len(c["times"]) + 1)), c["end"] - c["start"])
                answer, seconds, errors = self.platform.describer.describe(describe.SYSTEM, content, sch)
                describe.check(answer, spans)
                cardrules.beat_spans(c, answer["beats"])
                vecs = [ix.frames[i] for i in c.get("keys", [])] or None
                c.pop("error", None)
                c.pop("score", None)
                c |= {"answer": answer, "seconds": round(seconds, 1), "errors": errors,
                      "repeats": cardrules.repeats(answer["beats"], vecs)}
            except Exception as error:  # noqa: BLE001  记进这张卡片，接着做下一张
                c |= {"error": str(error), "seconds": round(time.perf_counter() - t0, 1)}
            write_json(done, path)
            self.log(cid, f"{c['end'] - c['start']:.0f}s", len(c["times"]), "帧", c["seconds"], "秒",
                     c.get("answer", {}).get("summary", c.get("error")))

    # 规划 -----------------------------------------------------------------------------------------------

    def usable(self, case: Case, opts: CutOptions) -> list[dict]:
        return catalog.usable(self.store.described(case.key), self.store.quality(case.key), self.store.reviewed(case.index),
                              self.store.excluded(case.index), quality=opts.quality, wide=opts.wide, upright=self.store.upright)

    def descriptions(self, case: Case) -> str:
        return self.store.cards_file(case.key).name

    def reused(self, path: Path, case: Case) -> dict:
        data = read_json(path)
        if data.get("descriptions") != self.descriptions(case):
            raise StaleStep(f"{path.name} 不是按 {self.descriptions(case)} 规划的：换一个 tag 重剪")
        return data

    def music(self, case: Case, song: str) -> tuple[dict, str]:
        stem = Path(song).stem.removeprefix("music-")
        m = self.platform.music.music(song if Path(song).suffix not in ("", ".json") else stem, case.target)
        return m, f"music-{stem}.json"

    def pick(self, case: Case, opts: CutOptions, song: str) -> dict:
        path = self.store.cut_file("pick", case.key, opts.tag)
        if path.exists():
            return self.reused(path, case)
        m, music_file = self.music(case, song)
        system, user, schema = plan.pick_request(case, opts, self.usable(case, opts), m)
        answer, cost = self.platform.planner.ask(system, user, schema)
        out = {"system": system, "user": user, "answer": answer, "cost": cost, "descriptions": self.descriptions(case),
               "music": music_file, "labels": opts.labels_file, "wide": opts.wide}
        write_json(out, path)
        self.log(case.key, "pick", cost, "段数", len(answer["parts"]))
        return out

    def shots(self, case: Case, opts: CutOptions, song: str) -> dict:
        path = self.store.cut_file("shots", case.key, opts.tag)
        if path.exists():
            return self.reused(path, case)
        m, music_file = self.music(case, song)
        picked = self.pick(case, opts, song)["answer"]
        req = plan.shots_request(case, opts, picked, {c["id"]: c for c in self.usable(case, opts)}, m)
        answer, cost = self.platform.planner.ask(req["system"], req["user"], req["schema"])
        raw, shaped = plan.shots_answer(answer, req["pictures"], picked)
        out = {"system": req["system"], "user": req["user"], "raw": raw, "seconds": req["seconds"], "cap": plan.CAP,
               "cost": cost, "step2": "claude", "labels": opts.labels_file, "wide": opts.wide,
               "descriptions": self.descriptions(case), "slots": req["slots"], "music": music_file, "answer": shaped}
        write_json(out, path)
        self.log(case.key, "shots", cost)
        return out

    # 剪辑 -----------------------------------------------------------------------------------------------

    def timeline(self, case: Case, opts: CutOptions, song: str) -> tuple[dict, list, list]:
        """(音乐, clips, short)：按已有的两步规划填槽，不渲染。"""
        m, _ = self.music(case, song)
        planned = self.reused(self.store.cut_file("shots", case.key, opts.tag), case)
        picked = self.reused(self.store.cut_file("pick", case.key, opts.tag), case)["answer"]
        ix = self.store.windows(case.index)
        ff = self.platform.ffmpeg
        turned = self.store.turned()

        def compute(file, key, lo, hi, turned_):
            return picture.clear(lambda a, b: ff.grey(file, a, b, picture.GRID, 1 / picture.STEP, turned_), key, lo, hi)

        footage = fill.Footage(cards={c["id"]: c for c in self.store.described(case.key)},
                               catalog=[c["id"] for c in self.usable(case, opts)], index=ix,
                               source=lambda video: self.store.source(ix, video), turned=turned)
        held = HeldCache(self.store.cuts / f"held-{case.key}.json", compute)
        clips, short = fill.fill(m, planned, picked, footage, held, fps=ff.fps, labels=opts.labels)
        return m, clips, short

    def cut(self, case: Case, opts: CutOptions, song: str, render: bool = True) -> dict:
        self.shots(case, opts, song)
        m, clips, short = self.timeline(case, opts, song)
        planned = read_json(self.store.cut_file("shots", case.key, opts.tag))
        seconds = fill.seconds(clips)
        final = self.store.cuts / f"{case.key}-{case.name}-第八版{opts.tag}.mp4"
        t0 = time.perf_counter()
        if render:
            self.platform.ffmpeg.render([(self.store.source(self.store.windows(case.index), c["video"]), c["start"], c["end"],
                                          c.get("slow", 1.0)) for c in clips],
                                        m["song"], m["excerpt"][0], seconds, str(final), turned=self.store.turned())
        out = {"case": case.key, "instruction": opts.instruction(case), "target": round(m["seconds"]), "style": case.style,
               "music": planned["music"], "seconds": round(seconds, 2), "clips": clips, "short": short,
               "plannedShots": sum(len(s) for part in planned["slots"] for s in part),
               "renderSeconds": round(time.perf_counter() - t0, 1), "cut": str(final), "platform": self.platform.name}
        write_json(out, self.store.cut_file("cut", case.key, opts.tag))
        self.log(case.key, "cut", out["seconds"], "秒", len(clips), "个镜头，槽", out["plannedShots"], "个，没填满", short)
        return out
