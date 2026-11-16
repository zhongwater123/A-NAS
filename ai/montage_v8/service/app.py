"""网页服务（Starlette + Uvicorn）：

GET  /                         网页（ai/montage_v8/web/index.html）
GET  /vendor/...               网页用的前端库
PUT  /api/uploads?group=&name=&modified=   请求体就是视频文件本身，边收边写盘；modified 是文件的修改时间（毫秒）
GET  /api/uploads              已经存下的视频，按组
GET  /uploads/<组>/<文件>      存下的视频（支持 Range，网页能拖着看）
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

PAGE = Path(__file__).resolve().parents[1] / "web"
PROXIED = ("/search", "/likeness", "/record", "/records")


def create_app(uploads_dir: Path, site: Path | None = None, proxy: str = "") -> Starlette:
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
        r = await client.request(request.method, request.url.path, params=request.query_params, content=await request.body(),
                                 headers={k: v for k, v in request.headers.items() if k.lower() in ("content-type",)})
        return Response(r.content, status_code=r.status_code, media_type=r.headers.get("content-type"))

    routes = [Route("/", page), Route("/index.html", page),
              Route("/api/uploads", listing, methods=["GET"]), Route("/api/uploads", receive, methods=["PUT"]),
              *(Route(p, forward, methods=["GET", "POST"]) for p in PROXIED),
              Mount("/vendor", StaticFiles(directory=PAGE / "vendor")),
              Mount("/uploads", StaticFiles(directory=uploads.root))]
    if site:
        routes.append(Mount("/", StaticFiles(directory=site, follow_symlink=True)))
    return Starlette(routes=routes, on_shutdown=[client.aclose] if client else [])


def serve(uploads_dir: Path, site: Path | None, proxy: str, host: str, port: int) -> None:
    import uvicorn

    print(f"网页服务监听 {host}:{port}，上传存到 {uploads_dir}", flush=True)
    uvicorn.run(create_app(uploads_dir, site, proxy), host=host, port=port, log_level="warning")
