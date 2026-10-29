"""Three judges score each montage of #322 from its frames, after CutClaw's evaluation.

CutClaw (docs/research/video-editing-cutclaw.md) has a model score visual quality
and instruction following, computes the cuts' sync with the music and leaves how
human the editing feels to a user study. These cuts have no music, so the three
judges here are:

  quality      how good the pictures look: CutClaw's aesthetic rubric (lighting,
               colour, composition, camera work, visual interest), plus defects
               such as black frames, flashes, blur and burnt-in text
  instruction  for each segment whether it shows what the instruction asks
               (on, partly, off), and the cut as a whole
  editing      pacing, repetition, still pictures held too long, flashes and how
               the segments follow one another, as an editor would see them

Each judge scores 0-10 against anchored levels and is one request to a vision model on an OpenAI-compatible API (the
development machine uses SiliconFlow; the key comes from SILICONFLOW_API_KEY and
never enters the repository) with temperature 0: the instruction, the segments'
lengths and one frame a second from each segment (at most FRAMES_MAX, short side
EDGE). Frames of the public test videos leave the machine only here, to judge;
the product keeps them local. Every request, minus the images, and every answer
are saved under <cache>/judge/<model>/.

The cuts are the versions eval.video_montage leaves in the model's cache
directory: the baseline (video_moments montage), montage-rules and montage-model,
and the third version's montage-rules3 and montage-model3.
With --summary nothing is sent: the saved verdicts are tabled, and where the
instructions label on-topic seconds, each segment's verdict is set against them
(a segment counts as on topic when at least half of it is).
"""

import argparse
import base64
import concurrent.futures
import json
import os
import subprocess
import time
import urllib.request

from eval import video_moments as vm

API = "https://api.siliconflow.cn/v1/chat/completions"
MODEL = "Qwen/Qwen3-VL-32B-Instruct"
FRAMES_MAX = 40
EDGE = 360
VERSIONS = ("baseline", "rules", "model", "rules3", "model3")

COMMON = """你在为一个家庭相册的“一句话剪视频”功能评审成片。成员的指令是：“{text}”，目标约 {seconds} 秒（时长是上限，素材不够时剪短是允许的）。
成片没有声音，共 {count} 段，按播放顺序：
{segments}
下面的图片按播放顺序排列，每秒一帧，每张图前面标了它属于第几段、在成片里的第几秒。只根据你看到的画面评价，不要猜测没看到的内容。"""

# Scores run 0-10, the even ones anchored, so that cuts that are all "fine" still spread out.
JUDGES = {
    "quality": """作为摄影与画面美学评审，评价这段成片的画面质量（按 Vlog 的标准，不按电影）。
评分维度：光线、色彩、构图、运镜（稳定、对焦、曝光）、画面吸引力。另外指出瑕疵：纯黑或纯色画面、一闪而过的无关画面、模糊、抖动、水印或字幕等叠加文字。
打 0–10 分，按下面的档位，介于两档之间就给奇数：
10 每一段都可以直接放进专业旅行短片：光线、色彩、构图出色，运镜稳；
8 整体好看，个别段落有小瑕疵（略暗、略晃、构图略偏）；
6 能用，但有几段明显平淡或有瑕疵（模糊、晃动、过曝欠曝、叠加文字）；
4 一半左右的画面质量差，或有明显的黑屏、闪帧、水印；
2 大部分画面质量差；
0 几乎不能看。
只输出 JSON：{"overall": 0-10, "lighting": 0-10, "color": 0-10, "composition": 0-10, "camera": 0-10, "interest": 0-10,
"defects": [{"segment": 段号, "what": "瑕疵"}], "analysis": "两三句话"}""",
    "instruction": """作为指令跟随评审，逐段判断画面是否符合成员的指令。
on：画面明确就是指令要的内容；partly：沾边但不完全是（比如只占画面一小部分，或只满足指令的一半条件）；off：不是指令要的内容。
再给整段成片打 0–10 分，按切题的时长占比，介于两档之间就给奇数；素材少导致成片短不扣分：
10 每一段都明确切题；
8 约 80% 的时长切题，其余沾边，没有跑题的段；
6 约 60% 切题，或有一段跑题；
4 约一半切题；
2 只有少数段切题；
0 完全跑题。
只输出 JSON：{"segments": [{"segment": 段号, "verdict": "on|partly|off", "seen": "画面是什么，一句话"}], "overall": 0-10, "analysis": "两三句话"}""",
    "editing": """作为剪辑师，评价这段成片剪得像不像人剪的：节奏是否合适，同一个画面是否重复或停留太久（几乎不动的画面超过约 6 秒算太长），有没有一闪而过的碎镜头，段与段之间的衔接和先后顺序是否顺，整体有没有一条线。
不评价画面是否切题，也不因为成片短扣分。
打 0–10 分，按下面的档位，介于两档之间就给奇数：
10 像专业剪辑师剪的：节奏贴合内容，衔接顺，有清楚的线索，没有重复和拖沓；
8 自然流畅，只有一两处小问题（某段略长、某处衔接略跳）；
6 能看，但有明显的生硬处：重复的画面、拖沓的段落或突兀的跳切；
4 多处生硬，或有一闪而过的碎镜头、明显的重复；
2 很生硬，像随便拼起来的；
0 无法观看。
只输出 JSON：{"overall": 0-10, "pacing": 0-10, "repetition": 0-10, "flow": 0-10,
"problems": [{"segment": 段号, "what": "问题"}], "analysis": "两三句话"}""",
}


