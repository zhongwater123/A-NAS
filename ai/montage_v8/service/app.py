"""网页服务（Starlette + Uvicorn）：

GET  /                         网页（ai/montage_v8/web/index.html）
GET  /vendor/...               网页用的前端库
PUT  /api/uploads?group=&name=&modified=   请求体就是视频文件本身，边收边写盘；modified 是文件的修改时间（毫秒）
GET  /api/uploads              已经存下的视频，按组
GET  /uploads/<组>/<文件>      存下的视频（支持 Range，网页能拖着看）
POST /api/jobs                 建剪辑任务 {case, videos, song, order, ask, versions}；GET /api/jobs 最近的任务
GET  /api/jobs/<id>            进度和结果；POST /api/jobs/<id>/cancel 取消
GET  /api/songs                能剪的歌（段落和小节分析过的）
GET  /media/cards|cuts|videos/…、/media/songs/<歌>   关键帧、成片、原片、歌（都支持 Range）
GET  /search /likeness …       转给第七版的向量搜索服务（--proxy），没给就回 404
GET  其余路径                  网站目录（--site）里的文件：data.js、成片、关键帧……（支持 Range）

视频先写到同目录的隐藏临时文件，收完、大小对上再改成正式名字：中途断了不留半个文件。
"""

import contextlib
import os
from pathlib import Path

import httpx
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import FileResponse, JSONResponse, Response
from starlette.routing import Mount, Route
from starlette.staticfiles import StaticFiles

from .uploads import UploadError, Uploads

JOB_FIELDS = ("id", "state", "steps", "step", "done", "total", "detail", "error", "request", "created", "updated")

PAGE = Path(__file__).resolve().parents[1] / "web"
PROXIED = ("/search", "/likeness", "/record", "/records")


def create_app(uploads_dir: Path, site: Path | None = None, proxy: str = "", platform=None, jobs_db: Path | None = None) -> Starlette:
    """platform 给了才有剪辑任务和 /media/：任务表在 jobs_db（默认原片目录旁的 montage-jobs/jobs.sqlite），一个后台 Worker 执行。"""
    uploads = Uploads(uploads_dir)
    uploads.root.mkdir(parents=True, exist_ok=True)

    async def page(_request):
        return FileResponse(PAGE / "index.html", headers={"Cache-Control": "no-cache"})

    async def listing(_request):
        return JSONResponse({"root": str(uploads.root), "groups": uploads.listing()})

    async def receive(request: Request):
        q = request.query_params
        try:
            size = int(request.headers.get("content-length", ""))
        except ValueError:
            return JSONResponse({"error": "要带 Content-Length"}, status_code=411)
        try:
            path, existed = uploads.target(q.get("group", ""), q.get("name", ""), size)
            if existed:
                return JSONResponse({"saved": str(path), "size": size, "existed": True})
            uploads.room(size)
        except UploadError as error:
            return JSONResponse({"error": str(error)}, status_code=400)
        path.parent.mkdir(parents=True, exist_ok=True)
        partial = path.with_name(f".{path.name}.partial")
        got = 0
        try:
            with open(partial, "xb") as out:
                async for chunk in request.stream():
                    out.write(chunk)
                    got += len(chunk)
            if got != size:
                raise UploadError(f"只收到 {got} / {size} 字节，连接断了")
            if q.get("modified", "").isdigit():
                t = int(q["modified"]) / 1000
                os.utime(partial, (t, t))
            os.replace(partial, path)
        except FileExistsError:
            return JSONResponse({"error": "同一个文件正在上传"}, status_code=409)
        except (UploadError, OSError) as error:
            return JSONResponse({"error": str(error)}, status_code=400)
        finally:
            with contextlib.suppress(FileNotFoundError):
                partial.unlink()
        return JSONResponse({"saved": str(path), "size": got, "existed": False})

    client = httpx.AsyncClient(base_url=proxy, timeout=120) if proxy else None

    async def forward(request: Request):
        if client is None:
            return JSONResponse({"error": "没有配置向量搜索服务（--proxy）"}, status_code=404)
        # 同名参数要全部转过去（/likeness 有好几个 to）：QueryParams 当字典用只剩最后一个
        r = await client.request(request.method, request.url.path, params=list(request.query_params.multi_items()), content=await request.body(),
                                 headers={k: v for k, v in request.headers.items() if k.lower() in ("content-type",)})
        return Response(r.content, status_code=r.status_code, media_type=r.headers.get("content-type"))

    job_routes, startup, shutdown = (_job_routes(platform, jobs_db) if platform else ([], [], []))
    routes = [Route("/", page), Route("/index.html", page), *job_routes,
              Route("/api/uploads", listing, methods=["GET"]), Route("/api/uploads", receive, methods=["PUT"]),
              *(Route(p, forward, methods=["GET", "POST"]) for p in PROXIED),
              Mount("/vendor", StaticFiles(directory=PAGE / "vendor")),
              Mount("/uploads", StaticFiles(directory=uploads.root))]
    if site:
        routes.append(Mount("/", StaticFiles(directory=site, follow_symlink=True)))
    return Starlette(routes=routes, on_startup=startup, on_shutdown=[*shutdown, *([client.aclose] if client else [])])


