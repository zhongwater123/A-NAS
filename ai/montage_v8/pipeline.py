"""编排层：切卡片 → 量画质 → 描述 → 第一步 → 第二步 → 填槽 → 渲染。每一步的结果写进数据目录，已有的直接复用
（换描述后旧的规划不能复用：结果里记着用的是哪份描述）。

只调用领域层和端口，不认识具体平台；平台由 config.load 选定的适配器决定。
"""

import hashlib
import statistics
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from .adapters.store import HeldCache, read_json, write_json
from .config import Platform
from .domain import (
    cards as cardrules,
    catalog,
    describe,
    fill,
    picture,
    plan,
    quality as qualityrules,
    windows as windowrules,
)
from .domain.options import Case, CutOptions

POINT_WIDTH, DARK, SHAKE_FPS = 1920, 60, 10
VIDEO_SUFFIXES = {".mov", ".mp4", ".m4v", ".mts", ".avi", ".mkv"}


class StaleStep(RuntimeError):
    """已有的规划不是按现在的描述做的。"""


class Cancelled(RuntimeError):
    """任务层要求停下：在两步之间检查，不打断正在进行的模型调用。"""


@dataclass
class Montage:
    """report(说明, 关键帧路径或 None, 完成数, 总数)：每做完一小步告诉任务层，命令行下为 None；
    stop()：返回 True 就在下一步之前抛 Cancelled。"""

    platform: Platform
    report: Callable | None = None
    stop: Callable | None = None

    @property
    def store(self):
        return self.platform.store

    def log(self, *args):
        print(*args, flush=True)

    def tell(self, detail: str, frame: str | None = None, done: int = 0, total: int = 0) -> None:
        if self.stop and self.stop():
            raise Cancelled
        if self.report:
            self.report(detail, frame, done, total)

    # 上传的文件夹 ---------------------------------------------------------------------------------------

    UPLOAD_INSTRUCTION = "剪一条旅行 vlog，场景之间衔接自然"

    def upload_case(self, folder: str) -> Case:
        """上传文件夹（原片目录下 uploads/<文件夹>）作为一组素材：键是 u 加文件夹名的短摘要（卡片编号和文件名里用），
        索引名 upload-<键>。"""
        key = "u" + hashlib.sha1(folder.encode("utf-8")).hexdigest()[:6]  # noqa: S324  只是取个短名字，不做安全用途
        return Case(key=key, index=f"upload-{key}", instruction=self.UPLOAD_INSTRUCTION, style="旅行", name=folder)

    def index(self, case: Case, folder: str) -> dict:
        """给上传文件夹里的视频建窗口索引（domain/windows.py），已经编码过的帧不再编码。返回 {视频编号: 文件}。"""
        embedder, ff = self.platform.embedder, self.platform.ffmpeg
        if embedder is None:
            raise RuntimeError("这个平台的配置里没有图文向量模型（[embedder]），上传的视频建不了索引")
        here = self.store.videos / "uploads" / folder
        files = [(str(f.relative_to(self.store.videos)), f.stat().st_mtime) for f in sorted(here.iterdir())
                 if f.is_file() and f.suffix.lower() in VIDEO_SUFFIXES] if here.is_dir() else []
        if not files:
            raise RuntimeError(f"上传文件夹“{folder}”里没有视频")
        model = embedder.model
        d = self.store.indexes / f"video-moments-{case.index}" / model
        old = read_json(d / "index.json") if (d / "index.json").exists() else {"videos": {}}
        vectors = self.store.read_vectors(d / "frames.npz")
        ids = windowrules.numbered(files, {meta["file"]: vid for vid, meta in old["videos"].items()})
        durations = {vid: old["videos"][vid]["duration"] if vid in old["videos"] else ff.duration(self.store.videos / rel)
                     for vid, rel in ids.items()}
        todo = sum(len(windowrules.windows(round(t * 1000))) for t in durations.values())
        out, done = {"model": model, "layout": "2s1f", "videos": {}}, 0
        for vid, rel in ids.items():
            path, duration = self.store.videos / rel, durations[vid]
            flat_before = {w["frame"]: w["flat"] for w in old["videos"].get(vid, {}).get("windows", [])}
            rows = []
            for start, end, frame in windowrules.windows(round(duration * 1000)):
                key = f"{vid}/{frame}"
                if key in vectors and frame in flat_before:
                    flat = flat_before[frame]
                else:
                    self.tell(f"编码 {Path(rel).name} 第 {start / 1000:.0f} 秒", None, done, todo)
                    jpeg, spread = ff.jpeg(path, frame, round(duration * 1000), windowrules.EDGE)
                    vectors[key] = embedder.embed_image(jpeg)
                    flat = spread < windowrules.FLAT
                rows.append({"start": start / 1000, "end": end / 1000, "frame": frame, "flat": flat})
                done += 1
            out["videos"][vid] = {"file": rel, "duration": duration, "windows": rows}
            self.store.write_vectors(vectors, d / "frames.npz")
        write_json(out, d / "index.json")
        return ids

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
        todo = [cid for cid, c in done.items() if "answer" not in c and (opts.videos is None or c["video"] in opts.videos)]
        for k, cid in enumerate(todo):
            c = done[cid]
            self.tell(f"描述卡片 {cid}", c["frames"][0] if c.get("frames") else None, k, len(todo))
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
                              self.store.excluded(case.index), quality=opts.quality, wide=opts.wide, upright=self.store.upright,
                              videos=opts.videos)

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
        self.tell("第一步：读卡片目录和音乐段落，写成片大纲")
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
        self.tell(f"第二步：给 {sum(req['pictures'])} 个画面挑节拍")
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
            self.tell(f"渲染 {len(clips)} 个镜头")
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