def cuts(cache, instructions):
    """Each version's cut of each instruction found in the cache:
    (version, instruction, mp4, segment lengths, segments as (video, start, end) or card ids)."""
    for instruction in instructions:
        name = instruction["id"]
        base = os.path.join(vm.CACHE, "montage", name, f"{name}.json")
        if os.path.exists(base):
            with open(base, encoding="utf-8") as file:
                parts = json.load(file)["parts"]
            yield ("baseline", instruction, base[:-5] + ".mp4", [p["end"] - p["start"] for p in parts],
                   [(p["video"], p["start"], p["end"]) for p in parts])
        for version in VERSIONS[1:]:
            plan = os.path.join(cache, f"montage-{version}", f"{name}.json")
            if not os.path.exists(plan):
                continue
            with open(plan, encoding="utf-8") as file:
                segments = json.load(file)["segments"]
            if segments:
                yield (version, instruction, plan[:-5] + ".mp4", [s["end"] - s["start"] for s in segments],
                       [(s["card"], s["start"], s["end"]) for s in segments])


def frames(path, lengths):
    """(segment, second in the cut, JPEG) for one frame a second of each segment, at least one each."""
    times, start = [], 0.0
    for n, length in enumerate(lengths, 1):
        count = max(1, round(length))
        times += [(n, start + length * (k + 0.5) / count) for k in range(count)]
        start += length
    if len(times) > FRAMES_MAX:  # thinned evenly, keeping each segment's first frame
        step = -(-len(times) // FRAMES_MAX)
        firsts = {next(nt for nt in times if nt[0] == n) for n in range(1, len(lengths) + 1)}
        times = sorted(firsts | set(times[::step]), key=lambda nt: nt[1])
    out = []
    for n, t in times:
        jpeg = subprocess.run(
            ["ffmpeg", "-v", "error", "-ss", f"{t:.2f}", "-i", path, "-frames:v", "1",  # noqa: S607  FFmpeg from PATH
             "-vf", f"scale=-2:{EDGE}", "-f", "image2", "-c:v", "mjpeg", "-q:v", "4", "-"],
            capture_output=True, check=True).stdout
        out.append((n, t, jpeg))
    return out


def ask(model, prompt, images, key):
    content = [{"type": "text", "text": prompt}]
    for n, t, jpeg in images:
        content.append({"type": "text", "text": f"第 {n} 段，{t:.1f} 秒："})
        content.append({"type": "image_url",
                        "image_url": {"url": "data:image/jpeg;base64," + base64.b64encode(jpeg).decode()}})
    body = json.dumps({"model": model, "temperature": 0, "max_tokens": 2000,
                       "response_format": {"type": "json_object"},
                       "messages": [{"role": "user", "content": content}]}).encode()
    request = urllib.request.Request(API, body, {"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
    for attempt in range(3):
        try:
            with urllib.request.urlopen(request, timeout=300) as response:  # noqa: S310  the fixed API above
                answer = json.load(response)
            break
        except OSError:
            if attempt == 2:
                raise
            time.sleep(5)
    text = answer["choices"][0]["message"]["content"].strip()
    if text.startswith("```"):
        text = text.split("\n", 1)[1].rsplit("```", 1)[0]
    return json.loads(text), answer.get("usage", {})


def summary(cache, instructions, out):
    rows, agree, total = {}, 0, 0
    for version, instruction, _path, lengths, segments in cuts(cache, instructions):
        verdicts = {}
        for judge in JUDGES:
            path = os.path.join(out, f"{version}-{instruction['id']}-{judge}.json")
            if os.path.exists(path):
                with open(path, encoding="utf-8") as file:
                    verdicts[judge] = json.load(file)["verdict"]
        if len(verdicts) < len(JUDGES):
            continue
        said = {v.get("segment"): v.get("verdict") for v in verdicts["instruction"].get("segments", [])}
        row = {"seconds": round(sum(lengths), 1), **{j: verdicts[j].get("overall") for j in JUDGES},
               "on": sum(v == "on" for v in said.values()), "partly": sum(v == "partly" for v in said.values()),
               "off": sum(v == "off" for v in said.values()), "segments": len(lengths)}
        if instruction["onTopic"]:
            hits = 0
            for n, (where, start, end) in enumerate(segments, 1):
                video = where.rsplit("-", 2)[0]  # a video, or a card id that begins with one
                inside = sum(max(0.0, min(end, b) - max(start, a)) for v, a, b in instruction["onTopic"] if v == video)
                labelled = inside >= (end - start) / 2
                hits += labelled == (said.get(n) == "on")
            row["agree"] = f"{hits}/{len(segments)}"
            agree, total = agree + hits, total + len(segments)
        rows[f"{version}:{instruction['id']}"] = row
    with open(os.path.join(out, "summary.json"), "w", encoding="utf-8") as file:
        json.dump(rows, file, ensure_ascii=False, indent=2)
    keys = ("seconds", "quality", "instruction", "editing", "on", "partly", "off", "agree")
    print(f"{'cut':<22}" + "".join(f"{k:>12}" for k in keys))
    for name, row in rows.items():
        print(f"{name:<22}" + "".join(f"{row.get(k, '')!s:>12}" for k in keys))
    if total:
        print(f"judge's on/off agrees with the labels on {agree} of {total} segments")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", default=vm.SET, help="the videos, as video_moments --set")
    parser.add_argument("--instructions", default=os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                                                "video_montage.json"))
    parser.add_argument("--cache", required=True, help="the embedding model's directory under the set's cache")
    parser.add_argument("--model", default=MODEL, help="the judging vision model")
    parser.add_argument("--only", nargs="*", help="versions to judge, all by default")
    parser.add_argument("--summary", action="store_true", help="table the saved verdicts, send nothing")
    parser.add_argument("--workers", type=int, default=3, help="cuts judged at once")
    args = parser.parse_args()
    vm.use_set(args.set)
    with open(args.instructions, encoding="utf-8") as file:
        instructions = json.load(file)["instructions"]
    out = os.path.join(args.cache, "judge", args.model.replace("/", "_"))
    if args.summary:
        summary(args.cache, instructions, out)
        return
    key = os.environ["SILICONFLOW_API_KEY"]
    os.makedirs(out, exist_ok=True)
    jobs = [c for c in cuts(args.cache, instructions) if not args.only or c[0] in args.only]
    with concurrent.futures.ThreadPoolExecutor(args.workers) as pool:
        for _ in pool.map(lambda c: judge_cut(c, args.model, key, out), jobs):
            pass


def judge_cut(cut, model, key, out):
    version, instruction, path, lengths, _segments = cut
    images = frames(path, lengths)
    listing = "\n".join(f"第 {n} 段：{length:.1f} 秒" for n, length in enumerate(lengths, 1))
    common = COMMON.format(text=instruction["text"], seconds=instruction["seconds"], count=len(lengths),
                           segments=listing)
    for judge, task in JUDGES.items():
        prompt = common + "\n\n" + task
        verdict, usage = ask(model, prompt, images, key)
        with open(os.path.join(out, f"{version}-{instruction['id']}-{judge}.json"), "w", encoding="utf-8") as f:
            json.dump({"model": model, "cut": path, "lengths": lengths, "frames": [t for _n, t, _j in images],
                       "prompt": prompt, "verdict": verdict, "usage": usage}, f, ensure_ascii=False, indent=2)
        print(version, instruction["id"], judge, verdict.get("overall"), flush=True)


if __name__ == "__main__":
    main()
