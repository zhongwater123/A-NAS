"""第八版剪辑的命令行（在 ai/ 目录下运行）。

  python3 -m montage_v8 [--profile mac-dev] cards    CASE        切卡片、存关键帧
  python3 -m montage_v8 [--profile ...]     quality  CASE        量画质
  python3 -m montage_v8 [--profile ...]     describe CASE        视觉模型描述卡片
  python3 -m montage_v8 [--profile ...]     cut      CASE --music 歌 [--tag -1] [--free] [--wide] [--labels 文件] [--no-render]
  python3 -m montage_v8 [--profile ...]     music    歌曲文件                         分析歌曲（开发机）
  python3 -m montage_v8 [--profile ...]     check                                     看这个平台的 FFmpeg、模型服务能不能用
  python3 -m montage_v8 [--profile ...]     serve [--site 网站目录] [--host 0.0.0.0] [--port 8765] [--proxy 搜索服务]
                                                  网页服务：上传的视频存到原片目录下的 uploads/

--profile 默认是环境变量 MONTAGE_PROFILE，没有就是 mac-dev。--music 是歌曲文件，或 music-<歌名>.json 的歌名。
"""

import argparse
import json
import os
import sys

from . import config
from .adapters.store import read_json
from .domain.options import CutOptions
from .pipeline import Montage


def options(args) -> CutOptions:
    labels = read_json(args.labels)["labels"] if args.labels else {}
    return CutOptions(tag=args.tag, free_order=args.free, labels=labels, labels_file=args.labels or "", wide=args.wide,
                      quality=not args.keep_poor)


def main(argv=None):
    ap = argparse.ArgumentParser(prog="montage_v8", description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--profile", default=os.environ.get("MONTAGE_PROFILE", "mac-dev"))
    ap.add_argument("command", choices=["cards", "quality", "describe", "cut", "music", "check", "serve"])
    ap.add_argument("target", nargs="?", help="素材组（ne、ch、pj）或歌曲文件")
    ap.add_argument("--music", default="")
    ap.add_argument("--tag", default="")
    ap.add_argument("--free", action="store_true", help="不要求拍摄时间顺序")
    ap.add_argument("--wide", action="store_true", help="只用横拍的卡片")
    ap.add_argument("--labels", default="", help="人标的节拍精彩度（labels-<组>.json）")
    ap.add_argument("--keep-poor", action="store_true", help="不按画质过滤")
    ap.add_argument("--no-render", action="store_true", help="只填槽，写 cut-*.json，不出视频")
    ap.add_argument("--site", default="", help="serve：网站目录（第七版 build.py 搭的，data.js、成片、关键帧）")
    ap.add_argument("--host", default="127.0.0.1", help="serve：0.0.0.0 让局域网里的电脑也能访问")
    ap.add_argument("--port", type=int, default=8765)
    ap.add_argument("--proxy", default="", help="serve：向量搜索服务地址，如 http://127.0.0.1:8766")
    args = ap.parse_args(argv)

    platform = config.load(args.profile)
    montage = Montage(platform)
    if args.command == "check":
        from .doctor import check

        sys.exit(0 if check(platform) else 1)
    if args.command == "serve":
        from pathlib import Path

        from .service.app import serve

        serve(platform.store.videos / "uploads", Path(os.path.expanduser(args.site)) if args.site else None, args.proxy,
              args.host, args.port)
        return
    if args.command == "music":
        out = platform.music.music(args.target, 90.0)
        print(json.dumps({k: out[k] for k in ("tempo", "bar", "excerpt", "seconds")}, ensure_ascii=False))
        return
    cases = config.cases()
    if args.target not in cases:
        ap.error(f"素材组是 {'、'.join(cases)} 之一")
    case = cases[args.target]
    if args.command == "cards":
        montage.cards(case)
    elif args.command == "quality":
        montage.quality(case)
    elif args.command == "describe":
        montage.describe(case, options(args))
    elif args.command == "cut":
        if not args.music:
            ap.error("cut 要 --music")
        montage.cut(case, options(args), args.music, render=not args.no_render)


if __name__ == "__main__":
    main()
