"""任务层：网页“开始剪辑”建一个任务，后台 Worker 一次执行一个，网页查进度、取结果（docs/research/backend-practices.md）。

任务存在 SQLite（标准库 sqlite3）的一张表里，服务重启后还在：重启时正在执行的任务回到排队，再执行时编排层复用已经
做完的步骤（描述、两步规划、成片都按文件存着），所以不会重复花钱。一台机器一次只能剪一个（描述和渲染会吃满 CPU 或
NPU），所以只有一个 Worker 线程，不用 Redis 之类的队列服务。

一个剪辑任务的步骤：检查素材 → 补写描述（已有的跳过）→ 读歌曲的段落 → 每个版本各规划、渲染一次。取消在两步之间生效，
不打断正在进行的模型调用或渲染。
"""

import contextlib
import json
import sqlite3
import threading
import time
import traceback
from collections.abc import Callable
from dataclasses import dataclass, field, replace
from pathlib import Path

from .domain.options import Case, CutOptions
from .pipeline import Cancelled, Montage

QUEUED, RUNNING, DONE, FAILED, CANCELLED = "queued", "running", "done", "failed", "cancelled"
UPLOAD = "upload:"
SCHEMA = """CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created REAL NOT NULL, updated REAL NOT NULL,
  state TEXT NOT NULL,
  request TEXT NOT NULL,            -- JSON：素材组、勾选的视频、歌、顺序、补充要求、版本数
  steps TEXT NOT NULL,              -- JSON：步骤名
  step INTEGER NOT NULL DEFAULT 0,  -- 正在做第几步（从 0 数）
  done INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0,   -- 这一步里完成了几项（描述的卡片）
  detail TEXT NOT NULL DEFAULT '',
  frame TEXT,                       -- 这一步正在看的关键帧
  result TEXT,                      -- JSON：每个版本的标签、成片和开销
  error TEXT,
  cancel INTEGER NOT NULL DEFAULT 0
)"""
FIELDS = ("id", "created", "updated", "state", "request", "steps", "step", "done", "total", "detail", "frame", "result",
          "error", "cancel")


class RequestError(ValueError):
    """请求本身不对：原因给用户看。"""


@dataclass(frozen=True)
class CutRequest:
    """一次剪辑：case 素材组（cases.toml 里的键，或 "upload:<上传文件夹>"）；videos 勾选的视频（素材组里是视频编号，
    上传文件夹里是文件名；空是全部）；song 曲库里的歌（music-<歌>.json 的歌名）；order "place"（同一地点连在一起，
    不按时间）或 "time"；ask 补充要求；versions 剪几版。"""

    case: str
    song: str
    videos: tuple = ()
    order: str = "place"
    ask: str = ""
    versions: int = 3
    wide: bool = False

    @classmethod
    def parse(cls, body: dict, resolve: Callable[[str], Case | None], songs: set[str]) -> "CutRequest":
        """resolve(case) 给出素材组，不存在给 None。"""
        case = str(body.get("case", ""))
        if resolve(case) is None:
            raise RequestError("没有这组素材（上传的文件夹要先上传完）")
        song = body.get("song", "")
        if song not in songs:
            raise RequestError(f"《{song}》还没有分析过段落和小节，先在曲库里选分析过的歌")
        order = body.get("order", "place")
        if order not in ("place", "time"):
            raise RequestError("顺序是 place 或 time")
        versions = int(body.get("versions", 3))
        if not 1 <= versions <= 3:
            raise RequestError("一次剪 1 到 3 版")
        ask = str(body.get("ask", "")).strip()
        if len(ask) > 200:
            raise RequestError("补充要求不超过 200 字")
        return cls(case=case, song=song, videos=tuple(sorted({str(v) for v in body.get("videos", [])})), order=order,
                   ask=ask, versions=versions, wide=bool(body.get("wide", False)))

    def options(self, tag: str) -> CutOptions:
        return CutOptions(tag=tag, free_order=self.order == "place", ask=self.ask, wide=self.wide,
                          videos=frozenset(self.videos) if self.videos else None)

    @property
    def folder(self) -> str | None:
        """上传文件夹的名字；不是上传的就是 None。"""
        return self.case.removeprefix(UPLOAD) if self.case.startswith(UPLOAD) else None

    def steps(self) -> list[str]:
        out = ["检查素材"]
        if self.folder is not None:
            out += ["给视频建索引", "按画面切成卡片", "检查模糊、抖动、遮挡"]
        out += ["本机模型补写卡片描述", "读取歌曲的段落和小节"]
        for k in range(1, self.versions + 1):
            out += [f"规划方案{k}", f"渲染方案{k}"]
        return out


