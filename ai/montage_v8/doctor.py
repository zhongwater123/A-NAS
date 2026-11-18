"""平台自检（python3 -m montage_v8 --profile rk3588 check）：板子到了先跑这个，逐项看配置里的东西能不能用。

不花钱、不改数据：只编码一段 2 秒的测试画面（在临时目录），不调用规划模型。
"""

import importlib.util
import os
import platform as pf
import shutil
import subprocess
import tempfile


def _line(ok: bool, what: str, detail: str = "") -> bool:
    print(("通过" if ok else "失败"), what, detail, flush=True)
    return ok


def _ffmpeg_lists(ff) -> tuple[str, str]:
    def listing(flag):
        done = subprocess.run([ff.binary, "-hide_banner", flag], capture_output=True, text=True, check=False)
        return done.stdout

    return listing("-hwaccels"), listing("-encoders")


def check(p) -> bool:
    ok = _line(True, "平台", f"{p.name}：{pf.machine()}，Python {pf.python_version()}")
    ff = p.ffmpeg
    ok &= _line(bool(shutil.which(ff.binary)), "FFmpeg", shutil.which(ff.binary) or f"找不到 {ff.binary}")
    if shutil.which(ff.binary):
        hwaccels, encoders = _ffmpeg_lists(ff)
        if "-hwaccel" in ff.decode:
            want = ff.decode[ff.decode.index("-hwaccel") + 1]
            ok &= _line(want in hwaccels.split(), "硬件解码", want)
        enc = ff.encode[ff.encode.index("-c:v") + 1] if "-c:v" in ff.encode else "libx264"
        ok &= _line(f" {enc} " in encoders, "编码器", enc)
        with tempfile.TemporaryDirectory() as work:
            src, out = os.path.join(work, "src.mp4"), os.path.join(work, "out.mkv")
            try:
                ff.run(["-f", "lavfi", "-i", "testsrc2=size=3840x2160:rate=30:duration=2", "-c:v", "libx264", "-preset", "ultrafast",
                        "-pix_fmt", "yuv420p", src])
                ff.run(ff.part_args(src, 0.0, 2.0, False, out))
                ok &= _line(os.path.getsize(out) > 0, "解码 4K 并编码一个镜头", f"{os.path.getsize(out)} 字节")
                frames = ff.grey(src, 0.0, 1.0, (256, 144), 2)
                ok &= _line(len(frames) >= 2, "按区间取灰度帧（用配置的硬件解码）", f"{len(frames)} 帧")
            except Exception as error:  # noqa: BLE001  自检：报出来，接着查下一项
                ok &= _line(False, "FFmpeg 编解码", str(error))
    import requests

    d = p.describer
    try:
        r = requests.get(f"{d.url}/v1/models", timeout=10)
        ok &= _line(r.ok, "视觉模型服务", f"{d.url}，schema 模式 {d.schema_mode}")
    except requests.RequestException as error:
        ok &= _line(False, "视觉模型服务", f"{d.url}：{error}")
    kind = type(p.planner).__name__
    if kind == "ClaudeApi":
        ok &= _line(importlib.util.find_spec("anthropic") is not None, "Anthropic SDK", "pip install anthropic")
        ok &= _line(bool(os.environ.get("ANTHROPIC_API_KEY")), "ANTHROPIC_API_KEY", "规划要调用 Claude API")
    else:
        ok &= _line(bool(shutil.which(p.planner.binary)), "Claude 命令行", p.planner.binary)
    for name, path in (("原片", p.store.videos), ("窗口索引", p.store.indexes), ("卡片", p.store.cards), ("剪辑结果", p.store.cuts)):
        _line(path.exists(), f"数据目录：{name}", str(path))
    return ok
