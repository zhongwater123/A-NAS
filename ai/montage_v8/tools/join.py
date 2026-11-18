"""把一组原片里接连拍的视频先拼起来，再切卡片（2026-11-17，用户：同一个地方拍成好几条短视频，比如 Insta360 的早餐
拍了 4 条，拼起来才是一张卡片）。拼接点是硬切点：剪辑时一个镜头不跨过它（domain/fill.py，Footage.parts）。

按拍摄时间排好，下一条在上一条结束后 GAP 分钟内开始、编码和方向都一样的连成一组，用 FFmpeg 的 concat 不重新
编码地拼成一个文件，文件名是组号和第一条的开始时间（UTC），所以按文件名排的视频编号就是组号。另写
joined-parts.json：每个拼接文件由哪些原片拼成，各从拼接文件的第几秒开始。

方向不同的不拼：拼接文件只留一个旋转标记，竖拍的几段会横躺着（潜水拼接文件 003 的 13 条原片里，5 条竖拍的
那样放了出来，4B 也是对着横躺的画面描述的）。

  python3 -m montage_v8.tools.join 原片目录 拼接后的目录 [joined-parts.json 的路径，默认在卡片目录]
"""
import datetime
import json
import subprocess
import sys
from pathlib import Path

from montage_v8.tools import CARDS, write_json

GAP = 30    # 分钟


def probe(path: Path) -> tuple[datetime.datetime, float, tuple]:
    """(开始时间 UTC, 秒数, 能不能拼在一起的特征：编码、分辨率、帧率、像素格式、方向、有没有声音)。"""
    out = json.loads(subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration:format_tags=creation_time:stream=codec_type,codec_name,"  # noqa: S607
         "width,height,r_frame_rate,pix_fmt:stream_side_data=rotation", "-of", "json", str(path)],
        capture_output=True, text=True, check=True).stdout)
    video = next(s for s in out["streams"] if s["codec_type"] == "video")
    audio = [s["codec_name"] for s in out["streams"] if s["codec_type"] == "audio"]
    turn = next((int(float(d["rotation"])) for d in video.get("side_data_list", []) if "rotation" in d), 0)
    tag = out["format"].get("tags", {}).get("creation_time")
    start = (datetime.datetime.fromisoformat(tag.replace("Z", "+00:00")) if tag
             else datetime.datetime.fromtimestamp(path.stat().st_mtime, datetime.UTC))
    kind = (video["codec_name"], video["width"], video["height"], video["r_frame_rate"], video["pix_fmt"], turn,
            audio[0] if audio else None)
    return start, float(out["format"]["duration"]), kind


def runs(rows: list[tuple]) -> list[dict]:
    """rows：(开始, 秒数, 特征, 文件)，按开始时间排好。连着拍、特征相同的连成一组。"""
    groups = []
    for start, seconds, kind, path in rows:
        g = groups[-1] if groups else None
        if g and g["kind"] == kind and (start - g["end"]).total_seconds() <= GAP * 60:
            g["parts"].append((path, seconds))
            g["end"] = start + datetime.timedelta(seconds=seconds)
        else:
            groups.append({"kind": kind, "start": start, "end": start + datetime.timedelta(seconds=seconds),
                           "parts": [(path, seconds)]})
    return groups


def join(src: Path, dst: Path, parts_file: Path) -> dict:
    rows = sorted((*probe(f), f) for f in src.iterdir() if f.suffix.lower() in (".mp4", ".mov"))
    dst.mkdir(parents=True, exist_ok=True)
    parts = {}
    for n, g in enumerate(runs(rows), 1):
        out = dst / f"{n:03d}-{g['start']:%Y%m%d-%H%M%S}{g['parts'][0][0].suffix}"
        if not out.exists():
            listing = dst / f".{n:03d}.txt"
            listing.write_text("".join(f"file '{p.resolve()}'\n" for p, _ in g["parts"]), encoding="utf-8")
            sound = ["-map", "0:a:0"] if g["kind"][-1] else []
            subprocess.run(["ffmpeg", "-hide_banner", "-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", str(listing),  # noqa: S607
                            "-map", "0:v:0", *sound, "-c", "copy", "-movflags", "+faststart", str(out)], check=True)
            listing.unlink()
        at, parts[f"{n:03d}"] = 0.0, []
        for p, seconds in g["parts"]:
            parts[f"{n:03d}"].append({"file": p.name, "at": round(at, 3), "duration": round(seconds, 3)})
            at += seconds
        print(f"{n:03d}", len(g["parts"]), "条", round(at, 1), "秒", out.name, flush=True)
    write_json(parts, parts_file)
    return parts


if __name__ == "__main__":
    join(Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3]) if len(sys.argv) > 3 else CARDS / "joined-parts.json")
