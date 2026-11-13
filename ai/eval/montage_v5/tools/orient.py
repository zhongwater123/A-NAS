"""旋转标记写错的原片（rotation.json）：只查带 180° 旋转标记的视频，由本机 Qwen3.5-4B 判断。

规则分不出画面倒没倒（标 180° 的多数是对的：品杰 6 段只有 1 段倒），要看懂画面。每段取 FRAMES 帧，
按标记转好（FFmpeg 默认）后，原图和再转 180° 的图并排给 4B，问哪张是正的；两张的左右顺序各问一次，
免得它偏爱某一边。6 次里至少 SURE 次选“再转 180°”：标记写错，记进 rotation.json，渲染时翻正；至多
6-SURE 次：标记是对的；介于两者（4B 看不出，多是只有海和一条海平线的画面）：整段不用，记进
excluded.json（2026-11-10 用户定：手机标记翻转、模型又判断不出的，是素材本身的问题，直接剔掉）。

  python tools/orient.py http://127.0.0.1:8094 newzealand chuanxi pinjie     (只打印，--write 才写 rotation.json 和 excluded.json；
  判断不出的，人工看截图后改 rotation.json 或从 excluded.json 删掉，见 README)
"""
import base64
import io
import json
import subprocess
import sys
from pathlib import Path

import requests
from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))  # montage_v5
from paths import CARDS as HERE, INDEXES, VIDEOS

FRAMES = 3
SURE = 5
ASK = ("下面两张图是同一个画面，其中一张是上下颠倒的。根据天空、地面、水面、人和物体的朝向判断，"
       "哪一张是正着的？只回答 A 或 B。")


def rotation(path):
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream_side_data=rotation",  # noqa: S607
                          "-of", "csv=p=0", str(path)], capture_output=True, text=True, check=True).stdout
    return next((int(float(x)) for x in out.replace(",", "\n").split() if x.strip()), 0)


def frame(path, t):
    raw = subprocess.run(["ffmpeg", "-v", "error", "-ss", f"{t:.2f}", "-i", str(path), "-frames:v", "1",  # noqa: S607
                          "-vf", "scale=512:-2", "-f", "image2", "-c:v", "mjpeg", "-"], capture_output=True, check=True).stdout
    return Image.open(io.BytesIO(raw)).convert("RGB")


def part(im):
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=85)
    return {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64," + base64.b64encode(buf.getvalue()).decode()}}


def upright(url, a, b):
    """'A' or 'B': which of the two the 4B calls upright."""
    body = {"messages": [{"role": "user", "content": [{"type": "text", "text": ASK}, {"type": "text", "text": "A："}, part(a),
                                                      {"type": "text", "text": "B："}, part(b)]}],
            "temperature": 0, "max_tokens": 4, "chat_template_kwargs": {"enable_thinking": False},
            "response_format": {"type": "json_schema", "json_schema": {"name": "pick", "schema": {"type": "string", "enum": ["A", "B"]}}}}
    r = requests.post(f"{url}/v1/chat/completions", json=body, timeout=300)
    r.raise_for_status()
    return json.loads(r.json()["choices"][0]["message"]["content"])


def main():
    url, keys = sys.argv[1], [a for a in sys.argv[2:] if not a.startswith("--")]
    rot = json.loads((HERE / "rotation.json").read_text(encoding="utf-8"))
    for key in keys:
        d = next((INDEXES / f"video-moments-{key}").glob("embeddinggemma*"))
        videos = json.loads((d / "index.json").read_text(encoding="utf-8"))["videos"]
        wrong, unsure = [], []
        for vid, meta in sorted(videos.items()):
            path = VIDEOS / meta["file"]
            if abs(rotation(path)) != 180:
                continue
            flips = 0
            for k in range(FRAMES):
                im = frame(path, meta["duration"] * (k + 0.5) / FRAMES)
                turned = im.rotate(180)
                flips += upright(url, im, turned) == "B"
                flips += upright(url, turned, im) == "A"
            verdict = "倒" if flips >= SURE else "正" if flips <= 2 * FRAMES - SURE else "看不出，剔除"
            print(key, vid, Path(meta["file"]).name, f"选再转 180° {flips}/{2 * FRAMES}", verdict, flush=True)
            if verdict == "倒":
                wrong.append(Path(meta["file"]).name)
            elif verdict != "正":
                unsure.append({"video": vid, "from": 0, "to": meta["duration"], "file": Path(meta["file"]).name,
                               "why": f"带 180° 旋转标记，4B 判断不出方向（{flips}/{2 * FRAMES}）"})
        if "--write" in sys.argv:
            rot[key] = sorted(set(rot.get(key, [])) | set(wrong))
            ex = json.loads((HERE / "excluded.json").read_text(encoding="utf-8"))
            have = {x["video"] for x in ex.get(key, [])}
            ex[key] = ex.get(key, []) + [x for x in unsure if x["video"] not in have]
            (HERE / "excluded.json").write_text(json.dumps(ex, ensure_ascii=False, indent=1), encoding="utf-8")
    if "--write" in sys.argv:
        (HERE / "rotation.json").write_text(json.dumps(rot, ensure_ascii=False, indent=1), encoding="utf-8")


if __name__ == "__main__":
    main()