class Jobs:
    """任务表。每次操作开一个连接：Worker 线程和网页请求各用各的，SQLite 自己处理并发。"""

    def __init__(self, path: Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with self._db() as db:
            db.execute("PRAGMA journal_mode=WAL")
            db.execute(SCHEMA)
            # 上次服务停掉时正在执行的：回到排队，再执行时复用已做完的步骤
            db.execute("UPDATE jobs SET state=?, detail='服务重启，接着做' WHERE state=?", (QUEUED, RUNNING))

    @contextlib.contextmanager
    def _db(self):
        """一个连接、一个事务，用完提交并关闭（sqlite3 的 with 只管事务，不关连接）。"""
        db = sqlite3.connect(self.path, timeout=30)
        db.row_factory = sqlite3.Row
        try:
            with db:
                yield db
        finally:
            db.close()

    @staticmethod
    def _row(row) -> dict | None:
        if row is None:
            return None
        out = dict(row)
        for key in ("request", "steps", "result"):
            out[key] = json.loads(out[key]) if out[key] else None
        out["cancel"] = bool(out["cancel"])
        return out

    def add(self, request: CutRequest) -> dict:
        now = time.time()
        with self._db() as db:
            cur = db.execute("INSERT INTO jobs (created, updated, state, request, steps) VALUES (?, ?, ?, ?, ?)",
                             (now, now, QUEUED, json.dumps(request.__dict__, ensure_ascii=False),
                              json.dumps(request.steps(), ensure_ascii=False)))
            return self.get(cur.lastrowid, db)

    def get(self, job_id: int, db=None) -> dict | None:
        if db is None:
            with self._db() as db:
                return self.get(job_id, db)
        return self._row(db.execute("SELECT * FROM jobs WHERE id=?", (job_id,)).fetchone())

    def recent(self, limit: int = 20) -> list[dict]:
        with self._db() as db:
            return [self._row(r) for r in db.execute("SELECT * FROM jobs ORDER BY id DESC LIMIT ?", (limit,))]

    def ahead(self, job_id: int) -> int:
        """排在它前面、还没做完的任务数。"""
        with self._db() as db:
            return db.execute("SELECT COUNT(*) FROM jobs WHERE id<? AND state IN (?, ?)", (job_id, QUEUED, RUNNING)).fetchone()[0]

    def update(self, job_id: int, **values) -> None:
        for key in ("request", "steps", "result"):
            if key in values and values[key] is not None:
                values[key] = json.dumps(values[key], ensure_ascii=False)
        values["updated"] = time.time()
        sets = ", ".join(f"{k}=?" for k in values)
        assert set(values) <= set(FIELDS), values.keys()  # noqa: S101  字段名来自本模块的调用，不来自请求
        with self._db() as db:
            db.execute(f"UPDATE jobs SET {sets} WHERE id=?", (*values.values(), job_id))  # noqa: S608  同上

    def cancel(self, job_id: int) -> dict | None:
        """排队的直接取消；执行中的标记一下，Worker 在下一步之前停。"""
        with self._db() as db:
            db.execute("UPDATE jobs SET state=?, detail='已取消', updated=? WHERE id=? AND state=?",
                       (CANCELLED, time.time(), job_id, QUEUED))
            db.execute("UPDATE jobs SET cancel=1, updated=? WHERE id=? AND state=?", (time.time(), job_id, RUNNING))
            return self.get(job_id, db)

    def next(self) -> dict | None:
        """取最早排队的任务并标为执行中。"""
        with self._db() as db:
            row = db.execute("SELECT id FROM jobs WHERE state=? ORDER BY id LIMIT 1", (QUEUED,)).fetchone()
            if row is None:
                return None
            db.execute("UPDATE jobs SET state=?, updated=? WHERE id=? AND state=?", (RUNNING, time.time(), row["id"], QUEUED))
            return self.get(row["id"], db)


@dataclass
class Worker:
    """后台线程：轮流取任务执行。results(montage, request, tags) 把做好的版本整理成网页要的数据。"""

    jobs: Jobs
    montage: Callable[..., Montage]         # 每个任务一个，带上它的进度回报
    resolve: Callable[[str], Case | None]   # 素材组的名字 → Case
    results: Callable
    poll: float = 1.0
    _wake: threading.Event = field(default_factory=threading.Event)
    _stop: threading.Event = field(default_factory=threading.Event)

    def start(self) -> threading.Thread:
        t = threading.Thread(target=self.loop, name="montage-jobs", daemon=True)
        t.start()
        return t

    def kick(self) -> None:
        self._wake.set()

    def close(self) -> None:
        self._stop.set()
        self._wake.set()

    def loop(self) -> None:
        while not self._stop.is_set():
            job = self.jobs.next()
            if job is None:
                self._wake.wait(self.poll)
                self._wake.clear()
                continue
            self.run(job)

    def run(self, job: dict) -> None:
        jid = job["id"]
        req = CutRequest(**{**job["request"], "videos": tuple(job["request"].get("videos", []))})
        state = {"step": 0}

        def step(n: int, detail: str = "") -> None:
            state["step"] = n
            self.jobs.update(jid, step=n, done=0, total=0, detail=detail, frame=None)

        def report(detail, frame, done, total):
            self.jobs.update(jid, detail=detail, frame=frame, done=done, total=total)

        def stop():
            return self.jobs.get(jid)["cancel"]

        montage = self.montage(report=report, stop=stop)
        case = self.resolve(req.case)
        n = iter(range(len(req.steps())))
        try:
            step(next(n), "看看这次要剪的视频")
            if case is None:
                raise RequestError("这组素材不在了")
            if req.folder is not None:
                step(next(n))
                ids = montage.index(case, req.folder)
                if req.videos:                    # 勾的是文件名，换成索引里的视频编号
                    wanted = set(req.videos)
                    req = replace(req, videos=tuple(v for v, f in ids.items() if f.rsplit("/", 1)[-1] in wanted))
                step(next(n))
                montage.cards(case)
                step(next(n))
                montage.quality(case)
            step(next(n))
            montage.describe(case, req.options(""))
            if not montage.usable(case, req.options("")):
                raise RequestError("勾选的视频里没有能用的画面（画质都不合格，或太短切不出卡片）")
            step(next(n))
            montage.music(case, req.song)
            tags = []
            for k in range(1, req.versions + 1):
                tag = f"-job{jid}-{k}"
                step(next(n))
                montage.shots(case, req.options(tag), req.song)
                step(next(n))
                montage.cut(case, req.options(tag), req.song)
                tags.append(tag)
            self.jobs.update(jid, state=DONE, step=len(req.steps()), detail="剪好了",
                             result=self.results(montage, case, req, tags))
        except Cancelled:
            self.jobs.update(jid, state=CANCELLED, detail="已取消")
        except Exception as error:  # noqa: BLE001  任务失败记进任务表给用户看，Worker 继续做下一个
            traceback.print_exc()
            self.jobs.update(jid, state=FAILED, error=str(error) or type(error).__name__,
                             detail=f"第 {state['step'] + 1} 步出错")
