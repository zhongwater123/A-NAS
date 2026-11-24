"""视频的窗口索引怎么建（和 A-NAS 视频索引器 eval/video_moments.py 的 2s1f 布局相同，第八版只建剪辑读的部分）：

每个视频从头起每 WINDOW 秒一个窗口（最后一个到视频结尾），窗口正中取一帧；这一帧缩到长边 EDGE、JPEG 质量 82 后交给
图文向量模型（EmbeddingGemma 2）编码；灰度的标准差小于 FLAT 的帧是黑场或纯色，不进卡片。索引器另有的镜头切点和
标签，剪辑不读，不建。

上传文件夹里的视频按 (修改时间, 文件名) 编号 001、002……；已经编过号的视频保留原编号，新加的接在后面，
所以往文件夹里再加视频，已有的卡片和规划不受影响。
"""

WINDOW = 2.0
EDGE = 512
FLAT = 8.0


def windows(duration_ms: int, seconds: float = WINDOW) -> list[tuple[int, int, int]]:
    """(起, 止, 取帧时刻) 毫秒：不重叠，正中一帧。"""
    out, start, length = [], 0, round(seconds * 1000)
    while start < duration_ms:
        end = min(start + length, duration_ms)
        out.append((start, end, min(max(start + (end - start) // 2, 0), duration_ms)))
        start += length
    return out


def numbered(files: list[tuple[str, float]], known: dict[str, str]) -> dict[str, str]:
    """{视频编号: 文件}。files：(文件, 修改时间)；known：已有索引里 {文件: 编号}，保持不变。"""
    out = {vid: f for f, vid in known.items() if f in {name for name, _ in files}}
    n = max((int(v) for v in known.values() if v.isdigit()), default=0)
    for f, _mtime in sorted(files, key=lambda x: (x[1], x[0])):
        if f in known:
            continue
        n += 1
        out[f"{n:03d}"] = f
    return dict(sorted(out.items()))
