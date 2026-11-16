"""上传的素材存在哪里、叫什么：<原片目录>/uploads/<组>/<文件名>。

组是网页上的一个文件夹：拖进来的文件夹用它的名字，零散选的视频进“新加的视频”。名字来自浏览器，只当文字用：
去掉路径分隔符和控制字符，不能是 . 或 ..，所以写不到 uploads 以外。同名文件大小相同时当作已经传过（再传一次不重复
存），大小不同时另起名字“名字 (2).mov”。
"""

import os
import re
import shutil
from dataclasses import dataclass
from pathlib import Path

VIDEO = {".mov", ".mp4", ".m4v", ".mts", ".avi", ".mkv"}
LONGEST = 120                       # 名字最多几个字符（不含扩展名）
SPARE = 1 << 30                     # 存完至少还留 1 GiB，别把磁盘写满


class UploadError(ValueError):
    """这次上传不能收：原因给用户看。"""


def clean(name: str, what: str = "名字") -> str:
    name = re.sub(r"[\x00-\x1f\x7f/\\:]", "", name or "").strip().strip(".")
    if not name:
        raise UploadError(f"{what}是空的")
    return name


def file_name(name: str) -> str:
    name = clean(Path(name.replace("\\", "/")).name, "文件名")
    stem, ext = os.path.splitext(name)
    if ext.lower() not in VIDEO:
        raise UploadError(f"只收视频文件（{'、'.join(sorted(e[1:].upper() for e in VIDEO))}），{name} 不是")
    return stem[:LONGEST] + ext


@dataclass
class Uploads:
    root: Path

    def __post_init__(self):
        self.root = Path(self.root)

    def target(self, group: str, name: str, size: int) -> tuple[Path, bool]:
        """(存到哪里, 是否已经有一样的)。"""
        folder = self.root / clean(group, "文件夹名")[:LONGEST]
        name = file_name(name)
        stem, ext = os.path.splitext(name)
        path, n = folder / name, 1
        while path.exists():
            if path.stat().st_size == size:
                return path, True
            n += 1
            path = folder / f"{stem} ({n}){ext}"
        return path, False

    def room(self, size: int) -> None:
        self.root.mkdir(parents=True, exist_ok=True)
        free = shutil.disk_usage(self.root).free
        if size + SPARE > free:
            raise UploadError(f"磁盘空间不够：还剩 {free / (1 << 30):.1f} GiB，这个文件 {size / (1 << 30):.1f} GiB，存完至少要留 1 GiB")

    def listing(self) -> list[dict]:
        """已经存下的：每组的文件，按修改时间（上传时设成了文件的拍摄或修改时间）。"""
        if not self.root.exists():
            return []
        out = []
        for folder in sorted(p for p in self.root.iterdir() if p.is_dir()):
            files = sorted((f for f in folder.iterdir() if f.is_file() and f.suffix.lower() in VIDEO),
                           key=lambda f: (f.stat().st_mtime, f.name))
            if files:
                out.append({"name": folder.name, "files": [{"name": f.name, "size": f.stat().st_size,
                                                            "modified": int(f.stat().st_mtime * 1000)} for f in files]})
        return out