def _job_routes(platform, jobs_db):
    from starlette.concurrency import run_in_threadpool

    from .. import config
    from ..jobs import UPLOAD, CutRequest, Jobs, RequestError, Worker
    from ..pipeline import Montage
    from . import results

    store = platform.store
    cases = config.cases()
    # 不放在 /media/ 能访问到的目录里
    jobs = Jobs(jobs_db or store.videos.parent / "montage-jobs" / "jobs.sqlite")
    def resolve(name: str):
        """素材组：cases.toml 里的，或 "upload:<文件夹>"（原片目录下 uploads/<文件夹> 存在且有视频）。"""
        if name in cases:
            return cases[name]
        if name.startswith(UPLOAD):
            folder = name.removeprefix(UPLOAD)
            if any(g["name"] == folder for g in Uploads(store.videos / "uploads").listing()):
                return Montage(platform).upload_case(folder)
        return None

    worker = Worker(jobs, lambda **kw: Montage(platform, **kw), resolve, results.build)

    def songs() -> dict[str, Path]:
        return {p.stem.removeprefix("music-"): p for p in sorted(store.cuts.glob("music-*.json")) if "." not in p.stem}

    def view(job: dict, full: bool = False) -> dict:
        out = {k: job[k] for k in JOB_FIELDS}
        out["frame"] = results.url("cards", job["frame"], store.cards) if job.get("frame") else None
        out["ahead"] = jobs.ahead(job["id"]) if job["state"] == "queued" else 0
        if full and job["result"]:
            out["result"] = job["result"]
        return out

    async def create(request: Request):
        try:
            body = await request.json()
            req = CutRequest.parse(body, resolve, set(songs()))
        except (ValueError, RequestError) as error:
            return JSONResponse({"error": str(error)}, status_code=400)
        job = await run_in_threadpool(jobs.add, req)
        worker.kick()
        return JSONResponse(view(job), status_code=201)

    async def listing(_request):
        return JSONResponse({"jobs": [view(j) for j in await run_in_threadpool(jobs.recent)]})

    async def one(request: Request):
        job = await run_in_threadpool(jobs.get, int(request.path_params["id"]))
        return JSONResponse(view(job, full=True)) if job else JSONResponse({"error": "没有这个任务"}, status_code=404)

    async def cancel(request: Request):
        job = await run_in_threadpool(jobs.cancel, int(request.path_params["id"]))
        return JSONResponse(view(job)) if job else JSONResponse({"error": "没有这个任务"}, status_code=404)

    async def song_list(_request):
        return JSONResponse({"songs": sorted(songs())})

    async def song_file(request: Request):
        path = songs().get(request.path_params["stem"])
        if not path:
            return JSONResponse({"error": "没有这首歌"}, status_code=404)
        return FileResponse(read_song(path))

    def read_song(path):
        import json

        return json.loads(path.read_text(encoding="utf-8"))["song"]

    routes = [Route("/api/jobs", create, methods=["POST"]), Route("/api/jobs", listing, methods=["GET"]),
              Route("/api/jobs/{id:int}", one), Route("/api/jobs/{id:int}/cancel", cancel, methods=["POST"]),
              Route("/api/songs", song_list), Route("/media/songs/{stem}", song_file),
              Mount("/media/cards", StaticFiles(directory=store.cards, follow_symlink=True)),
              Mount("/media/cuts", StaticFiles(directory=store.cuts)),
              Mount("/media/videos", StaticFiles(directory=store.videos, follow_symlink=True))]
    return routes, [worker.start], [worker.close]


def serve(uploads_dir: Path, site: Path | None, proxy: str, host: str, port: int, platform=None) -> None:
    import uvicorn

    print(f"网页服务监听 {host}:{port}，上传存到 {uploads_dir}", flush=True)
    uvicorn.run(create_app(uploads_dir, site, proxy, platform), host=host, port=port, log_level="warning")
